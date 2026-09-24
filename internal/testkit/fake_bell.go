package testkit

import (
	"context"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeBell implements domain.Bell in memory: Ring records at once (the real
// bell sends in the background), Send records and returns the scripted
// error.
type FakeBell struct {
	mu    sync.Mutex
	rings []domain.Ring
	sent  []domain.Ring
	err   error
}

var _ domain.Bell = (*FakeBell)(nil)

// NewFakeBell returns an empty bell.
func NewFakeBell() *FakeBell { return &FakeBell{} }

// Ring records r.
func (b *FakeBell) Ring(_ context.Context, r domain.Ring) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rings = append(b.rings, r)
}

// Send records r unless a failure is scripted.
func (b *FakeBell) Send(_ context.Context, r domain.Ring) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.sent = append(b.sent, r)
	return nil
}

// FailSend makes every Send return err; nil clears it.
func (b *FakeBell) FailSend(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
}

// Rings returns a copy of the rings so far.
func (b *FakeBell) Rings() []domain.Ring {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Ring(nil), b.rings...)
}

// Sent returns a copy of the pushes Send accepted.
func (b *FakeBell) Sent() []domain.Ring {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Ring(nil), b.sent...)
}
