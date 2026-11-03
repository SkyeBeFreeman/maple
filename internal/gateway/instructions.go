package gateway

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxInstructions is half of Claude Code's 4096 limit, leaving room for
	// other servers' instructions in the same prompt.
	maxInstructions = 2000
	firstLineBudget = 250
)

// ServerInfo is what the instructions text needs to know about a server.
type ServerInfo struct {
	Name, About string
	Tools       []ToolInfo
}

// ToolInfo is an upstream tool as described in the instructions text.
type ToolInfo struct {
	Name, Description string
}

// About picks the one-line purpose of a server: configured description,
// then the upstream serverInfo description, then its instructions' first line.
func About(cfgDescription string, info *mcp.InitializeResult) string {
	if s := strings.TrimSpace(cfgDescription); s != "" {
		return s
	}
	if info == nil {
		return ""
	}
	if info.ServerInfo != nil {
		if s := strings.TrimSpace(info.ServerInfo.Description); s != "" {
			return s
		}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(info.Instructions), "\n")
	return strings.TrimSpace(first)
}

// Instructions renders the initialize instructions text (spec 9.3). All
// lengths are in runes so truncation never splits a UTF-8 sequence.
func Instructions(servers []ServerInfo) string {
	if len(servers) == 0 {
		return ""
	}
	first, listed := firstLine(servers)
	lines := []string{first}
	if listed > 0 {
		remaining := maxInstructions - runes(first) - listed
		share := remaining / listed
		for _, s := range servers[:listed] {
			if len(s.Tools) > 0 {
				lines = append(lines, serverLine(s, share))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func firstLine(servers []ServerInfo) (string, int) {
	const head = "Tools of "
	// Each server's share covers its whole item ("name (about)" plus the
	// separator), so equal shares really do fit side by side.
	share := (firstLineBudget - runes(head) - len(".")) / len(servers)
	items := make([]string, len(servers))
	total := runes(head) + len(".")
	for i, s := range servers {
		items[i] = s.Name
		if about := truncateWords(s.About, share-runes(s.Name)-len(", "+" ()")); about != "" {
			items[i] += " (" + about + ")"
		}
		if i > 0 {
			items[i] = ", " + items[i]
		}
		total += runes(items[i])
	}
	if total <= firstLineBudget {
		return head + strings.Join(items, "") + ".", len(servers)
	}
	var b strings.Builder
	b.WriteString(head)
	listed := 0
	for i, item := range items {
		// Not everything fits, so every prefix must leave room for the tail.
		if runes(b.String())+runes(item)+runes(moreTail(len(items)-i-1))+len(".") > firstLineBudget {
			break
		}
		b.WriteString(item)
		listed++
	}
	if listed == 0 {
		return head + strconv.Itoa(len(servers)) + " servers.", 0
	}
	if rest := len(servers) - listed; rest > 0 {
		b.WriteString(moreTail(rest))
	}
	b.WriteString(".")
	return b.String(), listed
}

func moreTail(n int) string { return ", and " + strconv.Itoa(n) + " more" }

func serverLine(s ServerInfo, share int) string {
	prefix := "- " + s.Name + ": Tools:"
	full, names := make([]string, len(s.Tools)), make([]string, len(s.Tools))
	for i, t := range s.Tools {
		names[i] = oneLine(t.Name)
		full[i] = names[i]
		if d := firstSentence(t.Description); d != "" {
			full[i] += " (" + d + ")"
		}
	}
	for _, items := range [][]string{full, names} {
		if line := prefix + " " + strings.Join(items, ", "); runes(line) <= share {
			return line
		}
	}
	line := prefix
	n := 0
	for i, name := range names {
		sep := " "
		if i > 0 {
			sep = ", "
		}
		cand := line + sep + name
		if runes(cand)+runes(moreTail(len(names)-i-1)) > share {
			break
		}
		line = cand
		n++
	}
	if n == 0 {
		line += " and " + strconv.Itoa(len(names)) + " more"
	} else {
		line += moreTail(len(names) - n)
	}
	// Last resort for a share smaller than the bare prefix.
	return truncateRunes(line, share)
}

// firstSentence returns text up to and including the first sentence end.
func firstSentence(s string) string {
	s = oneLine(s)
	end := len(s)
	for _, sep := range []string{". ", "。"} {
		if i := strings.Index(s, sep); i >= 0 {
			// Keep the terminator but drop the whitespace after a period.
			stop := i + len(sep)
			if sep != "。" {
				stop = i + 1
			}
			end = min(end, stop)
		}
	}
	return strings.TrimSpace(s[:end])
}

// truncateWords cuts s to at most n runes, preferring a word boundary and
// marking the cut with an ellipsis.
func truncateWords(s string, n int) string {
	s = oneLine(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n < 2 {
		return ""
	}
	cut := n - 1
	for i := cut; i > 0; i-- {
		if wordBoundary(r[i-1], r[i]) {
			cut = i
			break
		}
	}
	// No boundary at all (one long word) falls back to a hard cut.
	return strings.TrimSpace(string(r[:cut])) + "…"
}

// wordBoundary reports whether a cut between a and b splits no word. CJK
// text has no spaces, so any cut next to a CJK rune counts.
func wordBoundary(a, b rune) bool {
	return unicode.IsSpace(a) || unicode.IsSpace(b) || isCJK(a) || isCJK(b)
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// oneLine collapses whitespace so upstream text cannot break the line layout.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

func runes(s string) int { return utf8.RuneCountInString(s) }
