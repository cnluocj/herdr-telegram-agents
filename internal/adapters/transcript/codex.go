package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Codex writes one rollout file per session under its sessions folder,
// in a folder per day the session started: YYYY/MM/DD/
// rollout-<start time>-<session id>.jsonl. Every turn ends with an
// event_msg "task_complete" that carries the turn's final message
// (last_agent_message) and its start and end, so the reply needs no
// guessing. There are no per-project folders to fall back on: without
// the session id Herdr reports, the caller posts the screen.
const (
	// kindCodex is Codex's Herdr agent kind.
	kindCodex = "codex"
	// DefaultCodexSessionsDir is where Codex keeps its rollout files when
	// nothing else is configured.
	DefaultCodexSessionsDir = "~/.codex/sessions"
	// codexHomeEnv moves Codex's whole home folder, sessions included, to
	// $CODEX_HOME/sessions.
	codexHomeEnv = "CODEX_HOME"
	// rolloutPrefix starts every Codex rollout file name.
	rolloutPrefix = "rollout-"
)

// codexRecord is the slice of a rollout line the reader needs. Codex
// writes many record types (session_meta, response_item, event_msg,
// turn_context, token_usage_record, ...); the payload type tells the
// events apart.
type codexRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type             string  `json:"type"`
		TurnID           string  `json:"turn_id"`
		LastAgentMessage *string `json:"last_agent_message"`
		StartedAt        int64   `json:"started_at"`
		CompletedAt      int64   `json:"completed_at"`
		DurationMS       int64   `json:"duration_ms"`
		Model            string  `json:"model"`
		TurnTokenUsage   struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"turn_token_usage"`
	} `json:"payload"`
}

// CodexRoots returns the Codex sessions folders a lookup searches right
// now: the configured entries, or DefaultCodexSessionsDir plus
// $CODEX_HOME/sessions when none are configured.
func (r *Reader) CodexRoots() ([]string, error) {
	return r.roots(r.dirs.Codex, DefaultCodexSessionsDir, codexHomeEnv, "sessions")
}

// lastCodexReply is LastReply for a Codex pane: the final message of the
// session's last completed turn.
func (r *Reader) lastCodexReply(agent domain.Agent) (domain.Reply, error) {
	if !sessionIDPattern.MatchString(agent.SessionID) {
		return domain.Reply{}, fmt.Errorf("%w: codex session unknown", domain.ErrNoReply)
	}
	roots, err := r.CodexRoots()
	if err != nil {
		return domain.Reply{}, fmt.Errorf("%w: %v", domain.ErrNoReply, err)
	}
	path, modTime, err := r.findRollout(roots, agent.SessionID)
	if err != nil {
		return domain.Reply{}, err
	}
	age := r.now().Sub(modTime)
	r.log.Debug("rollout lookup", slog.String("pane", agent.PaneID), slog.Int("roots", len(roots)),
		slog.String("chosen", path), slog.Int64("age_ms", age.Milliseconds()))
	text, meta, stats, err := lastCodexReplyIn(path, r.maxScan)
	turnDuration, _ := meta.Duration()
	r.log.Debug("rollout scanned",
		slog.String("chosen", filepath.Base(path)), slog.Int("lines", stats.lines),
		slog.Int64("bytes", stats.bytes), slog.Int("skipped_json", stats.skipped), slog.Bool("found", err == nil),
		slog.String("model", meta.Model), slog.Int("output_tokens", meta.OutputTokens), slog.Int64("turn_ms", turnDuration.Milliseconds()))
	if err != nil {
		return domain.Reply{}, err
	}
	return domain.Reply{Text: text, Source: path, Age: age, Written: modTime, Meta: meta}, nil
}

// findRollout returns the rollout file of session id, searching the day
// folders of every root newest first. A path found once is kept for the
// next lookup while the file is still there.
func (r *Reader) findRollout(roots []string, id string) (string, time.Time, error) {
	r.mu.Lock()
	cached := r.rollouts[id]
	r.mu.Unlock()
	if cached != "" {
		if info, err := os.Stat(cached); err == nil && info.Mode().IsRegular() {
			return cached, info.ModTime(), nil
		}
	}
	suffix := "-" + id + transcriptSuffix
	for _, root := range roots {
		path := findRolloutIn(root, suffix)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		r.mu.Lock()
		r.rollouts[id] = path
		r.mu.Unlock()
		return path, info.ModTime(), nil
	}
	return "", time.Time{}, fmt.Errorf("%w: no codex rollout for session %s in %s", domain.ErrNoReply, id, strings.Join(roots, ", "))
}

// findRolloutIn walks root/YYYY/MM/DD, newest folder first, for a rollout
// file whose name ends with suffix.
func findRolloutIn(root, suffix string) string {
	for _, year := range subdirsNewestFirst(root) {
		for _, month := range subdirsNewestFirst(year) {
			for _, day := range subdirsNewestFirst(month) {
				entries, err := os.ReadDir(day)
				if err != nil {
					continue
				}
				for _, e := range entries {
					if !e.IsDir() && strings.HasPrefix(e.Name(), rolloutPrefix) && strings.HasSuffix(e.Name(), suffix) {
						return filepath.Join(day, e.Name())
					}
				}
			}
		}
	}
	return ""
}

// subdirsNewestFirst lists the folders in dir by name, highest first: for
// the zero-padded YYYY, MM and DD folders that is newest first.
func subdirsNewestFirst(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// lastCodexReplyIn walks a rollout backwards and returns the final
// message of the last turn when that turn completed: the first turn event
// met from the end must be its task_complete. A task_started met first is
// a turn still running, a turn_aborted one that was interrupted; neither
// has a reply. After the message the walk goes on to the turn's start for
// the model (turn_context) and the output tokens (the newest
// token_usage_record carries the turn's running total).
func lastCodexReplyIn(path string, budget int64) (string, domain.TurnMeta, scanStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", domain.TurnMeta{}, scanStats{}, fmt.Errorf("%w: open rollout: %v", domain.ErrNoReply, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", domain.TurnMeta{}, scanStats{}, fmt.Errorf("%w: stat rollout: %v", domain.ErrNoReply, err)
	}
	var stats scanStats
	var meta domain.TurnMeta
	var text, turnID string
	var haveText, haveTokens bool
	visit := func(line []byte) error {
		stats.lines++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			return nil
		}
		var rec codexRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			stats.skipped++
			return nil
		}
		p := rec.Payload
		if !haveText {
			if rec.Type != "event_msg" {
				return nil
			}
			switch p.Type {
			case "task_started":
				return fmt.Errorf("%w: codex turn still running", domain.ErrNoReply)
			case "turn_aborted":
				return fmt.Errorf("%w: codex turn aborted", domain.ErrNoReply)
			case "task_complete":
				if p.LastAgentMessage == nil || strings.TrimSpace(*p.LastAgentMessage) == "" {
					return fmt.Errorf("%w: codex turn ended without a message", domain.ErrNoReply)
				}
				text, turnID, haveText = strings.TrimSpace(*p.LastAgentMessage), p.TurnID, true
				meta.Started = unixTime(p.StartedAt)
				meta.Ended = unixTime(p.CompletedAt)
				if meta.Ended.IsZero() && !meta.Started.IsZero() && p.DurationMS > 0 {
					meta.Ended = meta.Started.Add(time.Duration(p.DurationMS) * time.Millisecond)
				}
			}
			return nil
		}
		if turnID != "" && p.TurnID != "" && p.TurnID != turnID {
			return nil // another turn's record
		}
		switch {
		case rec.Type == "turn_context" && meta.Model == "":
			meta.Model = p.Model
		case rec.Type == "token_usage_record" && !haveTokens:
			meta.OutputTokens, haveTokens = p.TurnTokenUsage.OutputTokens, true
		case rec.Type == "event_msg" && p.Type == "task_started":
			return errStop
		}
		return nil
	}
	bytesRead, err := walkBack(f, info.Size(), budget, visit)
	stats.bytes = bytesRead
	switch {
	case haveText:
		// errStop, the file start or the budget: the message is found,
		// the model and tokens are whatever the walk met.
		if err != nil && !errors.Is(err, errStop) {
			stats.readErr = err
		}
		return text, meta, stats, nil
	case err != nil:
		return "", domain.TurnMeta{}, stats, err
	case bytesRead >= budget && info.Size() > budget:
		return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: no completed codex turn within the last %d bytes", domain.ErrNoReply, budget)
	}
	return "", domain.TurnMeta{}, stats, fmt.Errorf("%w: rollout has no completed turn", domain.ErrNoReply)
}

// unixTime converts Codex's Unix seconds; zero stays the zero time.
func unixTime(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
