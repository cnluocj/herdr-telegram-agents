package transcript

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// placeTranscript copies testdata/meta.jsonl to dir/name with mtime mod
// and returns its path.
func placeTranscript(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "meta.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestClaudeRoots(t *testing.T) {
	home := t.TempDir()
	homeFn := func() (string, error) { return home, nil }
	// Default: ~/.claude/projects only.
	r := newReader(homeFn, nil, time.Now, Dirs{}, nil)
	if got, err := r.ClaudeRoots(); err != nil || !reflect.DeepEqual(got, []string{filepath.Join(home, ".claude", "projects")}) {
		t.Fatalf("default roots = %v, %v", got, err)
	}
	// CLAUDE_CONFIG_DIR adds its projects folder to the default.
	r = newReader(homeFn, envOf(map[string]string{"CLAUDE_CONFIG_DIR": "/acct/one"}), time.Now, Dirs{}, nil)
	want := []string{filepath.Join(home, ".claude", "projects"), filepath.Join("/acct/one", "projects")}
	if got, err := r.ClaudeRoots(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("env roots = %v, %v, want %v", got, err, want)
	}
	// Configured entries replace the default: "~", variables and globs
	// expand, a folder that does not exist yet is kept, duplicates go.
	for _, name := range []string{"cc1", "cc2", "cc10"} {
		if err := os.MkdirAll(filepath.Join(home, ".ccs", "instances", name, "projects"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r = newReader(homeFn, envOf(map[string]string{"CLAUDE_CONFIG_DIR": "/ignored", "ACCT": "/acct/two"}), time.Now,
		Dirs{Claude: []string{"~/.claude/projects", " ", "~/.ccs/instances/*/projects", "$ACCT/projects", "${HOME_NONE}/x", "~/.ccs/instances/cc1/projects"}}, nil)
	want = []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".ccs", "instances", "cc1", "projects"),
		filepath.Join(home, ".ccs", "instances", "cc10", "projects"),
		filepath.Join(home, ".ccs", "instances", "cc2", "projects"),
		filepath.Join("/acct/two", "projects"),
		"/x",
	}
	if got, err := r.ClaudeRoots(); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("configured roots = %v, %v, want %v", got, err, want)
	}
	// A glob that matches nothing contributes nothing.
	r = newReader(homeFn, nil, time.Now, Dirs{Claude: []string{"~/nothing/*/projects"}}, nil)
	if got, err := r.ClaudeRoots(); err != nil || len(got) != 0 {
		t.Fatalf("empty glob roots = %v, %v", got, err)
	}
	// "~" needs the home; without one the lookup fails, a plain path not.
	broken := newReader(func() (string, error) { return "", errors.New("no home") }, nil, time.Now, Dirs{Claude: []string{"/abs/projects"}}, nil)
	if got, err := broken.ClaudeRoots(); err != nil || !reflect.DeepEqual(got, []string{"/abs/projects"}) {
		t.Fatalf("absolute roots without home = %v, %v", got, err)
	}
	broken = newReader(func() (string, error) { return "", errors.New("no home") }, nil, time.Now, Dirs{}, nil)
	if _, err := broken.ClaudeRoots(); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("default roots without home err = %v", err)
	}
}

func TestLastReplyAcrossProjectsFolders(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/op/Projects/demo"
	slug := projectSlug(cwd)
	one := filepath.Join(home, "instances", "cc1", "projects")
	two := filepath.Join(home, "instances", "cc2", "projects")
	base := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	// cc1 holds the newest transcript of the project, cc2 the pane's own
	// (older) session, and cc2 also a session started in another project.
	newest := placeTranscript(t, filepath.Join(one, slug), "other-pane.jsonl", base.Add(time.Minute))
	own := placeTranscript(t, filepath.Join(two, slug), "sess-own.jsonl", base)
	moved := placeTranscript(t, filepath.Join(two, "-Users-op-elsewhere"), "sess-moved.jsonl", base)
	now := base.Add(2 * time.Minute)
	r := newReader(func() (string, error) { return home, nil }, nil, func() time.Time { return now },
		Dirs{Claude: []string{filepath.Join(home, "instances", "*", "projects")}}, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	tests := []struct {
		name      string
		agent     domain.Agent
		wantPath  string
		wantError string
	}{
		{"session id beats the newest file", domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "sess-own"}, own, ""},
		{"no session id takes the newest across folders", domain.Agent{Kind: "claude", Cwd: cwd}, newest, ""},
		{"unknown session id falls back to the newest", domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "sess-gone"}, newest, ""},
		{"a path-like session id is ignored", domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "../" + slug + "/sess-own"}, newest, ""},
		{"a session in another project folder is found", domain.Agent{Kind: "claude", Cwd: cwd, SessionID: "sess-moved"}, moved, ""},
		{"a session id works without a working directory", domain.Agent{Kind: "claude", SessionID: "sess-own"}, own, ""},
		{"no working directory and an unknown session", domain.Agent{Kind: "claude", SessionID: "sess-gone"}, "", "agent has no working directory"},
		{"a project no folder knows names every folder", domain.Agent{Kind: "claude", Cwd: "/nowhere"}, "", "no transcript directory " + filepath.Join(one, projectSlug("/nowhere")) + ", " + filepath.Join(two, projectSlug("/nowhere"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply, err := r.LastReply(ctx, tt.agent)
			if tt.wantError != "" {
				if !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("err = %v, want ErrNoReply with %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reply.Source != tt.wantPath || reply.Text != "All done: **three files** touched." {
				t.Errorf("reply from %q (%q), want %q", reply.Source, reply.Text, tt.wantPath)
			}
		})
	}

	// A glob that matches no folder says so.
	none := newReader(func() (string, error) { return home, nil }, nil, time.Now, Dirs{Claude: []string{"~/missing/*/projects"}}, nil)
	if _, err := none.LastReply(ctx, domain.Agent{Kind: "claude", Cwd: cwd}); !errors.Is(err, domain.ErrNoReply) || !strings.Contains(err.Error(), "no projects folder matches") {
		t.Errorf("no folder err = %v", err)
	}
}

func TestSessionIDPattern(t *testing.T) {
	for id, ok := range map[string]bool{
		"8181259a-8c12-49c0-81cf-40ea3a9f3deb": true,
		"01a0cd3e-52db-7452-9af8-2d5470123b1b": true,
		"abc_DEF-1":                            true,
		"":                                     false,
		"../x":                                 false,
		"a/b":                                  false,
		`a\b`:                                  false,
		".hidden":                              false,
		"-leading":                             false,
		"a.b":                                  false,
	} {
		if got := sessionIDPattern.MatchString(id); got != ok {
			t.Errorf("sessionIDPattern(%q) = %v, want %v", id, got, ok)
		}
	}
}
