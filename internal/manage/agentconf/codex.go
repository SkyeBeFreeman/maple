package agentconf

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

const codexServersKey = "mcp_servers"

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Codex edits ~/.codex/config.toml. No TOML library round-trips formatting,
// so edits are splices of whole [mcp_servers.<name>] blocks located with the
// go-toml parser, and the result is parsed again before it is returned.
var Codex Agent = codexAgent{}

type codexAgent struct{}

func (codexAgent) ID() string                 { return "codex" }
func (codexAgent) Path(p config.Paths) string { return p.CodexTOML() }

// span is the byte range of one [mcp_servers.<name>] table, together with
// the sub-tables that directly follow it: from the start of the header line
// to the end of the line holding its last key/value.
type span struct{ start, end int }

type layout struct {
	spans []span
	// inline reports that the name is (also) defined outside its own table
	// headers, through dotted keys or an inline table.
	inline bool
	// inlineServers reports that mcp_servers itself is an inline table,
	// which a new [mcp_servers.<name>] header may not extend.
	inlineServers bool
}

func (codexAgent) Set(data []byte, e Entry) ([]byte, error) {
	if !config.ValidName(e.Name) {
		return nil, fmt.Errorf("invalid entry name %q", e.Name)
	}
	if e.URL == "" && e.Command == "" {
		return nil, fmt.Errorf("entry %q has neither url nor command", e.Name)
	}
	nl := detectNewline(data)
	block, err := renderCodex(e, nl)
	if err != nil {
		return nil, err
	}
	want, err := entryOf(block, e.Name)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return block, checkCodex(nil, block, e.Name, want)
	}
	doc, servers, err := parseCodex(data)
	if err != nil {
		return nil, err
	}
	_, exists := servers[e.Name]
	l := scanCodex(data, e.Name)
	if err := l.editable(e.Name, exists); err != nil {
		return nil, err
	}

	var out []byte
	if len(l.spans) == 0 {
		if l.inlineServers {
			return nil, fmt.Errorf("%s is an inline table; move it to [%s.<name>] tables to let maple add %q", codexServersKey, codexServersKey, e.Name)
		}
		out = appendBlock(data, block, nl)
	} else {
		out = data
		// Later spans first so earlier offsets stay valid.
		for i := len(l.spans) - 1; i > 0; i-- {
			out = removeSpan(out, l.spans[i])
		}
		first := l.spans[0]
		out = append(append(append([]byte{}, out[:first.start]...), block...), out[first.end:]...)
	}
	return out, checkCodex(doc, out, e.Name, want)
}

// Remove also deletes user entries (import --remove-originals), so any name
// is accepted.
func (codexAgent) Remove(data []byte, name string) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil
	}
	doc, servers, err := parseCodex(data)
	if err != nil {
		return nil, err
	}
	_, exists := servers[name]
	if !exists {
		return data, nil
	}
	l := scanCodex(data, name)
	if err := l.editable(name, exists); err != nil {
		return nil, err
	}
	out := data
	for i := len(l.spans) - 1; i >= 0; i-- {
		out = removeSpan(out, l.spans[i])
	}
	return out, checkCodex(doc, out, name, nil)
}

func (codexAgent) Canonical(data []byte, name string) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false, nil
	}
	_, servers, err := parseCodex(data)
	if err != nil {
		return nil, false, err
	}
	v, ok := servers[name]
	if !ok {
		return nil, false, nil
	}
	// encoding/json sorts map keys, so key order and inline-table versus
	// sub-table spelling do not change the result.
	canon, err := marshal(v)
	if err != nil {
		return nil, false, err
	}
	return canon, true, nil
}

func (codexAgent) List(data []byte) ([]Foreign, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	_, servers, err := parseCodex(data)
	if err != nil {
		return nil, err
	}
	var out []Foreign
	// Decoding into a map loses file order; sorting keeps output stable.
	for _, n := range slices.Sorted(maps.Keys(servers)) {
		v, ok := servers[n].(map[string]any)
		if !ok {
			continue
		}
		f := Foreign{
			Name:    n,
			URL:     tomlString(v["url"]),
			Command: tomlString(v["command"]),
			Headers: tomlStringMap(v["http_headers"]),
			Env:     tomlStringMap(v["env"]),
		}
		if args, ok := v["args"].([]any); ok {
			for _, a := range args {
				f.Args = append(f.Args, tomlString(a))
			}
		}
		if f.URL != "" {
			// Codex speaks streamable HTTP only; it has no SSE transport.
			f.Transport = config.TransportHTTP
		}
		if bearer := tomlString(v["bearer_token_env_var"]); bearer != "" {
			f.Headers = setHeader(f.Headers, "Authorization", "Bearer ${"+bearer+"}")
		}
		for h, envVar := range tomlStringMap(v["env_http_headers"]) {
			f.Headers = setHeader(f.Headers, h, "${"+envVar+"}")
		}
		out = append(out, f)
	}
	return out, nil
}

func setHeader(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}

func tomlString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func tomlStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, x := range m {
		out[k] = tomlString(x)
	}
	return out
}

func (l layout) editable(name string, exists bool) error {
	if l.inline || (exists && len(l.spans) == 0) {
		return fmt.Errorf("%s.%s is defined with dotted keys or an inline table; rewrite it as a [%s.%s] table or edit it by hand",
			codexServersKey, name, codexServersKey, name)
	}
	return nil
}

// parseCodex validates the whole document and returns it with its
// mcp_servers table.
func parseCodex(data []byte) (doc, servers map[string]any, err error) {
	if err := toml.Unmarshal(data, &doc); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, _ := de.Position()
			return nil, nil, fmt.Errorf("line %d: %s", row, de.Error())
		}
		return nil, nil, err
	}
	v, ok := doc[codexServersKey]
	if !ok {
		return doc, nil, nil
	}
	servers, ok = v.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("line %d: %s is not a table", serversLine(data), codexServersKey)
	}
	return doc, servers, nil
}

// serversLine finds the first expression that defines mcp_servers, for
// error messages on input that already parsed successfully.
func serversLine(data []byte) int {
	var p unstable.Parser
	p.Reset(data)
	var table []string
	for p.NextExpression() {
		e := p.Expression()
		keys, off := exprKeys(e)
		full := keys
		if e.Kind == unstable.KeyValue {
			full = append(append([]string{}, table...), keys...)
		} else {
			table = keys
		}
		if len(full) > 0 && full[0] == codexServersKey {
			return bytes.Count(data[:off], []byte("\n")) + 1
		}
	}
	return 1
}

// scanCodex walks the parsed expressions rather than raw lines, so text that
// only looks like a header (inside a multi-line string) is never matched.
func scanCodex(data []byte, name string) layout {
	var l layout
	var p unstable.Parser
	p.Reset(data)
	var table []string
	owned := false
	for p.NextExpression() {
		e := p.Expression()
		keys, off := exprKeys(e)
		switch e.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys
			isOwn := len(keys) >= 2 && keys[0] == codexServersKey && keys[1] == name
			if isOwn && !owned {
				l.spans = append(l.spans, span{start: lineStart(data, off)})
			}
			owned = isOwn
			if owned {
				l.spans[len(l.spans)-1].end = lineEnd(data, off)
			}
		case unstable.KeyValue:
			if owned {
				l.spans[len(l.spans)-1].end = lineEnd(data, int(e.Raw.Offset+e.Raw.Length))
				continue
			}
			full := append(append([]string{}, table...), keys...)
			switch {
			case len(full) >= 2 && full[0] == codexServersKey && full[1] == name:
				l.inline = true
			case len(full) == 1 && full[0] == codexServersKey:
				l.inlineServers = true
			}
		}
	}
	return l
}

// exprKeys returns the decoded key parts of a table header or key/value and
// the offset of the first part.
func exprKeys(e *unstable.Node) ([]string, int) {
	var keys []string
	off := -1
	it := e.Key()
	for it.Next() {
		n := it.Node()
		if off < 0 {
			off = int(n.Raw.Offset)
		}
		keys = append(keys, string(n.Data))
	}
	return keys, off
}

func lineStart(data []byte, off int) int {
	return bytes.LastIndexByte(data[:off], '\n') + 1
}

// lineEnd returns the offset just past the newline ending the line that
// contains off, so trailing comments go with the line.
func lineEnd(data []byte, off int) int {
	if i := bytes.IndexByte(data[off:], '\n'); i >= 0 {
		return off + i + 1
	}
	return len(data)
}

// removeSpan deletes s and the blank lines after it, so the separator
// before s now separates the neighbours. At the end of the file there is no
// next neighbour, so the blank line before s goes instead, undoing
// appendBlock.
func removeSpan(data []byte, s span) []byte {
	end := s.end
	for end < len(data) {
		next := lineEnd(data, end)
		if len(bytes.TrimSpace(data[end:next])) != 0 {
			break
		}
		end = next
	}
	start := s.start
	if end == len(data) && start > 0 {
		if prev := lineStart(data, start-1); len(bytes.TrimSpace(data[prev:start])) == 0 {
			start = prev
		}
	}
	return append(append([]byte{}, data[:start]...), data[end:]...)
}

// appendBlock adds block after the last non-blank line with exactly one
// blank line in between; trailing blank lines are the only bytes it drops.
func appendBlock(data, block []byte, nl string) []byte {
	last := len(bytes.TrimRight(data, " \t\r\n"))
	var out []byte
	if i := bytes.IndexByte(data[last:], '\n'); i >= 0 {
		out = append(out, data[:last+i+1]...)
	} else {
		out = append(append(out, data...), nl...)
	}
	return append(append(out, nl...), block...)
}

// checkCodex re-parses the edited document and compares it with the
// original: a splice that leaked into a neighbouring table would otherwise
// silently change the user's settings. want is the entry Set wrote, nil for
// Remove.
func checkCodex(before map[string]any, out []byte, name string, want any) error {
	var after map[string]any
	if err := toml.Unmarshal(out, &after); err != nil {
		return fmt.Errorf("internal error: edit produced invalid TOML: %w", err)
	}
	got := detachServer(after, name)
	detachServer(before, name)
	// fmt prints maps with sorted keys and NaN as NaN, so unlike
	// reflect.DeepEqual it treats a user's nan value as equal to itself.
	if fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(after) != fmt.Sprint(before) {
		return errors.New("internal error: edit changed more than the target entry")
	}
	return nil
}

// entryOf decodes the entry a rendered block defines.
func entryOf(block []byte, name string) (any, error) {
	var doc map[string]any
	if err := toml.Unmarshal(block, &doc); err != nil {
		return nil, fmt.Errorf("internal error: rendered invalid TOML: %w", err)
	}
	return detachServer(doc, name), nil
}

// detachServer removes mcp_servers.<name> from doc and returns it. An
// emptied mcp_servers is dropped too, since removing the last [mcp_servers.x]
// table also removes the implicit parent.
func detachServer(doc map[string]any, name string) any {
	servers, ok := doc[codexServersKey].(map[string]any)
	if !ok {
		return nil
	}
	v := servers[name]
	delete(servers, name)
	if len(servers) == 0 {
		delete(doc, codexServersKey)
	}
	return v
}

func renderCodex(e Entry, nl string) ([]byte, error) {
	// TOML documents must be valid UTF-8, so invalid input is rejected
	// instead of written.
	for _, v := range entryStrings(e) {
		if !utf8.ValidString(v) {
			return nil, fmt.Errorf("value %q is not valid UTF-8", v)
		}
	}
	var lines []string
	// config.ValidName only admits bare-key characters, so the name needs no quoting.
	lines = append(lines, "["+codexServersKey+"."+e.Name+"]")
	if e.URL != "" {
		lines = append(lines, "url = "+tomlBasicString(e.URL))
		if len(e.Headers) > 0 {
			parts := make([]string, 0, len(e.Headers))
			for _, k := range slices.Sorted(maps.Keys(e.Headers)) {
				parts = append(parts, tomlKey(k)+" = "+tomlBasicString(e.Headers[k]))
			}
			lines = append(lines, "http_headers = { "+strings.Join(parts, ", ")+" }")
		}
	} else {
		lines = append(lines, "command = "+tomlBasicString(e.Command))
		args := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			args = append(args, tomlBasicString(a))
		}
		lines = append(lines, "args = ["+strings.Join(args, ", ")+"]")
	}
	return []byte(strings.Join(lines, nl) + nl), nil
}

// entryStrings lists every string of e that renderCodex writes, in the
// order it writes them.
func entryStrings(e Entry) []string {
	if e.URL != "" {
		out := []string{e.URL}
		for _, k := range slices.Sorted(maps.Keys(e.Headers)) {
			out = append(out, e.Headers[k], k)
		}
		return out
	}
	return append([]string{e.Command}, e.Args...)
}

func tomlKey(k string) string {
	if bareKeyRe.MatchString(k) {
		return k
	}
	return tomlBasicString(k)
}

// tomlBasicString quotes s, which must be valid UTF-8, as a TOML basic
// string.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
