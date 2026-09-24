package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestPlainText(t *testing.T) {
	md := "## Result\n\n\n**Done**: see `a.go` and [the docs](https://x.y/z).\n\n- one\n  * two\n> quoted\n```go\nx := 1\n```\n\n"
	want := "Result\n\nDone: see a.go and the docs.\n\n• one\n  • two\nquoted\nx := 1"
	if got := plainText(md); got != want {
		t.Errorf("plainText =\n%q\nwant\n%q", got, want)
	}
}

func TestHeadAndTailBytes(t *testing.T) {
	s := strings.Repeat("回复", 50) // 300 bytes, 3 per rune
	for _, max := range []int{10, 11, 12, 100, 299} {
		head, tail := headBytes(s, max), tailBytes(s, max)
		for name, got := range map[string]string{"head": head, "tail": tail} {
			if len(got) > max || !utf8.ValidString(got) || !strings.Contains(got, "…") {
				t.Errorf("%s(%d) = %q (%d bytes)", name, max, got, len(got))
			}
		}
		if !strings.HasPrefix(head, "回") || !strings.HasSuffix(tail, "复") {
			t.Errorf("head/tail(%d) = %q / %q", max, head, tail)
		}
	}
	if headBytes(s, 300) != s || tailBytes(s, 300) != s {
		t.Error("text within the limit must pass unchanged")
	}
}

func TestRingTitle(t *testing.T) {
	a := domain.Agent{WorkspaceLabel: "yimall", TabLabel: "1", Kind: "claude"}
	if got := ringTitle("🏆", a); got != "🏆 yimall · 1 · claude" {
		t.Errorf("title = %q", got)
	}
	a.Name = "claude-review"
	if got := ringTitle("", a); got != "yimall · claude-review" {
		t.Errorf("title naming the kind = %q", got)
	}
}

// bellFixture is a fixture whose outbound rings a fake bell through the
// posts' redactor.
func bellFixture(t *testing.T) (*bridgeFixture, *testkit.FakeBell) {
	t.Helper()
	f := newBridgeFixture(t)
	bell := testkit.NewFakeBell()
	red := domain.NewRedactor(testBotToken)
	f.out.SetBell(bell, func(s string) string { s, _ = red.Redact(s); return s })
	return f, bell
}

const linkPrefix = "https://t.me/c/1234567890/101/"

func TestOutboundRingsDonePosts(t *testing.T) {
	f, bell := bellFixture(t)
	if err := f.opts.Set(f.ctx, domain.OptionPostsDone, string(domain.DoneFormatted), 1); err != nil {
		t.Fatal(err)
	}
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.replies.Set(a.Key, "## Done\n\n**All** tests pass with "+testBotToken+".")
	f.replies.SetMeta(a.Key, domain.TurnMeta{Model: "claude-fable-5-1", Started: tb0, Ended: tb0.Add(4 * time.Minute)}, f.clock.Now().Add(-time.Second))
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	rings := bell.Rings()
	if len(rings) != 1 {
		t.Fatalf("rings = %+v", rings)
	}
	r := rings[0]
	wantTitle := domain.DefaultStatusIcons().Done + " ws · reviewer"
	if r.Title != wantTitle || r.Subtitle != "⏱ 4 min · fable-5-1" || r.Urgent || r.Group != "ws · reviewer" {
		t.Errorf("ring = %+v, want title %q", r, wantTitle)
	}
	if !strings.HasPrefix(r.Body, "Done\n\nAll tests pass with ") || strings.Contains(r.Body, testBotToken) || strings.Contains(r.Body, "**") {
		t.Errorf("body = %q", r.Body)
	}
	if !strings.HasPrefix(r.URL, linkPrefix) || len(r.URL) == len(linkPrefix) {
		t.Errorf("url = %q", r.URL)
	}
	// Screen mode rings with the end of the screen.
	f2, bell2 := bellFixture(t)
	b := f2.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f2.herdr.SetScreen("p1", "line one\nline two")
	f2.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f2.setStatus(b, domain.StatusDone)})
	f2.fire(t, 1)
	if rings := bell2.Rings(); len(rings) != 1 || rings[0].Body != "line one\nline two" {
		t.Fatalf("screen rings = %+v", rings)
	}
}

func TestOutboundRingsQuestionsOnce(t *testing.T) {
	f, bell := bellFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "Allow?\n1. Yes\n2. No")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	// The same question again is a duplicate: no post, no second ring.
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	rings := bell.Rings()
	if len(rings) != 1 {
		t.Fatalf("rings = %+v", rings)
	}
	r := rings[0]
	if !r.Urgent || r.Title != domain.DefaultStatusIcons().Blocked+" ws · reviewer" || r.Body != "1. Yes\n2. No" || !strings.HasPrefix(r.URL, linkPrefix) {
		t.Errorf("question ring = %+v", r)
	}
}

func TestOutboundRingsNothingAtTheDesk(t *testing.T) {
	f, bell := bellFixture(t)
	atDesk := true
	f.out.SetPresence(func() bool { return atDesk }, f.opts)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "done screen")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 1 {
		t.Fatalf("the topic post must still go out: %d sends", n)
	}
	if rings := bell.Rings(); len(rings) != 0 {
		t.Fatalf("rang at the desk: %+v", rings)
	}
	// The catch-up on leaving rings the questions still open.
	f.herdr.SetScreen("p1", "Allow?\n1. Yes\n2. No")
	f.setStatus(a, domain.StatusBlocked)
	atDesk = false
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	if rings := bell.Rings(); len(rings) != 1 || !rings[0].Urgent {
		t.Fatalf("catch-up rings = %+v", rings)
	}
}

func TestOutboundRingsIdleReplies(t *testing.T) {
	f, a := idleReplyFixture(t, true)
	bell := testkit.NewFakeBell()
	f.out.SetBell(bell, nil)
	idleTurn(t, f, a)
	if rings := bell.Rings(); len(rings) != 1 || rings[0].Body != "The whole answer." || rings[0].Urgent {
		t.Fatalf("idle reply rings = %+v", rings)
	}
}

func TestOutboundWithoutBell(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "done screen")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	assertCallsEqual(t, f.tg, "send:101:done screen")
}

func TestRingTest(t *testing.T) {
	bell := testkit.NewFakeBell()
	now := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	msg, err := RingTest(context.Background(), bell, "v1", now, nil)
	if err != nil || msg != "bark: rung" {
		t.Fatalf("RingTest = %q, %v", msg, err)
	}
	if sent := bell.Sent(); len(sent) != 1 || sent[0].Title != "🔔 Telegram Agents v1" || !strings.Contains(sent[0].Body, "2026-09-24 02:00 UTC") {
		t.Fatalf("sent = %+v", sent)
	}
	bell.FailSend(errors.New("bark: HTTP 400: failed to get device token"))
	if _, err := RingTest(context.Background(), bell, "v1", now, nil); err == nil || !strings.Contains(err.Error(), "bark test failed") {
		t.Fatalf("failing RingTest err = %v", err)
	}
}
