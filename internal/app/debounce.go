package app

import (
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// debouncer coalesces edits per agent key: Schedule (re)starts a trailing
// timer and, when it fires, the key is delivered on Due. The owner runs the
// actual edit on its own goroutine, so topic writes stay single-writer.
type debouncer struct {
	clock domain.Clock
	delay time.Duration
	log   *slog.Logger

	mu      sync.Mutex
	pending map[domain.Key]scheduledEdit
	due     chan domain.Key
}

type scheduledEdit struct {
	cancel chan struct{}
	dueAt  time.Time
}

func newDebouncer(clock domain.Clock, delay time.Duration, log *slog.Logger) *debouncer {
	return &debouncer{
		clock:   clock,
		delay:   delay,
		log:     log,
		pending: map[domain.Key]scheduledEdit{},
		due:     make(chan domain.Key, 256),
	}
}

// Due delivers keys whose timer fired.
func (d *debouncer) Due() <-chan domain.Key { return d.due }

// Schedule arms (or re-arms) the timer for key with the default delay.
func (d *debouncer) Schedule(key domain.Key) { d.ScheduleAfter(key, d.delay) }

// ScheduleAfter arms (or re-arms) the timer for key with an explicit delay.
func (d *debouncer) ScheduleAfter(key domain.Key, delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if timer, ok := d.pending[key]; ok {
		close(timer.cancel)
	}
	cancel := make(chan struct{})
	d.pending[key] = scheduledEdit{cancel: cancel, dueAt: d.clock.Now().Add(delay)}
	timer := d.clock.After(delay)
	d.log.Debug("edit scheduled", slog.String("key", key.String()), slog.Int64("delay_ms", delay.Milliseconds()))
	go func() {
		select {
		case <-timer:
		case <-cancel:
			return
		}
		d.mu.Lock()
		if d.pending[key].cancel != cancel {
			d.mu.Unlock()
			return
		}
		delete(d.pending, key)
		d.mu.Unlock()
		d.due <- key
	}()
}

// Cancel drops a pending timer for key, if any.
func (d *debouncer) Cancel(key domain.Key) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	timer, ok := d.pending[key]
	if !ok {
		return false
	}
	close(timer.cancel)
	delete(d.pending, key)
	d.log.Debug("edit cancelled", slog.String("key", key.String()))
	return true
}

// Move transfers a pending timer to a new identity key while preserving its
// remaining delay.
func (d *debouncer) Move(from, to domain.Key) bool {
	if from == to {
		return false
	}
	d.mu.Lock()
	timer, ok := d.pending[from]
	if !ok {
		d.mu.Unlock()
		return false
	}
	remaining := timer.dueAt.Sub(d.clock.Now())
	close(timer.cancel)
	delete(d.pending, from)
	if prior, exists := d.pending[to]; exists {
		close(prior.cancel)
		delete(d.pending, to)
	}
	d.mu.Unlock()
	if remaining < 0 {
		remaining = 0
	}
	d.ScheduleAfter(to, remaining)
	return true
}

// Drain cancels every pending timer and returns the keys, in a stable
// order, so the owner can fire them immediately.
func (d *debouncer) Drain() []domain.Key {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]domain.Key, 0, len(d.pending))
	for key, timer := range d.pending {
		close(timer.cancel)
		keys = append(keys, key)
	}
	d.pending = map[domain.Key]scheduledEdit{}
	sortKeys(keys)
	return keys
}

// Pending returns the number of armed timers.
func (d *debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}
