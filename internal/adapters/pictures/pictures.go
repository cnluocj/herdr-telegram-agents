// Package pictures reads the image files an agent's reply names, so the
// done post can carry them into the topic. It implements
// domain.PictureSource on the local file system.
package pictures

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"  // registers GIF for DecodeConfig
	_ "image/jpeg" // registers JPEG for DecodeConfig
	_ "image/png"  // registers PNG for DecodeConfig
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// maxFileBytes is the most a bot may upload as a file.
	maxFileBytes = 50 << 20
	// maxPhotoBytes, maxPhotoSum and maxPhotoRatio are Telegram's limits
	// for a photo: its size, width plus height, and the longer side over
	// the shorter.
	maxPhotoBytes = 10 << 20
	maxPhotoSum   = 10000
	maxPhotoRatio = 20
	// maxPhotoSide keeps a picture readable: Telegram shrinks a photo to
	// 2560 px on its longer side, and a picture that would lose more than
	// half (a full-page screenshot) goes as a file instead.
	maxPhotoSide = 2 * 2560
)

// Finder implements domain.PictureSource.
type Finder struct {
	home func() (string, error)
	log  *slog.Logger
}

var _ domain.PictureSource = (*Finder)(nil)

// New returns a finder that expands "~/" with the user's home folder.
func New(log *slog.Logger) *Finder { return newFinder(os.UserHomeDir, log) }

func newFinder(home func() (string, error), log *slog.Logger) *Finder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Finder{home: home, log: log}
}

// Pictures implements domain.PictureSource. Each skipped ref is logged at
// debug with the reason.
func (f *Finder) Pictures(ctx context.Context, dir string, refs []string, since time.Time) []domain.Picture {
	var out []domain.Picture
	seen := map[string]bool{}
	for i, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		if len(out) == domain.MaxPictures {
			f.log.Debug("pictures capped", slog.Int("max", domain.MaxPictures), slog.Int("refs_left", len(refs)-i))
			break
		}
		path, ok := f.resolve(dir, ref)
		if !ok {
			f.log.Debug("picture skipped", slog.String("ref", ref), slog.String("reason", "relative without a directory"))
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		pic, reason := read(path, since)
		if reason != "" {
			f.log.Debug("picture skipped", slog.String("path", path), slog.String("reason", reason))
			continue
		}
		out = append(out, pic)
	}
	return out
}

// resolve turns a ref into a clean absolute path: "~/" against the home
// folder, a relative ref against dir.
func (f *Finder) resolve(dir, ref string) (string, bool) {
	switch {
	case strings.HasPrefix(ref, "~/"):
		home, err := f.home()
		if err != nil || home == "" {
			return "", false
		}
		return filepath.Join(home, ref[2:]), true
	case filepath.IsAbs(ref):
		return filepath.Clean(ref), true
	case dir == "":
		return "", false
	}
	return filepath.Join(dir, ref), true
}

// read loads one picture, or says why path is not one to send.
func read(path string, since time.Time) (domain.Picture, string) {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return domain.Picture{}, "missing"
	case !info.Mode().IsRegular():
		return domain.Picture{}, "not a file"
	case info.ModTime().Before(since):
		return domain.Picture{}, "written before the turn"
	case info.Size() == 0:
		return domain.Picture{}, "empty"
	case info.Size() > maxFileBytes:
		return domain.Picture{}, "too big"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.Picture{}, "unreadable"
	}
	if len(data) > maxFileBytes {
		return domain.Picture{}, "too big"
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil && !isWebP(data) {
		return domain.Picture{}, "not a picture"
	}
	photo := (format == "png" || format == "jpeg") && len(data) <= maxPhotoBytes && photoShape(cfg.Width, cfg.Height)
	return domain.Picture{Path: path, Name: filepath.Base(path), Data: data, Photo: photo}, ""
}

// isWebP reports a RIFF container of WebP data, which the standard
// library cannot decode.
func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// photoShape reports whether Telegram takes a picture of these dimensions
// as a photo and keeps it readable.
func photoShape(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	long, short := max(w, h), min(w, h)
	return w+h <= maxPhotoSum && long <= maxPhotoRatio*short && long <= maxPhotoSide
}
