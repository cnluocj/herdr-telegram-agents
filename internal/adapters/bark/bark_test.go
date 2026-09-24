package bark

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// deviceKey stands for the secret part of a Bark endpoint.
const deviceKey = "SeCrEtDeViCeKeY"

type hit struct {
	method, path, contentType string
	body                      map[string]any
}

// server answers every push with status and reply and hands each request
// over on the returned channel.
func server(t *testing.T, status int, reply string) (*httptest.Server, <-chan hit) {
	t.Helper()
	hits := make(chan hit, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		hits <- hit{method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func TestSendPayload(t *testing.T) {
	srv, hits := server(t, http.StatusOK, `{"code":200,"message":"success"}`)
	c, err := New(srv.URL+"/"+deviceKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Send(ctx, domain.Ring{Title: "🏆 ws · claude", Subtitle: "⏱ 4 min", Body: "Done.", URL: "https://t.me/c/1/2/3", Group: "ws · claude"}); err != nil {
		t.Fatal(err)
	}
	h := <-hits
	if h.method != http.MethodPost || h.path != "/"+deviceKey || !strings.HasPrefix(h.contentType, "application/json") {
		t.Fatalf("request = %+v", h)
	}
	want := map[string]any{"title": "🏆 ws · claude", "subtitle": "⏱ 4 min", "body": "Done.", "url": "https://t.me/c/1/2/3", "group": "ws · claude", "level": "active"}
	if len(h.body) != len(want) {
		t.Fatalf("body = %v, want %v", h.body, want)
	}
	for k, v := range want {
		if h.body[k] != v {
			t.Errorf("%s = %v, want %v", k, h.body[k], v)
		}
	}
	// A question rings through Focus with the alarm sound.
	if err := c.Send(ctx, domain.Ring{Title: "❓ ws", Body: "1. Yes", Urgent: true}); err != nil {
		t.Fatal(err)
	}
	h = <-hits
	if h.body["level"] != "timeSensitive" || h.body["sound"] != "alarm" || h.body["subtitle"] != nil || h.body["url"] != nil {
		t.Fatalf("urgent body = %v", h.body)
	}
}

func TestSendErrorsKeepTheKeySecret(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		status int
		reply  string
		want   string
	}{
		"http status with message": {http.StatusBadRequest, `{"code":400,"message":"failed to get device token"}`, "bark: HTTP 400: failed to get device token"},
		"http status without json": {http.StatusBadGateway, `oops`, "bark: HTTP 502"},
		"code in a 200 answer":     {http.StatusOK, `{"code":500,"message":"push failed"}`, "bark: code 500: push failed"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := server(t, tc.status, tc.reply)
			c, err := New(srv.URL+"/"+deviceKey, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Send(ctx, domain.Ring{Body: "x"})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// A 200 without Bark's JSON (a self-hosted proxy, say) is success.
	srv, _ := server(t, http.StatusOK, `ok`)
	c, _ := New(srv.URL+"/"+deviceKey, nil)
	if err := c.Send(ctx, domain.Ring{Body: "x"}); err != nil {
		t.Fatalf("plain 200 err = %v", err)
	}
	// A transport error names neither the endpoint nor the key.
	dead := httptest.NewServer(http.NotFoundHandler())
	endpoint := dead.URL + "/" + deviceKey
	dead.Close()
	c, _ = New(endpoint, nil)
	err := c.Send(ctx, domain.Ring{Body: "x"})
	if err == nil || strings.Contains(err.Error(), deviceKey) || strings.Contains(err.Error(), dead.URL) {
		t.Fatalf("transport err = %v", err)
	}
}

func TestNewRejectsBadEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "  ", "api.day.app/" + deviceKey, "ftp://api.day.app/" + deviceKey, "https://", "https://%zz/" + deviceKey} {
		_, err := New(endpoint, nil)
		if err == nil {
			t.Errorf("New(%q) accepted", endpoint)
			continue
		}
		if strings.Contains(err.Error(), deviceKey) {
			t.Errorf("New(%q) error leaks the key: %v", endpoint, err)
		}
	}
	if _, err := New("  https://api.day.app/"+deviceKey+" ", nil); err != nil {
		t.Errorf("padded endpoint: %v", err)
	}
}

// syncBuffer is a log sink the background sender and the test may use at
// the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func TestRingSendsInTheBackground(t *testing.T) {
	logBuf := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logBuf, nil))
	srv, hits := server(t, http.StatusOK, `{"code":200}`)
	c, _ := New(srv.URL+"/"+deviceKey, log)
	ctx, cancel := context.WithCancel(context.Background())
	c.Ring(ctx, domain.Ring{Title: "🏆 ws", Body: "Done."})
	cancel() // the caller moving on does not cut the push
	select {
	case h := <-hits:
		if h.body["body"] != "Done." {
			t.Fatalf("body = %v", h.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ring never arrived")
	}
	waitFor(t, func() bool { return strings.Contains(logBuf.String(), `"msg":"bark rung"`) })

	// A failure is logged, never with the key.
	logBuf.Reset()
	bad, _ := server(t, http.StatusInternalServerError, ``)
	c, _ = New(bad.URL+"/"+deviceKey, log)
	c.Ring(context.Background(), domain.Ring{Title: "🏆 ws", Body: "Done."})
	waitFor(t, func() bool { return strings.Contains(logBuf.String(), `"msg":"bark ring failed"`) })
	if strings.Contains(logBuf.String(), deviceKey) {
		t.Fatalf("log leaks the key: %s", logBuf.String())
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
