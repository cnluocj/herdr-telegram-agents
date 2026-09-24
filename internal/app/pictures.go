package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// picturesJob is the follow-up of a done post whose reply names pictures:
// where to look (the agent's directory for relative paths), what the reply
// named and the oldest modification time still sent. The bridge runs it as
// a job of its own.
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

// followWithPictures queues the pictures a done post's reply names: any
// written in the last pictureMaxAge, whoever wrote them, so a reply that
// shows what another agent made, or what this one made a turn ago, still
// carries it. An old picture the reply merely mentions stays home, and
// SendPictures skips what the topic already got. An empty reply (no
// transcript read) sends nothing.
func (o *outbound) followWithPictures(ctx context.Context, key domain.Key, agent domain.Agent, threadID int, r domain.Reply) error {
	refs := domain.PictureRefs(r.Text)
	if len(refs) == 0 {
		return nil
	}
	job := picturesJob{key: key, threadID: threadID, dir: agent.Cwd, refs: refs, since: o.clock.Now().Add(-pictureMaxAge)}
	if o.submit != nil {
		o.submit(job)
		return nil
	}
	return o.SendPictures(ctx, job)
}

// SendPictures reads the pictures of a done post and uploads the ones its
// topic has not got yet. A failed upload is logged and marks nothing as
// sent; only fatal Telegram errors are returned.
func (o *outbound) SendPictures(ctx context.Context, j picturesJob) error {
	found := o.pictures.Pictures(ctx, j.dir, j.refs, j.since)
	sent := o.sentPictures[j.threadID]
	var pics []domain.Picture
	for _, p := range found {
		if _, ok := sent[pictureID(p)]; !ok {
			pics = append(pics, p)
		}
	}
	if len(pics) == 0 {
		o.log.Debug("no pictures to send", slog.String("key", j.key.String()), slog.Int("refs", len(j.refs)), slog.Int("already_sent", len(found)))
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
		o.markSent(j.threadID, pics, j.since)
		o.log.Info("pictures posted", slog.String("key", j.key.String()), slog.Int("thread_id", j.threadID),
			slog.Int("pictures", len(pics)), slog.Int("photos", photos), slog.Int("already_sent", len(found)-len(pics)),
			slog.Int("bytes", size), slog.Int64("dur_ms", o.clock.Now().Sub(start).Milliseconds()))
	case isFatal(err):
		o.log.Error("pictures failed with a fatal telegram error", slog.String("key", j.key.String()), slog.String("err", err.Error()))
		return err
	default:
		o.log.Warn("pictures failed", slog.String("key", j.key.String()), slog.Int("thread_id", j.threadID),
			slog.Int("pictures", len(pics)), slog.Int("bytes", size), slog.String("err", err.Error()))
	}
	return nil
}

// markSent records pics as sent to the topic and forgets the entries
// written before since: the window keeps them from being sent anyway.
func (o *outbound) markSent(threadID int, pics []domain.Picture, since time.Time) {
	sent := o.sentPictures[threadID]
	if sent == nil {
		sent = map[string]time.Time{}
		o.sentPictures[threadID] = sent
	}
	for id, modified := range sent {
		if modified.Before(since) {
			delete(sent, id)
		}
	}
	for _, p := range pics {
		sent[pictureID(p)] = p.Modified
	}
}

// pictureID names one version of a picture file: its path, when it was
// written and its size. A file written again is a new picture.
func pictureID(p domain.Picture) string {
	return fmt.Sprintf("%s\x00%d\x00%d", p.Path, p.Modified.UnixNano(), len(p.Data))
}
