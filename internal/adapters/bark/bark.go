// Package bark rings the operator's iPhone through Bark
// (https://github.com/Finb/Bark): one JSON POST to the device's endpoint
// per ring. The endpoint carries the device key, so it is a secret like
// the bot token: it never appears in a log line or an error.
package bark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// sendTimeout bounds one push, the Bark server included.
	sendTimeout = 10 * time.Second
	// levelActive is Bark's ordinary notification; levelUrgent breaks
	// through Focus (iOS time-sensitive) and goes with urgentSound.
	levelActive = "active"
	levelUrgent = "timeSensitive"
	urgentSound = "alarm"
	// maxResponse bounds how much of the server's answer is read.
	maxResponse = 4 << 10
)

// payload is the JSON Bark accepts on POST /<device key>.
type payload struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body"`
	URL      string `json:"url,omitempty"`
	Group    string `json:"group,omitempty"`
	Level    string `json:"level,omitempty"`
	Sound    string `json:"sound,omitempty"`
}

// answer is Bark's reply; code 200 is success.
type answer struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Client implements domain.Bell for one Bark endpoint.
type Client struct {
	endpoint string
	http     *http.Client
	log      *slog.Logger
}

var _ domain.Bell = (*Client)(nil)

// New returns a client for endpoint, an http(s) URL with a host. The
// error never repeats the endpoint.
func New(endpoint string, log *slog.Logger) (*Client, error) {
	return newClient(endpoint, &http.Client{Timeout: sendTimeout}, log)
}

func newClient(endpoint string, hc *http.Client, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("bark_url is not an http(s) URL with a host")
	}
	return &Client{endpoint: endpoint, http: hc, log: log}, nil
}

// Ring sends r in the background and logs the outcome; the caller's
// cancellation does not cut a push already on its way.
func (c *Client) Ring(ctx context.Context, r domain.Ring) {
	go func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
		defer cancel()
		if err := c.Send(sctx, r); err != nil {
			c.log.Warn("bark ring failed", slog.String("title", r.Title), slog.String("err", err.Error()))
			return
		}
		c.log.Info("bark rung", slog.String("title", r.Title), slog.Bool("urgent", r.Urgent),
			slog.Int("body_bytes", len(r.Body)), slog.Bool("link", r.URL != ""))
	}()
}

// Send pushes r and waits for Bark's answer.
func (c *Client) Send(ctx context.Context, r domain.Ring) error {
	p := payload{Title: r.Title, Subtitle: r.Subtitle, Body: r.Body, URL: r.URL, Group: r.Group, Level: levelActive}
	if r.Urgent {
		p.Level, p.Sound = levelUrgent, urgentSound
	}
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("bark: encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("bark: bad request")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bark: %s", withoutURL(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	var a answer
	decoded := json.Unmarshal(raw, &a) == nil
	switch {
	case resp.StatusCode != http.StatusOK:
		if decoded && a.Message != "" {
			return fmt.Errorf("bark: HTTP %d: %s", resp.StatusCode, a.Message)
		}
		return fmt.Errorf("bark: HTTP %d", resp.StatusCode)
	case decoded && a.Code != 0 && a.Code != http.StatusOK:
		return fmt.Errorf("bark: code %d: %s", a.Code, a.Message)
	}
	return nil
}

// withoutURL drops the request URL (and with it the device key) that
// net/http puts into every transport error.
func withoutURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
