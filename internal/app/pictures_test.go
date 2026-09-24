package app

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// picturesFixture is a fixture in the given done mode whose outbound reads
// pictures from a fake source; the agent works in /work/shop.
func picturesFixture(t *testing.T, mode domain.DoneMode) (*bridgeFixture, *testkit.FakePictures, domain.Agent) {
	t.Helper()
	f := newBridgeFixture(t)
	if err := f.opts.Set(f.ctx, domain.OptionPostsDone, string(mode), 1); err != nil {
		t.Fatal(err)
	}
	src := testkit.NewFakePictures()
	src.Set("shots/home.png", domain.Picture{Path: "/work/shop/shots/home.png", Name: "home.png", Data: []byte("png"), Photo: true, Modified: tb0})
	src.Set("/tmp/full.png", domain.Picture{Path: "/tmp/full.png", Name: "full.png", Data: []byte("tall png"), Modified: tb0})
	f.out.SetPictures(src)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	a.Cwd = "/work/shop"
	f.agents[a.Key] = a
	return f, src, a
}

const picturesReply = "Done.\n\n截图：`shots/home.png`，整页在 /tmp/full.png。旧图 docs/old.png"

// workedTurn plays a turn the daemon sees start (working) and end (done)
// five seconds later, fires the done post and returns when it started.
func workedTurn(t *testing.T, f *bridgeFixture, a domain.Agent) time.Time {
	t.Helper()
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	started := f.clock.Now()
	f.clock.Advance(5 * time.Second)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	return started
}

func TestOutboundSendsPicturesAfterDonePost(t *testing.T) {
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	f.replies.Set(a.Key, picturesReply)
	workedTurn(t, f, a)
	calls := f.tg.Calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "send:101:Done.") || calls[1] != "pictures:101:home.png*|full.png" {
		t.Fatalf("calls = %q", calls)
	}
	// Any picture written in the last day goes, whoever wrote it.
	want := []testkit.PicturesCall{{Dir: "/work/shop", Refs: []string{"shots/home.png", "/tmp/full.png", "docs/old.png"}, Since: f.clock.Now().Add(-pictureMaxAge)}}
	if got := src.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("source calls = %+v, want %+v", got, want)
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"pictures posted"`) || !strings.Contains(f.logBuf.String(), `"photos":1`) {
		t.Errorf("log = %s", f.logBuf.String())
	}
}

func TestOutboundPicturesWithoutATurn(t *testing.T) {
	// A done post whose turn the daemon never saw start (it restarted
	// meanwhile) still carries the pictures: the window does not depend
	// on the turn.
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	f.replies.Set(a.Key, picturesReply)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	if calls := src.Calls(); len(calls) != 1 || !calls[0].Since.Equal(f.clock.Now().Add(-pictureMaxAge)) {
		t.Fatalf("source calls = %+v", calls)
	}
	if n := len(f.tg.Pictures()); n != 2 {
		t.Fatalf("pictures = %d", n)
	}
}

func TestOutboundPicturesOncePerTopic(t *testing.T) {
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	src.Set("new.png", domain.Picture{Path: "/work/shop/new.png", Name: "new.png", Data: []byte("new"), Photo: true, Modified: tb0})
	f.replies.Set(a.Key, picturesReply)
	workedTurn(t, f, a)
	// The next turn names the same two and a new one: only the new one goes.
	f.replies.Set(a.Key, "Again `shots/home.png`, /tmp/full.png and new.png")
	f.tg.Reset()
	workedTurn(t, f, a)
	if got := f.tg.Calls(); len(got) != 2 || got[1] != "pictures:101:new.png*" {
		t.Fatalf("second turn calls = %q", got)
	}
	if !strings.Contains(f.logBuf.String(), `"already_sent":2`) {
		t.Errorf("log = %s", f.logBuf.String())
	}
	// Nothing new at all: no upload.
	f.replies.Set(a.Key, "Still `shots/home.png`")
	f.tg.Reset()
	workedTurn(t, f, a)
	if got := f.tg.Calls(); len(got) != 1 {
		t.Fatalf("third turn calls = %q", got)
	}
	// A picture written again is a new picture.
	src.Set("shots/home.png", domain.Picture{Path: "/work/shop/shots/home.png", Name: "home.png", Data: []byte("png v2"), Photo: true, Modified: tb0.Add(time.Hour)})
	f.replies.Set(a.Key, "Redrawn `shots/home.png`")
	f.tg.Reset()
	workedTurn(t, f, a)
	if got := f.tg.Calls(); len(got) != 2 || got[1] != "pictures:101:home.png*" {
		t.Fatalf("rewritten picture calls = %q", got)
	}
	// Another topic gets it too.
	b := f.add(t, "p2", "t2", "reviewer-2", domain.StatusIdle)
	b.Cwd = "/work/shop"
	f.agents[b.Key] = b
	f.replies.Set(b.Key, "See `shots/home.png`")
	workedTurn(t, f, b)
	if got := f.tg.Calls(); len(got) != 2 || got[1] != "pictures:102:home.png*" {
		t.Fatalf("other topic calls = %q", got)
	}
}

func TestOutboundSentPicturesForgetOldEntries(t *testing.T) {
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	src.Set("old.png", domain.Picture{Path: "/work/shop/old.png", Name: "old.png", Data: []byte("old"), Modified: tb0.Add(-48 * time.Hour)})
	src.Set("new.png", domain.Picture{Path: "/work/shop/new.png", Name: "new.png", Data: []byte("new"), Modified: tb0})
	f.replies.Set(a.Key, "old.png and `shots/home.png`")
	workedTurn(t, f, a)
	f.replies.Set(a.Key, "new.png")
	workedTurn(t, f, a)
	sent := f.out.sentPictures[101]
	if len(sent) != 2 {
		t.Fatalf("sent = %v, want home.png and new.png only", sent)
	}
	for id := range sent {
		if strings.Contains(id, "old.png") {
			t.Fatalf("an entry older than the window is kept: %q", id)
		}
	}
}

func TestOutboundPicturesInScreenMode(t *testing.T) {
	// Screen mode reads the transcript for the pictures alone.
	f, src, a := picturesFixture(t, domain.DoneScreen)
	if err := f.opts.Set(f.ctx, domain.OptionPostsMeta, "false", 1); err != nil {
		t.Fatal(err)
	}
	f.herdr.SetScreen("p1", "screen tail")
	f.replies.Set(a.Key, picturesReply)
	workedTurn(t, f, a)
	assertCallsEqual(t, f.tg, "send:101:screen tail", "pictures:101:home.png*|full.png")
	if len(src.Calls()) != 1 {
		t.Fatalf("source calls = %+v", src.Calls())
	}
	// Off: no pictures, and in screen mode without the summary line no
	// transcript read either.
	f2, src2, a2 := picturesFixture(t, domain.DoneScreen)
	for key, value := range map[string]string{domain.OptionPostsMeta: "false", domain.OptionPostsPictures: "false"} {
		if err := f2.opts.Set(f2.ctx, key, value, 1); err != nil {
			t.Fatal(err)
		}
	}
	f2.herdr.SetScreen("p1", "screen tail")
	f2.replies.Set(a2.Key, picturesReply)
	workedTurn(t, f2, a2)
	assertCallsEqual(t, f2.tg, "send:101:screen tail")
	if len(src2.Calls()) != 0 || len(f2.replies.Calls()) != 0 {
		t.Fatalf("pictures off: source calls %+v, transcript reads %+v", src2.Calls(), f2.replies.Calls())
	}
}

func TestOutboundPicturesOffInReplyMode(t *testing.T) {
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	if err := f.opts.Set(f.ctx, domain.OptionPostsPictures, "false", 1); err != nil {
		t.Fatal(err)
	}
	f.replies.Set(a.Key, picturesReply)
	workedTurn(t, f, a)
	if calls := f.tg.Calls(); len(calls) != 1 || len(src.Calls()) != 0 {
		t.Fatalf("calls = %q, source calls = %+v", calls, src.Calls())
	}
}

func TestOutboundPicturesNeedAFreshReply(t *testing.T) {
	// A reply that names no picture asks the source nothing.
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	f.replies.Set(a.Key, "All tests pass.")
	workedTurn(t, f, a)
	if len(src.Calls()) != 0 {
		t.Fatalf("source calls = %+v", src.Calls())
	}
	// A transcript of an earlier turn is not this turn's reply: the post
	// falls back to the screen and sends no pictures.
	f2, src2, a2 := picturesFixture(t, domain.DoneFormatted)
	f2.herdr.SetScreen("p1", "screen tail")
	f2.replies.Set(a2.Key, picturesReply)
	f2.replies.SetMeta(a2.Key, domain.TurnMeta{Started: tb0.Add(-time.Minute)}, tb0.Add(-time.Second))
	workedTurn(t, f2, a2)
	assertCallsEqual(t, f2.tg, "send:101:screen tail")
	if len(src2.Calls()) != 0 {
		t.Fatalf("stale transcript source calls = %+v", src2.Calls())
	}
	// A done post skipped as a duplicate sends its pictures only once.
	f3, src3, a3 := picturesFixture(t, domain.DoneFormatted)
	f3.replies.Set(a3.Key, picturesReply)
	f3.replies.SetMeta(a3.Key, domain.TurnMeta{Started: tb0}, tb0.Add(4*time.Second))
	workedTurn(t, f3, a3)
	f3.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f3.setStatus(a3, domain.StatusDone)})
	f3.fire(t, 1)
	if len(src3.Calls()) != 1 || len(f3.tg.Pictures()) != 2 {
		t.Fatalf("duplicate: source calls %+v, pictures %d", src3.Calls(), len(f3.tg.Pictures()))
	}
}

func TestOutboundPicturesFailures(t *testing.T) {
	f, _, a := picturesFixture(t, domain.DoneFormatted)
	f.replies.Set(a.Key, picturesReply)
	f.tg.FailNext("pictures", errors.New("sendMediaGroup: network down"))
	workedTurn(t, f, a) // fire fails the test on an error
	if !strings.Contains(f.logBuf.String(), `"msg":"pictures failed"`) {
		t.Errorf("log = %s", f.logBuf.String())
	}
	// A failed upload marks nothing as sent: the next reply naming the
	// pictures sends them.
	f.replies.Set(a.Key, "Once more: `shots/home.png` and /tmp/full.png")
	workedTurn(t, f, a)
	if n := len(f.tg.Pictures()); n != 2 {
		t.Fatalf("pictures after a failed upload = %d", n)
	}
	// A fatal Telegram error still reaches the daemon.
	f2, _, a2 := picturesFixture(t, domain.DoneFormatted)
	f2.replies.Set(a2.Key, picturesReply)
	f2.tg.FailNext("pictures", fmt.Errorf("sendMediaGroup: %w", domain.ErrBotUnauthorized))
	f2.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f2.setStatus(a2, domain.StatusWorking)})
	f2.clock.Advance(5 * time.Second)
	f2.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f2.setStatus(a2, domain.StatusDone)})
	f2.clock.Advance(screenSettle)
	key := <-f2.out.Due()
	if err := f2.out.Fire(f2.ctx, key); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("Fire = %v, want the fatal error", err)
	}
}

func TestOutboundPictureNamesRedacted(t *testing.T) {
	f, src, a := picturesFixture(t, domain.DoneFormatted)
	src.Set("leak.png", domain.Picture{Name: "shot-" + testBotToken + ".png", Data: []byte("png"), Photo: true})
	f.replies.Set(a.Key, "See leak.png")
	workedTurn(t, f, a)
	pics := f.tg.Pictures()
	if len(pics) != 1 || strings.Contains(pics[0].Name, testBotToken) {
		t.Fatalf("pictures = %+v", pics)
	}
}

func TestBridgeUploadsPicturesAsTheirOwnJob(t *testing.T) {
	r := newRunningBridge(t)
	src := testkit.NewFakePictures()
	src.Set("shots/home.png", domain.Picture{Name: "home.png", Data: []byte("png"), Photo: true})
	r.bridge.out.replies = r.replies
	r.bridge.out.SetPictures(src)
	a := r.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	r.replies.Set(a.Key, "Screenshot: shots/home.png")
	r.replies.SetMeta(a.Key, domain.TurnMeta{Started: tb0}, tb0.Add(time.Second))
	r.herdr.SetScreen("p1", "screen tail")
	r.bridge.Submit(AgentEvent{Kind: AgentChanged, Agent: r.setStatus(a, domain.StatusDone)})
	waitUntil(t, "settle timer armed", func() bool { return r.clock.Pending() == 1 })
	r.clock.Advance(screenSettle)
	waitUntil(t, "pictures", func() bool { return len(r.tg.Pictures()) == 1 })
	calls := src.Calls()
	if len(calls) != 1 || calls[0].Budget <= bridgeCallTimeout {
		t.Fatalf("source calls = %+v, want a budget above the %s call timeout", calls, bridgeCallTimeout)
	}
	if got := r.tg.Calls(); len(got) != 2 || got[1] != "pictures:101:home.png*" {
		t.Fatalf("calls = %q", got)
	}
}
