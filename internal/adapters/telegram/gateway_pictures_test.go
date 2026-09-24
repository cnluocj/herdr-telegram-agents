package telegram_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// mediaItem is one entry of a sendMediaGroup media list.
type mediaItem struct {
	Type                        string `json:"type"`
	Media                       string `json:"media"`
	Caption                     string `json:"caption"`
	DisableContentTypeDetection bool   `json:"disable_content_type_detection"`
}

func mediaOf(t *testing.T, c call) []mediaItem {
	t.Helper()
	var items []mediaItem
	if err := json.Unmarshal([]byte(c.form.Get("media")), &items); err != nil {
		t.Fatalf("media = %q: %v", c.form.Get("media"), err)
	}
	return items
}

func pic(name, data string, photo bool) domain.Picture {
	return domain.Picture{Path: "/shots/" + name, Name: name, Data: []byte(data), Photo: photo}
}

func albumReply(url.Values) apiReply {
	return okReply([]map[string]any{{"message_id": 11}, {"message_id": 12}})
}

func TestSendPicturesAlbums(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMediaGroup", albumReply)
	pics := []domain.Picture{
		pic("home.png", "png one", true),
		pic("full page.png", "tall png", false),
		pic("home.png", "png two", true), // same name from another folder
		pic("demo.gif", "gif", false),
	}
	if err := h.gw.SendPictures(h.ctx, 42, pics); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMediaGroup")
	if len(calls) != 2 {
		t.Fatalf("sendMediaGroup calls = %d, want photos then files", len(calls))
	}
	photos, files := calls[0], calls[1]
	for _, c := range calls {
		if c.form.Get("message_thread_id") != "42" || c.form.Get("disable_notification") != "true" {
			t.Errorf("form = %v", c.form)
		}
	}
	want := []mediaItem{
		{Type: "photo", Media: "attach://home.png", Caption: "home.png"},
		{Type: "photo", Media: "attach://home-2.png", Caption: "home.png"},
	}
	if got := mediaOf(t, photos); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("photo media = %+v, want %+v", got, want)
	}
	if string(photos.files["home.png"].data) != "png one" || string(photos.files["home-2.png"].data) != "png two" {
		t.Fatalf("photo parts = %v", photos.files)
	}
	wantFiles := []mediaItem{
		{Type: "document", Media: "attach://full-page.png", DisableContentTypeDetection: true},
		{Type: "document", Media: "attach://demo.gif", DisableContentTypeDetection: true},
	}
	if got := mediaOf(t, files); len(got) != 2 || got[0] != wantFiles[0] || got[1] != wantFiles[1] {
		t.Fatalf("file media = %+v, want %+v", got, wantFiles)
	}
	if f := files.files["full-page.png"]; f.name != "full-page.png" || string(f.data) != "tall png" {
		t.Fatalf("file part = %+v", f)
	}
}

func TestSendPicturesSingles(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendPhoto", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 5}) })
	h.api.on("sendDocument", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 6}) })
	if err := h.gw.SendPictures(h.ctx, 42, []domain.Picture{pic("home.png", "png", true), pic("demo.webp", "webp", false)}); err != nil {
		t.Fatal(err)
	}
	photo := h.api.callsOf("sendPhoto")
	if len(photo) != 1 || photo[0].form.Get("caption") != "home.png" || photo[0].form.Get("disable_notification") != "true" ||
		string(photo[0].files["photo"].data) != "png" || photo[0].form.Get("message_thread_id") != "42" {
		t.Fatalf("sendPhoto = %+v", photo)
	}
	doc := h.api.callsOf("sendDocument")
	if len(doc) != 1 || doc[0].files["document"].name != "demo.webp" || doc[0].form.Get("disable_content_type_detection") != "true" ||
		doc[0].form.Get("caption") != "" || doc[0].form.Get("disable_notification") != "true" {
		t.Fatalf("sendDocument = %+v", doc)
	}
	if len(h.api.callsOf("sendMediaGroup")) != 0 {
		t.Fatal("a single picture needs no album")
	}
}

func TestSendPicturesRefusedPhotosGoAsFiles(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMediaGroup", func(form url.Values) apiReply {
		var items []mediaItem
		_ = json.Unmarshal([]byte(form.Get("media")), &items)
		if len(items) > 0 && items[0].Type == "photo" {
			return errReply(400, "Bad Request: PHOTO_INVALID_DIMENSIONS")
		}
		return albumReply(form)
	})
	h.api.on("sendDocument", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 6}) })
	pics := []domain.Picture{pic("a.png", "a", true), pic("b.png", "b", true), pic("c.gif", "c", false)}
	if err := h.gw.SendPictures(h.ctx, 42, pics); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMediaGroup")
	if len(calls) != 2 {
		t.Fatalf("sendMediaGroup calls = %d", len(calls))
	}
	got := mediaOf(t, calls[1])
	if len(got) != 3 || got[0].Media != "attach://a.png" || got[1].Media != "attach://b.png" || got[2].Media != "attach://c.gif" || got[0].Type != "document" {
		t.Fatalf("resent media = %+v", got)
	}
	// Any other failure is the caller's to see, and photos are not resent.
	h2 := newHarness(t)
	h2.api.on("sendPhoto", func(url.Values) apiReply { return errReply(403, "Forbidden: bot was kicked") })
	err := h2.gw.SendPictures(h2.ctx, 42, []domain.Picture{pic("a.png", "a", true), pic("c.gif", "c", false)})
	if !errors.Is(err, domain.ErrForbidden) || len(h2.api.callsOf("sendDocument")) != 0 {
		t.Fatalf("err = %v, documents = %d", err, len(h2.api.callsOf("sendDocument")))
	}
}

func TestSendPicturesRetryUploadsAgain(t *testing.T) {
	h := newHarness(t)
	n := 0
	h.api.on("sendMediaGroup", func(form url.Values) apiReply {
		n++
		if n == 1 {
			return errReply(502, "Bad Gateway")
		}
		return albumReply(form)
	})
	if err := h.gw.SendPictures(h.ctx, 42, []domain.Picture{pic("a.png", "first", true), pic("b.png", "second", true)}); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("sendMediaGroup")
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want a retry", len(calls))
	}
	if string(calls[1].files["a.png"].data) != "first" || string(calls[1].files["b.png"].data) != "second" {
		t.Fatalf("retried parts = %+v", calls[1].files)
	}
}
