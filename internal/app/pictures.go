package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// picturesJob is the follow-up of a done post whose reply names pictures:
// where to look (the agent's directory for relative paths), what the reply
// named and when the turn started. The bridge runs it as a job of its own.
type picturesJob struct {
	key      domain.Key
	threadID int
	dir      string
	refs     []string
	since    time.Time
}

// SetPictures wires the reader of the pictures a done reply names; nil
// sends none.
func (o *outbound) SetPictures(src domain.PictureSource) { o.pictures = src }

// followWithPictures queues the pictures a done post's reply names. Only a
// picture written during the turn is sent, so the turn's start must be
// known: the prompt the transcript records or, when earlier, the first
// working status the daemon saw. Without either nothing is sent, since
// any picture could be an old one. An empty reply (no transcript read)
// sends nothing.
func (o *outbound) followWithPictures(ctx context.Context, key domain.Key, agent domain.Agent, threadID int, r domain.Reply, t turn, hasTurn bool) error {
	refs := domain.PictureRefs(r.Text)
	if len(refs) == 0 {
		return nil
	}
	since := r.Meta.Started
	if hasTurn && !t.started.IsZero() && (since.IsZero() || t.started.Before(since)) {
		since = t.started
	}
	if since.IsZero() {
		o.log.Debug("pictures skipped", slog.String("key", key.String()), slog.String("reason", "turn start unknown"), slog.Int("refs", len(refs)))
		return nil
	}
	job := picturesJob{key: key, threadID: threadID, dir: agent.Cwd, refs: refs, since: since}
	if o.submit != nil {
		o.submit(job)
		return nil
	}
	return o.SendPictures(ctx, job)
}

// SendPictures reads the pictures of a done post and uploads them into its
// topic. A failed upload is logged; only fatal Telegram errors are
// returned.
func (o *outbound) SendPictures(ctx context.Context, j picturesJob) error {
	pics := o.pictures.Pictures(ctx, j.dir, j.refs, j.since)
	if len(pics) == 0 {
		o.log.Debug("no pictures to send", slog.String("key", j.key.String()), slog.Int("refs", len(j.refs)))
		return nil
	}
	photos, size := 0, 0
	for _, p := range pics {
		if p.Photo {
			photos++
		}
		size += len(p.Data)
	}
	start := o.clock.Now()
	err := o.tg.SendPictures(ctx, j.threadID, pics)
	switch {
	case err == nil:
		o.log.Info("pictures posted", slog.String("key", j.key.String()), slog.Int("thread_id", j.threadID),
			slog.Int("pictures", len(pics)), slog.Int("photos", photos), slog.Int("bytes", size),
			slog.Int64("dur_ms", o.clock.Now().Sub(start).Milliseconds()))
	case isFatal(err):
		o.log.Error("pictures failed with a fatal telegram error", slog.String("key", j.key.String()), slog.String("err", err.Error()))
		return err
	default:
		o.log.Warn("pictures failed", slog.String("key", j.key.String()), slog.Int("thread_id", j.threadID),
			slog.Int("pictures", len(pics)), slog.Int("bytes", size), slog.String("err", err.Error()))
	}
	return nil
}
