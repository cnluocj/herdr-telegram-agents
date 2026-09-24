package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// PicturesCall is one call of FakePictures.Pictures. Budget is how long
// the caller's context had left, zero without a deadline.
type PicturesCall struct {
	Dir    string
	Refs   []string
	Since  time.Time
	Budget time.Duration
}

// FakePictures implements domain.PictureSource in memory: Set scripts the
// picture a ref stands for, and every call is recorded. It ignores dir and
// since; resolving and dating files is the real finder's job.
type FakePictures struct {
	mu    sync.Mutex
	files map[string]domain.Picture
	calls []PicturesCall
}

var _ domain.PictureSource = (*FakePictures)(nil)

// NewFakePictures returns a source that knows no pictures.
func NewFakePictures() *FakePictures {
	return &FakePictures{files: map[string]domain.Picture{}}
}

// Set makes ref stand for p.
func (f *FakePictures) Set(ref string, p domain.Picture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[ref] = p
}

// Calls returns every call so far, in order.
func (f *FakePictures) Calls() []PicturesCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PicturesCall(nil), f.calls...)
}

// Pictures returns the scripted pictures of refs, in order, at most
// domain.MaxPictures.
func (f *FakePictures) Pictures(ctx context.Context, dir string, refs []string, since time.Time) []domain.Picture {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := PicturesCall{Dir: dir, Refs: append([]string(nil), refs...), Since: since}
	if deadline, ok := ctx.Deadline(); ok {
		call.Budget = time.Until(deadline)
	}
	f.calls = append(f.calls, call)
	var out []domain.Picture
	for _, ref := range refs {
		if p, ok := f.files[ref]; ok && len(out) < domain.MaxPictures {
			out = append(out, p)
		}
	}
	return out
}
