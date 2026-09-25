package transcript

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Rollout lines in the shape Codex writes them (2026-09, trimmed).
const (
	codexMeta     = `{"timestamp":"2026-09-24T01:10:00.000Z","type":"session_meta","payload":{"session_id":"sid-1","cwd":"/p"}}`
	codexStarted1 = `{"timestamp":"2026-09-24T01:10:16.060Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":1790212216}}`
	codexContext1 = `{"timestamp":"2026-09-24T01:10:16.100Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"/p","model":"gpt-6-sol"}}`
	codexMessage1 = `{"timestamp":"2026-09-24T01:10:27.099Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Received."}]}}`
	codexTokens1a = `{"timestamp":"2026-09-24T01:10:20.000Z","type":"token_usage_record","payload":{"turn_id":"turn-1","usage":{"output_tokens":100},"turn_token_usage":{"output_tokens":100}}}`
	codexTokens1b = `{"timestamp":"2026-09-24T01:10:27.000Z","type":"token_usage_record","payload":{"turn_id":"turn-1","usage":{"output_tokens":2400},"turn_token_usage":{"output_tokens":2500}}}`
	codexDone1    = `{"timestamp":"2026-09-24T01:10:27.519Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"## Done\n\n- **one**\n- two","started_at":1790212216,"completed_at":1790212287,"duration_ms":71000}}`
	codexStarted2 = `{"timestamp":"2026-09-24T01:12:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-2","started_at":1790212320}}`
	codexAborted2 = `{"timestamp":"2026-09-24T01:12:05.000Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-2"}}`
	codexEmpty2   = `{"timestamp":"2026-09-24T01:12:05.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-2","last_agent_message":null,"started_at":1790212320,"completed_at":1790212325}}`
	codexCount1a  = `{"timestamp":"2026-09-24T01:10:21.000Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":40000,"total_tokens":40100},"model_context_window":258400}}}`
	codexCount1b  = `{"timestamp":"2026-09-24T01:10:27.100Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":71478,"total_tokens":73573},"model_context_window":258400}}}`
	codexNoise    = `{"timestamp":"2026-09-24T01:10:21.000Z","type":"event_msg","payload":{"type":"token_count","info":{}}}`
)

func writeRollout(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLastCodexReplyIn(t *testing.T) {
	complete := []string{codexMeta, codexStarted1, codexContext1, codexTokens1a, codexNoise, codexCount1a, codexMessage1, codexTokens1b, codexCount1b, codexNoise, codexDone1}
	tests := []struct {
		name    string
		lines   []string
		want    string
		wantErr string
	}{
		{"completed turn", complete, "## Done\n\n- **one**\n- two", ""},
		{"trailing garbage is skipped", append(append([]string(nil), complete...), `not json`), "## Done\n\n- **one**\n- two", ""},
		{"next turn still running", append(append([]string(nil), complete...), codexStarted2), "", "codex turn still running"},
		{"next turn aborted", append(append([]string(nil), complete...), codexStarted2, codexAborted2), "", "codex turn aborted"},
		{"turn without a message", append(append([]string(nil), complete...), codexStarted2, codexEmpty2), "", "codex turn ended without a message"},
		{"no turn at all", []string{codexMeta}, "", "rollout has no completed turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			writeRollout(t, path, tt.lines...)
			text, meta, stats, err := lastCodexReplyIn(path, defaultMaxScan)
			if tt.wantErr != "" {
				if !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want ErrNoReply with %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if text != tt.want {
				t.Errorf("text = %q, want %q", text, tt.want)
			}
			wantMeta := domain.TurnMeta{Model: "gpt-6-sol", OutputTokens: 2500, ContextTokens: 73573, ContextWindow: 258400,
				Started: time.Unix(1790212216, 0).UTC(), Ended: time.Unix(1790212287, 0).UTC()}
			if !reflect.DeepEqual(meta, wantMeta) {
				t.Errorf("meta = %+v, want %+v", meta, wantMeta)
			}
			if line := meta.Line(); line != "⏱ 1 min · gpt-6-sol · ↑ 2.5k tokens · 🧠 74k/258k (28%)" {
				t.Errorf("summary line = %q", line)
			}
			if stats.lines == 0 || stats.bytes == 0 {
				t.Errorf("stats = %+v", stats)
			}
		})
	}
	// Without completed_at the end is the start plus duration_ms.
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeRollout(t, path, strings.Replace(codexDone1, `"completed_at":1790212287,`, "", 1))
	if _, meta, _, err := lastCodexReplyIn(path, defaultMaxScan); err != nil || !meta.Ended.Equal(time.Unix(1790212287, 0)) {
		t.Errorf("duration end = %v, %v", meta.Ended, err)
	}
	if _, _, _, err := lastCodexReplyIn(filepath.Join(t.TempDir(), "missing.jsonl"), defaultMaxScan); !errors.Is(err, domain.ErrNoReply) {
		t.Errorf("missing rollout err = %v", err)
	}
}

func TestLastReplyCodex(t *testing.T) {
	home := t.TempDir()
	cx1 := filepath.Join(home, "codex-instances", "cx1", "sessions")
	cx3 := filepath.Join(home, "codex-instances", "cx3", "sessions")
	const sid = "01a0cd3e-52db-7452-9af8-2d5470123b1b"
	// The session started two days ago in cx3; later sessions of other ids
	// fill the newer day folders, cx1 has an unrelated one.
	own := filepath.Join(cx3, "2026", "09", "22", "rollout-2026-09-22T15-50-08-"+sid+".jsonl")
	writeRollout(t, own, codexMeta, codexStarted1, codexContext1, codexTokens1b, codexDone1)
	writeRollout(t, filepath.Join(cx3, "2026", "09", "24", "rollout-2026-09-24T08-00-00-other.jsonl"), codexMeta)
	writeRollout(t, filepath.Join(cx1, "2026", "09", "24", "rollout-2026-09-24T08-00-00-other.jsonl"), codexMeta)
	mod := time.Date(2026, 9, 24, 1, 11, 0, 0, time.UTC)
	if err := os.Chtimes(own, mod, mod); err != nil {
		t.Fatal(err)
	}
	now := mod.Add(4 * time.Second)
	r := newReader(func() (string, error) { return home, nil }, nil, func() time.Time { return now },
		Dirs{Codex: []string{"~/codex-instances/*/sessions"}}, nil)
	ctx := context.Background()
	agent := domain.Agent{Key: domain.Key{PaneID: "p6"}, Kind: "codex", Cwd: "/p", SessionID: sid}

	reply, err := r.LastReply(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "## Done\n\n- **one**\n- two" || reply.Source != own || reply.Age != 4*time.Second || !reply.Written.Equal(mod) {
		t.Errorf("reply = %+v", reply)
	}
	// The path is remembered; a removed file is looked up again.
	if cached := r.rollouts[sid]; cached != own {
		t.Errorf("cache = %q", cached)
	}
	moved := filepath.Join(cx1, "2026", "09", "23", filepath.Base(own))
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, moved); err != nil {
		t.Fatal(err)
	}
	if reply, err := r.LastReply(ctx, agent); err != nil || reply.Source != moved {
		t.Errorf("after move: %q, %v", reply.Source, err)
	}

	for name, a := range map[string]domain.Agent{
		"codex session unknown":  {Kind: "codex", Cwd: "/p"},
		"no codex rollout for":   {Kind: "codex", Cwd: "/p", SessionID: "not-there"},
		"codex session unknown ": {Kind: "codex", SessionID: "../escape"},
	} {
		if _, err := r.LastReply(ctx, a); !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), strings.TrimSpace(name)) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCodexRoots(t *testing.T) {
	home := t.TempDir()
	homeFn := func() (string, error) { return home, nil }
	r := newReader(homeFn, nil, time.Now, Dirs{}, nil)
	if got, err := r.CodexRoots(); err != nil || !reflect.DeepEqual(got, []string{filepath.Join(home, ".codex", "sessions")}) {
		t.Fatalf("default roots = %v, %v", got, err)
	}
	r = newReader(homeFn, envOf(map[string]string{"CODEX_HOME": "/cx/two"}), time.Now, Dirs{}, nil)
	want := []string{filepath.Join(home, ".codex", "sessions"), filepath.Join("/cx/two", "sessions")}
	if got, err := r.CodexRoots(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("env roots = %v, %v, want %v", got, err, want)
	}
	// The Claude list does not leak into Codex and the other way round.
	r = newReader(homeFn, nil, time.Now, Dirs{Claude: []string{"/claude"}, Codex: []string{"/codex"}}, nil)
	if got, _ := r.CodexRoots(); !reflect.DeepEqual(got, []string{"/codex"}) {
		t.Errorf("codex roots = %v", got)
	}
	if got, _ := r.ClaudeRoots(); !reflect.DeepEqual(got, []string{"/claude"}) {
		t.Errorf("claude roots = %v", got)
	}
}

func TestSubdirsNewestFirst(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"2025", "2026", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := subdirsNewestFirst(root)
	want := []string{filepath.Join(root, "notes"), filepath.Join(root, "2026"), filepath.Join(root, "2025")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("subdirs = %v, want %v", got, want)
	}
	if subdirsNewestFirst(filepath.Join(root, "missing")) != nil {
		t.Error("missing dir should list nothing")
	}
}
