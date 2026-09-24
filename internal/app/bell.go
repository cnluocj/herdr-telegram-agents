package app

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// ringBodyMax bounds a ring's body in bytes: Bark goes through APNs, whose
// whole payload (title, link and Bark's own fields included) must stay
// under 4 KB. The phone shows the start; the link opens the full post.
const ringBodyMax = 2400

// doneRing is the phone's copy of a done post: the agent's icon and label,
// the summary line, the start of the reply (or the end of the screen) and
// the link to the post. text is already redacted: the cut to size comes
// after the redactor, so a secret cannot survive half-cut at the edge.
func doneRing(agent domain.Agent, icon, text string, reply bool, footer, link string) domain.Ring {
	body := headBytes(plainText(text), ringBodyMax)
	if !reply {
		body = tailBytes(text, ringBodyMax)
	}
	return domain.Ring{Title: ringTitle(icon, agent), Subtitle: footer, Body: body, URL: link, Group: agent.Label()}
}

// questionRing is the phone's copy of a question: the options when the
// dialog was recognised, else the last lines of the screen, as the pager
// shows them. It is urgent. screen and dialog are already redacted.
func questionRing(agent domain.Agent, icon, screen string, dialog domain.Dialog, link string) domain.Ring {
	var b strings.Builder
	if len(dialog.Choices) > 0 {
		for _, c := range dialog.Choices {
			b.WriteString(strconv.Itoa(c.Number) + ". " + c.Label + "\n")
		}
	} else {
		b.WriteString(lastLines(screen, pagerLines))
	}
	return domain.Ring{Title: ringTitle(icon, agent), Body: tailBytes(strings.TrimSpace(b.String()), ringBodyMax),
		URL: link, Group: agent.Label(), Urgent: true}
}

// ringTitle is "<status icon> <label>", with the agent kind added when the
// label does not name it ("🏆 yimall · 1 · claude").
func ringTitle(icon string, agent domain.Agent) string {
	title := agent.Label()
	if agent.Kind != "" && !strings.Contains(title, agent.Kind) {
		title += " · " + agent.Kind
	}
	if icon != "" {
		title = icon + " " + title
	}
	return title
}

var (
	mdFence    = regexp.MustCompile("^\\s*(```|~~~)")
	mdHeading  = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	mdBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+`)
	mdQuote    = regexp.MustCompile(`^\s*>\s?`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)\s]+\)`)
	mdEmphasis = regexp.MustCompile("\\*\\*|__|`")
)

// plainText turns a Markdown reply into what a notification can show:
// fences, heading marks, emphasis and backticks dropped, bullets as "•",
// links as their text, runs of blank lines collapsed.
func plainText(md string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if mdFence.MatchString(line) {
			continue
		}
		line = mdHeading.ReplaceAllString(line, "")
		line = mdQuote.ReplaceAllString(line, "")
		line = mdBullet.ReplaceAllString(line, "${1}• ")
		line = mdLink.ReplaceAllString(line, "$1")
		line = mdEmphasis.ReplaceAllString(line, "")
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// headBytes keeps the start of s within max bytes, cut on a rune and
// marked with "…".
func headBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n") + "…"
}

// tailBytes keeps the end of s within max bytes, cut on a rune and marked
// with "…"; a screen's newest lines are at its end.
func tailBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	start := len(s) - (max - len("…"))
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return "…" + strings.TrimLeft(s[start:], " \n")
}
