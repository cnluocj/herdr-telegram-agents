package domain_test

import (
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestKeyString(t *testing.T) {
	k := domain.Key{PaneID: "p1", TerminalID: "t9"}
	if got := k.String(); got != "p1/t9" {
		t.Fatalf("String() = %q, want %q", got, "p1/t9")
	}
	if got, ok := domain.ParseKey(k.String()); !ok || got != k {
		t.Fatalf("ParseKey(String()) = %v,%v, want %v,true", got, ok, k)
	}
}

func TestSessionTupleDigest(t *testing.T) {
	first := domain.SessionTuple{Source: "claude-code", Agent: "agent-17", Kind: "path", Value: "/private/session/abc"}
	digest := first.Digest()
	if digest == "" || digest != first.Digest() {
		t.Fatalf("Digest() is empty or unstable: %q", digest)
	}
	other := domain.SessionTuple{Source: "claude-code", Agent: "agent-17", Kind: "id", Value: "/private/session/abc"}
	if first.Digest() == other.Digest() {
		t.Fatal("different session tuples produced the same digest")
	}
	ambiguousA := domain.SessionTuple{Source: "a", Agent: "bc", Kind: "d", Value: "ef"}
	ambiguousB := domain.SessionTuple{Source: "ab", Agent: "c", Kind: "d", Value: "ef"}
	if ambiguousA.Digest() == ambiguousB.Digest() {
		t.Fatal("tuple fields were not encoded unambiguously")
	}
	if got := (domain.SessionTuple{Source: "claude-code", Agent: "agent-17", Kind: "path"}).Digest(); got != "" {
		t.Fatalf("incomplete tuple Digest() = %q, want empty", got)
	}
}

func TestSessionKeyIdentity(t *testing.T) {
	digest := (domain.SessionTuple{Source: "codex", Agent: "a1", Kind: "id", Value: "session-one"}).Digest()
	changedTerminal := domain.Key{PaneID: "pane-1", TerminalID: "terminal-new", SessionDigest: digest}
	sameSession := domain.Key{PaneID: "pane-1", TerminalID: "terminal-old", SessionDigest: digest}
	newSession := domain.Key{PaneID: "pane-1", TerminalID: "terminal-old", SessionDigest: strings.Repeat("a", 64)}
	otherPane := domain.Key{PaneID: "pane-2", TerminalID: "terminal-new", SessionDigest: digest}

	if !changedTerminal.SameSession(sameSession) || !changedTerminal.SameIdentity(sameSession) {
		t.Fatal("same session in the same pane should survive a terminal change")
	}
	if !changedTerminal.ConflictsWith(newSession) || changedTerminal.SameIdentity(newSession) {
		t.Fatal("different sessions in the same pane must conflict")
	}
	if changedTerminal.SameSession(otherPane) || changedTerminal.SameIdentity(otherPane) {
		t.Fatal("the same session in different panes must have different identities")
	}
	if changedTerminal.String() == newSession.String() {
		t.Fatal("a new session in the same terminal must have a distinct key")
	}
}

func TestSessionKeyStringDoesNotExposeSessionTuple(t *testing.T) {
	tuple := domain.SessionTuple{Source: "private-source-value", Agent: "private-agent-value", Kind: "path", Value: "/private/session/path"}
	k := domain.Key{PaneID: "pane-1", TerminalID: "terminal-1", SessionDigest: tuple.Digest()}
	got := k.String()
	for _, privateValue := range []string{tuple.Source, tuple.Agent, tuple.Kind, tuple.Value} {
		if strings.Contains(got, privateValue) {
			t.Fatalf("key string exposes session tuple value %q: %q", privateValue, got)
		}
	}
}

func TestAgentLabel(t *testing.T) {
	tests := []struct {
		name  string
		agent domain.Agent
		want  string
	}{
		{
			name:  "workspace label and custom name",
			agent: domain.Agent{Name: "reviewer", Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs", TabID: "w1:t1", TabLabel: "claude"},
			want:  "V3Jobs · reviewer",
		},
		{
			name:  "tab label when no name",
			agent: domain.Agent{Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs", TabLabel: "1"},
			want:  "V3Jobs · 1",
		},
		{
			name:  "kind when no name or tab label",
			agent: domain.Agent{Title: "fix tests", Kind: "claude", WorkspaceID: "w1", WorkspaceLabel: "V3Jobs"},
			want:  "V3Jobs · claude",
		},
		{
			name:  "workspace id when label unknown",
			agent: domain.Agent{Kind: "codex", WorkspaceID: "ws1"},
			want:  "ws1 · codex",
		},
		{
			name:  "title is never used",
			agent: domain.Agent{Title: "Подтверждение", Kind: "claude", WorkspaceID: "wE", WorkspaceLabel: "herdr_tg", TabLabel: "1"},
			want:  "herdr_tg · 1",
		},
		{
			name:  "bare name without workspace",
			agent: domain.Agent{Name: "reviewer", Kind: "claude"},
			want:  "reviewer",
		},
		{
			name:  "bare workspace",
			agent: domain.Agent{WorkspaceLabel: "V3Jobs"},
			want:  "V3Jobs",
		},
		{
			name:  "empty everything",
			agent: domain.Agent{},
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.agent.Label(); got != tt.want {
				t.Fatalf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}
