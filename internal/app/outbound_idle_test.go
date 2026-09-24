package app

import (
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestOutboundScreenLinesOption(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.opts.Set(f.ctx, domain.OptionPostsScreenLines, "50", 1); err != nil {
		t.Fatal(err)
	}
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "long answer")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	assertCallsEqual(t, f.tg, "send:101:long answer")
	if reads := f.herdr.Reads(); len(reads) != 1 || reads[0].Lines != 50 {
		t.Fatalf("done reads = %+v, want one of 50 lines", reads)
	}
	// A question keeps its own tail length.
	f.herdr.SetScreen("p1", "Allow?\n1. Yes\n2. No")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if reads := f.herdr.Reads(); len(reads) != 2 || reads[1].Lines != blockedLines {
		t.Fatalf("blocked reads = %+v, want %d lines", reads, blockedLines)
	}
}

// idleTurn plays a prompt from the topic (message 2 in thread 101) that the
// agent works on and finishes as idle, the way Herdr reports a turn that
// ended in the focused tab.
func idleTurn(t *testing.T, f *bridgeFixture, a domain.Agent) {
	t.Helper()
	if err := f.out.PromptSent(f.ctx, a.Key, 101, 2); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Second)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	f.clock.Advance(3 * time.Second)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusIdle)})
	f.endTurns(t, 1)
}

func idleReplyFixture(t *testing.T, on bool) (*bridgeFixture, domain.Agent) {
	t.Helper()
	f := newBridgeFixture(t)
	for key, value := range map[string]string{
		domain.OptionPostsIdleReply: map[bool]string{true: "true", false: "false"}[on],
		domain.OptionPostsDone:      string(domain.DoneFormatted),
		domain.OptionPostsMeta:      "false",
	} {
		if err := f.opts.Set(f.ctx, key, value, 1); err != nil {
			t.Fatal(err)
		}
	}
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", "screen tail")
	f.replies.Set(a.Key, "The **whole** answer.")
	return f, a
}

func TestOutboundIdleReplyAnswersTelegramPrompt(t *testing.T) {
	f, a := idleReplyFixture(t, true)
	idleTurn(t, f, a)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Text != "The **whole** answer." || !sent[0].Markdown || sent[0].ThreadID != 101 || sent[0].Notify {
		t.Fatalf("Sent = %+v", sent)
	}
	for _, line := range []string{`"msg":"idle turn posted as done","key":"p1/t1","message_id":2`, `"msg":"reply posted"`} {
		if !strings.Contains(f.logBuf.String(), line) {
			t.Errorf("log lacks %s", line)
		}
	}
	if _, open := f.out.turns[a.Key]; open {
		t.Fatal("turn still open after the idle reply")
	}
	// The same answer again (a second prompt the agent answered
	// identically) is a duplicate and stays unposted.
	idleTurn(t, f, a)
	if n := len(f.tg.Sent()); n != 1 {
		t.Fatalf("duplicate posted: %d sends", n)
	}
}

func TestOutboundIdleReplyStaleTranscriptFallsBackToScreen(t *testing.T) {
	f, a := idleReplyFixture(t, true)
	// The transcript was last written before the turn began: no answer
	// of this turn in it, so the screen goes out instead.
	f.replies.SetMeta(a.Key, domain.TurnMeta{}, f.clock.Now().Add(-time.Minute))
	idleTurn(t, f, a)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Text != "screen tail" || !sent[0].Code {
		t.Fatalf("Sent = %+v", sent)
	}
	if !strings.Contains(f.logBuf.String(), "stale transcript") {
		t.Error("log lacks the stale transcript reason")
	}
}

func TestOutboundIdleReplyLeavesOtherTurnsAlone(t *testing.T) {
	t.Run("option off", func(t *testing.T) {
		f, a := idleReplyFixture(t, false)
		idleTurn(t, f, a)
		if calls := f.tg.Calls(); len(calls) != 0 {
			t.Fatalf("calls with the option off = %q", calls)
		}
	})
	t.Run("turn typed in Herdr", func(t *testing.T) {
		f, a := idleReplyFixture(t, true)
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
		f.clock.Advance(3 * time.Second)
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusIdle)})
		f.endTurns(t, 1)
		if calls := f.tg.Calls(); len(calls) != 0 {
			t.Fatalf("calls for a turn without a prompt = %q", calls)
		}
	})
	t.Run("prompt never started", func(t *testing.T) {
		f, a := idleReplyFixture(t, true)
		if err := f.out.PromptSent(f.ctx, a.Key, 101, 2); err != nil {
			t.Fatal(err)
		}
		// An idle event without any working in between: the transcript
		// may still end on the previous answer.
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusIdle)})
		f.endTurns(t, 1)
		if calls := f.tg.Calls(); len(calls) != 0 {
			t.Fatalf("calls for a turn that never started = %q", calls)
		}
	})
	t.Run("done posts once", func(t *testing.T) {
		f, a := idleReplyFixture(t, true)
		if err := f.out.PromptSent(f.ctx, a.Key, 101, 2); err != nil {
			t.Fatal(err)
		}
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
		f.clock.Advance(3 * time.Second)
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
		f.fire(t, 1)
		// Done then idle (the operator looked at the tab): no second post.
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusIdle)})
		f.clock.Advance(turnSettle)
		select {
		case key := <-f.out.TurnDue():
			t.Fatalf("turn due after done: %v", key)
		case <-time.After(30 * time.Millisecond):
		}
		if n := len(f.tg.Sent()); n != 1 {
			t.Fatalf("sends = %d, want 1", n)
		}
	})
}
