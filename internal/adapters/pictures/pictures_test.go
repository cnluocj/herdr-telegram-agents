package pictures

import (
	"bytes"
	"context"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func encode(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "jpeg":
		err = jpeg.Encode(&buf, img, nil)
	case "gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// place writes data to dir/name with the given modification time.
func place(t *testing.T, dir, name string, data []byte, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPictures(t *testing.T) {
	root := t.TempDir()
	cwd, home := filepath.Join(root, "project"), filepath.Join(root, "home")
	since := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	fresh, stale := since.Add(time.Minute), since.Add(-time.Second)
	webp := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 8)...)

	shot := place(t, cwd, "shots/home.png", encode(t, "png", 1280, 720), fresh)
	photo := place(t, home, "Desktop/after.jpg", encode(t, "jpeg", 390, 844), fresh)
	anim := place(t, cwd, "demo.gif", encode(t, "gif", 20, 20), fresh)
	web := place(t, cwd, "demo.webp", webp, fresh)
	tall := place(t, root, "tmp/full-page.png", encode(t, "png", 300, 5200), fresh)
	place(t, cwd, "old.png", encode(t, "png", 10, 10), stale)
	place(t, cwd, "fake.png", []byte("not a picture at all"), fresh)
	place(t, cwd, "empty.png", nil, fresh)
	if err := os.MkdirAll(filepath.Join(cwd, "folder.png"), 0o755); err != nil {
		t.Fatal(err)
	}

	f := newFinder(func() (string, error) { return home, nil }, nil)
	refs := []string{
		"shots/home.png", "./shots/home.png", shot, // one file three ways
		"~/Desktop/after.jpg", "demo.gif", "demo.webp", tall,
		"old.png", "fake.png", "empty.png", "folder.png", "missing.png",
	}
	got := f.Pictures(context.Background(), cwd, refs, since)
	want := []struct {
		path  string
		photo bool
	}{{shot, true}, {photo, true}, {anim, false}, {web, false}, {tall, false}}
	if len(got) != len(want) {
		t.Fatalf("pictures = %d %v, want %d", len(got), names(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Path != w.path || g.Photo != w.photo || g.Name != filepath.Base(w.path) || len(g.Data) == 0 || !g.Modified.Equal(fresh) {
			t.Errorf("picture %d = %s photo=%v name=%s, want %s photo=%v", i, g.Path, g.Photo, g.Name, w.path, w.photo)
		}
	}

	// A relative ref needs a directory; "~/" needs a home.
	noHome := newFinder(func() (string, error) { return "", os.ErrNotExist }, nil)
	if got := noHome.Pictures(context.Background(), "", []string{"shots/home.png", "~/Desktop/after.jpg"}, since); len(got) != 0 {
		t.Fatalf("without dir and home = %v", names(got))
	}
}

func TestPicturesCapped(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	var refs []string
	for i := 0; i < domain.MaxPictures+3; i++ {
		name := string(rune('a'+i)) + ".png"
		place(t, dir, name, encode(t, "png", 4, 4), now)
		refs = append(refs, name)
	}
	got := New(nil).Pictures(context.Background(), dir, refs, now.Add(-time.Minute))
	if len(got) != domain.MaxPictures || got[0].Name != "a.png" {
		t.Fatalf("pictures = %v", names(got))
	}
}

func TestPhotoShape(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		want bool
	}{
		{1280, 720, true},
		{1170, 2532, true},
		{2880, 1800, true},
		{5120, 400, true},
		{5121, 400, false}, // Telegram would shrink it below half
		{300, 5200, false},
		{100, 2001, false}, // longer side over 20 times the shorter
		{100, 2000, true},
		{5000, 5001, false}, // width plus height over 10000
		{0, 10, false},
	} {
		if got := photoShape(tc.w, tc.h); got != tc.want {
			t.Errorf("photoShape(%d, %d) = %v, want %v", tc.w, tc.h, got, tc.want)
		}
	}
}

func names(pics []domain.Picture) []string {
	var out []string
	for _, p := range pics {
		out = append(out, p.Path)
	}
	return out
}
