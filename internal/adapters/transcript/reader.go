// Package transcript reads an agent's last reply from the session
// transcript the agent writes for itself, outside Herdr: Claude Code's
// per-project transcripts here, Codex's rollout files in codex.go.
//
// Claude Code keeps one directory per project under its projects folder
// (~/.claude/projects, or $CLAUDE_CONFIG_DIR/projects when that variable
// moves its config) and one .jsonl per session, named by the session id. When Herdr reports the
// pane's session id the reader opens that exact file in any configured
// projects folder. Without one (Herdr 0.7.5 does not tell which session a
// pane runs) it takes the newest transcript of the pane's working
// directory, and two Claude panes in the same directory cannot be told
// apart; that limitation is documented and the caller falls back to the
// screen whenever the reader is unsure.
package transcript

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// kindClaude is Claude Code's Herdr agent kind.
	kindClaude = "claude"
	// transcriptSuffix is the extension of Claude Code session files.
	transcriptSuffix = ".jsonl"
	// defaultMaxScan bounds how many bytes are read from the end of a
	// transcript before giving up: a turn with huge tool results in it
	// costs one screen post, not a multi-megabyte parse.
	defaultMaxScan = 4 << 20
	// DefaultProjectsDir is Claude Code's transcript root when nothing
	// else is configured.
	DefaultProjectsDir = "~/.claude/projects"
	// configDirEnv moves Claude Code's whole config folder, transcripts
	// included, to $CLAUDE_CONFIG_DIR/projects.
	configDirEnv = "CLAUDE_CONFIG_DIR"
)

// errNoDir marks a lookup whose project folder does not exist in any
// projects folder; its text is part of the reason the caller logs.
var errNoDir = errors.New("no transcript directory")

// sessionIDPattern is what a session id may look like before it is used
// as a file name: Claude Code writes UUIDs, and nothing that could leave
// the project folder ("/", "\", "..") is accepted.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Dirs says where the agents keep their session files. Entries may start
// with "~", name environment variables and use glob patterns; an empty
// list means that agent's default.
type Dirs struct {
	// Claude lists Claude Code's projects folders (see
	// domain.Config.ClaudeProjectsDirs).
	Claude []string
	// Codex lists Codex's sessions folders (see
	// domain.Config.CodexSessionsDirs).
	Codex []string
	// ClaudeWindow is the Claude Code context size (see
	// domain.Config.ClaudeContextWindow); zero leaves it unknown.
	ClaudeWindow int
}

// Reader implements domain.ReplySource for Claude Code and Codex.
type Reader struct {
	home    func() (string, error)
	getenv  func(string) string
	now     func() time.Time
	log     *slog.Logger
	maxScan int64
	// dirs are the folders as configured; they are expanded on every
	// lookup, so a folder created later (a new account) is found without
	// a restart.
	dirs Dirs
	// mu guards rollouts, the Codex rollout path found per session id.
	mu       sync.Mutex
	rollouts map[string]string
}

// NewReader returns a reader over the given folders; an empty list means
// the default of that agent. getenv reads the daemon's environment for
// CLAUDE_CONFIG_DIR, CODEX_HOME and the variables the folders name.
func NewReader(dirs Dirs, getenv func(string) string, log *slog.Logger) *Reader {
	return newReader(os.UserHomeDir, getenv, time.Now, dirs, log)
}

// newReader takes the home, environment and clock sources so tests can
// point the reader at a temporary directory.
func newReader(home func() (string, error), getenv func(string) string, now func() time.Time, dirs Dirs, log *slog.Logger) *Reader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	dirs = Dirs{Claude: append([]string(nil), dirs.Claude...), Codex: append([]string(nil), dirs.Codex...), ClaudeWindow: dirs.ClaudeWindow}
	return &Reader{home: home, getenv: getenv, now: now, log: log, maxScan: defaultMaxScan, dirs: dirs, rollouts: map[string]string{}}
}

// ClaudeRoots returns the Claude Code projects folders a lookup searches
// right now: the configured entries, or DefaultProjectsDir plus
// $CLAUDE_CONFIG_DIR/projects when none are configured.
func (r *Reader) ClaudeRoots() ([]string, error) {
	return r.roots(r.dirs.Claude, DefaultProjectsDir, configDirEnv, "projects")
}

// roots expands folder patterns: the configured ones, or the default plus
// $env/sub when none are configured, with "~" and environment variables
// expanded and glob patterns replaced by their matches, in order and
// without duplicates. A folder that does not exist is kept: the lookup
// names it when nothing is found.
func (r *Reader) roots(patterns []string, def, env, sub string) ([]string, error) {
	if len(patterns) == 0 {
		patterns = []string{def}
		if dir := strings.TrimSpace(r.getenv(env)); dir != "" {
			patterns = append(patterns, filepath.Join(dir, sub))
		}
	}
	seen := map[string]bool{}
	var roots []string
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		expanded, err := expandPath(pattern, r.home, r.getenv)
		if err != nil {
			return nil, err
		}
		matches := []string{expanded}
		if strings.ContainsAny(expanded, "*?[") {
			if matches, err = filepath.Glob(expanded); err != nil {
				return nil, fmt.Errorf("folder pattern %q: %v", pattern, err)
			}
		}
		for _, m := range matches {
			m = filepath.Clean(m)
			if !seen[m] {
				seen[m] = true
				roots = append(roots, m)
			}
		}
	}
	return roots, nil
}

// expandPath replaces environment variables ($VAR, ${VAR}) and a leading
// "~" with their values; the home is looked up only when needed.
func expandPath(p string, home func() (string, error), getenv func(string) string) (string, error) {
	p = os.Expand(p, getenv)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("home directory: %v", err)
		}
		p = filepath.Join(h, p[1:])
	}
	return p, nil
}

// LastReply finds the agent's transcript and returns the last text the
// agent wrote after the operator's last prompt. Every failure is
// domain.ErrNoReply wrapped with the reason; the reader logs the lookup at
// debug and leaves the one info line to the caller.
func (r *Reader) LastReply(ctx context.Context, agent domain.Agent) (domain.Reply, error) {
	if err := ctx.Err(); err != nil {
		return domain.Reply{}, err
	}
	switch agent.Kind {
	case kindClaude:
		return r.lastClaudeReply(agent)
	case kindCodex:
		return r.lastCodexReply(agent)
	}
	return domain.Reply{}, fmt.Errorf("%w: unsupported agent %q", domain.ErrNoReply, agent.Kind)
}

// lastClaudeReply is LastReply for a Claude Code pane.
func (r *Reader) lastClaudeReply(agent domain.Agent) (domain.Reply, error) {
	if strings.TrimSpace(agent.Cwd) == "" && agent.SessionID == "" {
		return domain.Reply{}, fmt.Errorf("%w: agent has no working directory", domain.ErrNoReply)
	}
	roots, err := r.ClaudeRoots()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: %v", domain.ErrNoReply, err)
	}
	path, modTime, candidates, how, err := r.locate(agent, roots)
	if err != nil {
		return domain.Reply{}, err
	}
	age := r.now().Sub(modTime)
	r.log.Debug("transcript lookup",
		slog.String("pane", agent.PaneID), slog.String("cwd", agent.Cwd), slog.Int("roots", len(roots)), slog.String("by", how),
		slog.Int("candidates", candidates), slog.String("chosen", path), slog.Int64("age_ms", age.Milliseconds()))
	text, turn, stats, err := lastReplyIn(path, r.maxScan)
	meta := turn.meta()
	// A context larger than the configured window means the window is
	// wrong (a 1M session with 200000 set): the size stands alone then.
	if w := r.dirs.ClaudeWindow; w > 0 && meta.ContextTokens > 0 && meta.ContextTokens <= w {
		meta.ContextWindow = w
	}
	turnDuration, _ := meta.Duration()
	r.log.Debug("transcript scanned",
		slog.String("chosen", filepath.Base(path)), slog.Int("lines", stats.lines),
		slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped), slog.Bool("found", err == nil),
		slog.String("model", meta.Model), slog.Int("output_tokens", meta.OutputTokens), slog.Int("files", len(meta.Files)),
		slog.Int64("turn_ms", turnDuration.Milliseconds()), slog.Bool("complete", turn.complete))
	if err != nil {
		return domain.Reply{}, err
	}
	if stats.readErr != nil {
		r.log.Debug("[FIX] transcript read failed after the reply was found, stats are partial",
			slog.String("chosen", filepath.Base(path)), slog.Int64("bytes", stats.bytes), slog.String("err", stats.readErr.Error()))
	}
	return domain.Reply{Text: text, Source: path, Age: age, Written: modTime, Meta: meta}, nil
}

// locate picks the transcript to read and says how ("session" or
// "newest"): the file named after the agent's session id when Herdr
// reported one and a projects folder holds it, else the newest transcript
// of the working directory across every projects folder.
func (r *Reader) locate(agent domain.Agent, roots []string) (path string, modTime time.Time, candidates int, how string, err error) {
	if len(roots) == 0 {
		return "", time.Time{}, 0, "", fmt.Errorf("%w: %w: no projects folder matches %q", domain.ErrNoReply, errNoDir, r.dirs.Claude)
	}
	slug := ""
	if cwd := strings.TrimSpace(agent.Cwd); cwd != "" {
		slug = projectSlug(cwd)
	}
	if sessionIDPattern.MatchString(agent.SessionID) {
		if path, modTime, candidates = findSession(roots, slug, agent.SessionID+transcriptSuffix); path != "" {
			return path, modTime, candidates, "session", nil
		}
		r.log.Debug("session transcript not found, newest in project used",
			slog.String("pane", agent.PaneID), slog.String("session", agent.SessionID), slog.Any("roots", roots))
	}
	if slug == "" {
		return "", time.Time{}, 0, "", fmt.Errorf("%w: agent has no working directory", domain.ErrNoReply)
	}
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		dirs = append(dirs, filepath.Join(root, slug))
	}
	path, modTime, candidates, err = newestTranscriptIn(dirs)
	return path, modTime, candidates, "newest", err
}

// findSession looks for the session file name in the project folder of
// every root, then in every other project folder of every root (a session
// started in another directory than the one Herdr reports now). The
// newest match wins; n counts the matches of the pass that found it.
func findSession(roots []string, slug, name string) (path string, modTime time.Time, n int) {
	pick := func(p string) {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return
		}
		n++
		if path == "" || info.ModTime().After(modTime) {
			path, modTime = p, info.ModTime()
		}
	}
	if slug != "" {
		for _, root := range roots {
			pick(filepath.Join(root, slug, name))
		}
		if path != "" {
			return path, modTime, n
		}
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != slug {
				pick(filepath.Join(root, e.Name(), name))
			}
		}
	}
	return path, modTime, n
}

// newestTranscriptIn returns the most recently modified session file
// across dirs and how many candidates there were. A dir that does not
// exist is skipped; when none exists the error names them all.
func newestTranscriptIn(dirs []string) (path string, modTime time.Time, candidates int, err error) {
	var missing []string
	var firstErr error
	for _, dir := range dirs {
		p, m, n, err := newestTranscript(dir)
		if err != nil {
			if errors.Is(err, errNoDir) {
				missing = append(missing, dir)
			} else if firstErr == nil {
				firstErr = err
			}
			continue
		}
		candidates += n
		if path == "" || m.After(modTime) {
			path, modTime = p, m
		}
	}
	switch {
	case path != "":
		return path, modTime, candidates, nil
	case firstErr != nil:
		return "", time.Time{}, 0, firstErr
	}
	return "", time.Time{}, 0, fmt.Errorf("%w: %w %s", domain.ErrNoReply, errNoDir, strings.Join(missing, ", "))
}

// newestTranscript returns the most recently modified session file in dir
// and how many candidates there were.
func newestTranscript(dir string) (path string, modTime time.Time, candidates int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", time.Time{}, 0, fmt.Errorf("%w: %w %s", domain.ErrNoReply, errNoDir, dir)
		}
		return "", time.Time{}, 0, fmt.Errorf("%w: read %s: %v", domain.ErrNoReply, dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), transcriptSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates++
		if path == "" || info.ModTime().After(modTime) {
			path, modTime = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if path == "" {
		return "", time.Time{}, 0, fmt.Errorf("%w: no transcript files in %s", domain.ErrNoReply, dir)
	}
	return path, modTime, candidates, nil
}
