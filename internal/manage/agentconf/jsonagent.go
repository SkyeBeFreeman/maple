package agentconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

const serversKey = "mcpServers"

// Entry names become sjson/gjson path components unescaped, so they must not
// contain path syntax characters such as '.', '*' or '?'.
var entryNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Claude edits ~/.claude.json.
var Claude Agent = jsonAgent{
	id:   "claude",
	path: config.Paths.ClaudeJSON,
	http: func(e Entry) any {
		return struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers,omitempty"`
		}{"http", e.URL, e.Headers}
	},
}

// CodeBuddy edits ~/.codebuddy/mcp.json.
var CodeBuddy Agent = jsonAgent{
	id:   "codebuddy",
	path: config.Paths.CodeBuddyJSON,
	http: func(e Entry) any {
		// transportType matches how CodeBuddy's own UI writes remote entries.
		return struct {
			URL           string            `json:"url"`
			TransportType string            `json:"transportType"`
			Headers       map[string]string `json:"headers,omitempty"`
		}{e.URL, "streamable-http", e.Headers}
	},
}

type jsonAgent struct {
	id   string
	path func(config.Paths) string
	http func(Entry) any
}

func (a jsonAgent) ID() string                 { return a.id }
func (a jsonAgent) Path(p config.Paths) string { return a.path(p) }

func (a jsonAgent) render(e Entry) any {
	if e.URL != "" {
		return a.http(e)
	}
	args := e.Args
	if args == nil {
		args = []string{}
	}
	return struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}{"stdio", e.Command, args}
}

func (a jsonAgent) Set(data []byte, e Entry) ([]byte, error) {
	if !entryNameRe.MatchString(e.Name) {
		return nil, fmt.Errorf("invalid entry name %q", e.Name)
	}
	if e.URL == "" && e.Command == "" {
		return nil, fmt.Errorf("entry %q has neither url nor command", e.Name)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = nil
	} else if err := validate(data); err != nil {
		return nil, err
	}
	unit := detectIndent(data)
	nl := detectNewline(data)
	if data == nil {
		v, err := renderIndented(map[string]any{e.Name: a.render(e)}, unit, 1)
		if err != nil {
			return nil, err
		}
		out := []byte("{" + nl + unit + `"` + serversKey + `": ` + string(fixNewlines(v, nl)) + nl + "}" + nl)
		return out, checkValid(out)
	}

	root := gjson.ParseBytes(data)
	servers := root.Get(serversKey)
	var out []byte
	var err error
	switch {
	case !servers.Exists() || servers.Type == gjson.Null:
		v, rerr := renderIndented(map[string]any{e.Name: a.render(e)}, unit, 1)
		if rerr != nil {
			return nil, rerr
		}
		v = fixNewlines(v, nl)
		if servers.Exists() {
			out, err = sjson.SetRawBytes(data, serversKey, v)
		} else {
			out, err = appendMember(data, root, serversKey, v, unit, nl, 1)
		}
	case servers.Get(e.Name).Exists():
		v, rerr := renderIndented(a.render(e), unit, 2)
		if rerr != nil {
			return nil, rerr
		}
		out, err = sjson.SetRawBytes(data, serversKey+"."+e.Name, fixNewlines(v, nl))
	default:
		v, rerr := renderIndented(a.render(e), unit, 2)
		if rerr != nil {
			return nil, rerr
		}
		out, err = appendMember(data, servers, e.Name, fixNewlines(v, nl), unit, nl, 2)
	}
	if err != nil {
		return nil, err
	}
	return out, checkValid(out)
}

// Remove also deletes user entries (import --remove-originals), so any name
// is accepted and escaped for the path.
func (a jsonAgent) Remove(data []byte, name string) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	path := serversKey + "." + gjson.Escape(name)
	if !gjson.GetBytes(data, path).Exists() {
		return data, nil
	}
	out, err := sjson.DeleteBytes(data, path)
	if err != nil {
		return nil, err
	}
	// sjson leaves the whitespace that preceded '}' behind; collapse an
	// emptied section back to "{}", which is what Set started from.
	if servers := gjson.GetBytes(out, serversKey); servers.IsObject() && servers.Index > 0 &&
		strings.TrimSpace(servers.Raw[1:len(servers.Raw)-1]) == "" && servers.Raw != "{}" {
		out = append(out[:servers.Index:servers.Index], append([]byte("{}"), out[servers.Index+len(servers.Raw):]...)...)
	}
	return out, checkValid(out)
}

func (a jsonAgent) Canonical(data []byte, name string) ([]byte, bool, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, false, nil
	}
	if err := validate(data); err != nil {
		return nil, false, err
	}
	// Look the name up via ForEach rather than a path so any key is safe.
	var raw string
	gjson.GetBytes(data, serversKey).ForEach(func(k, v gjson.Result) bool {
		if k.String() == name {
			raw = v.Raw
			return false
		}
		return true
	})
	if raw == "" {
		return nil, false, nil
	}
	// UseNumber keeps number literals as written, so 1.50 and 1.5 differ
	// just as they would to the user.
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false, err
	}
	// encoding/json sorts map keys, which makes the result order-insensitive.
	canon, err := marshal(v)
	if err != nil {
		return nil, false, err
	}
	return canon, true, nil
}

func (a jsonAgent) List(data []byte) ([]Foreign, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	var out []Foreign
	gjson.GetBytes(data, serversKey).ForEach(func(k, v gjson.Result) bool {
		if !v.IsObject() {
			return true
		}
		f := Foreign{
			Name:    k.String(),
			URL:     v.Get("url").String(),
			Command: v.Get("command").String(),
			Headers: stringMap(v.Get("headers")),
			Env:     stringMap(v.Get("env")),
		}
		for _, x := range v.Get("args").Array() {
			f.Args = append(f.Args, x.String())
		}
		f.Transport = transportOf(v.Get("type").String(), v.Get("transportType").String(), f.URL, f.Command)
		out = append(out, f)
		return true
	})
	return out, nil
}

func transportOf(typ, transportType, url, command string) string {
	for _, t := range []string{typ, transportType} {
		switch strings.ToLower(t) {
		case "stdio":
			return ""
		case "http", "streamablehttp", "streamable-http", "streamable_http":
			return config.TransportHTTP
		case "sse":
			return config.TransportSSE
		}
	}
	switch {
	case url == "" && command != "":
		return ""
	case strings.HasSuffix(strings.TrimRight(url, "/"), "/sse"):
		return config.TransportSSE
	case url != "":
		return config.TransportHTTP
	}
	return ""
}

func stringMap(r gjson.Result) map[string]string {
	if !r.IsObject() {
		return nil
	}
	m := map[string]string{}
	r.ForEach(func(k, v gjson.Result) bool {
		m[k.String()] = v.String()
		return true
	})
	return m
}

// validate uses encoding/json because gjson accepts malformed input silently.
func validate(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return fmt.Errorf("line %d: %s", lineAt(data, se.Offset), se.Error())
		}
		var ue *json.UnmarshalTypeError
		if errors.As(err, &ue) {
			return fmt.Errorf("line %d: %s", lineAt(data, ue.Offset), ue.Error())
		}
		return err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New("line 1: top-level value is not a JSON object")
	}
	if s, exists := obj[serversKey]; exists && s != nil {
		if _, ok := s.(map[string]any); !ok {
			r := gjson.GetBytes(data, serversKey)
			return fmt.Errorf("line %d: %s is not a JSON object", lineAt(data, int64(r.Index)), serversKey)
		}
	}
	return nil
}

func checkValid(out []byte) error {
	if !json.Valid(out) {
		return errors.New("internal error: edit produced invalid JSON")
	}
	return nil
}

func lineAt(data []byte, offset int64) int {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	// Offsets point just past the offending byte; skipping back over trailing
	// whitespace keeps "unexpected end of input" on the last real line.
	for offset > 0 && data[offset-1] <= ' ' {
		offset--
	}
	return bytes.Count(data[:offset], []byte("\n")) + 1
}

func detectNewline(data []byte) string {
	if i := bytes.IndexByte(data, '\n'); i > 0 && data[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

func fixNewlines(v []byte, nl string) []byte {
	if nl == "\n" {
		return v
	}
	return bytes.ReplaceAll(v, []byte("\n"), []byte(nl))
}

// detectIndent takes the leading whitespace of the first indented line as the
// file's indent unit, so inserted entries look hand-written.
func detectIndent(data []byte) string {
	for _, line := range bytes.Split(data, []byte("\n"))[1:] {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || len(trimmed) == len(line) {
			continue
		}
		return string(line[:len(line)-len(trimmed)])
	}
	return "  "
}

// marshal avoids encoding/json's default HTML escaping so URLs with '&'
// stay readable in the user's file.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// renderIndented renders v for a value position at the given nesting depth.
func renderIndented(v any, unit string, depth int) ([]byte, error) {
	compact, err := marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, strings.Repeat(unit, depth), unit); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// appendMember inserts `"key": value` as the last member of obj, a parsed
// object inside data. sjson would append it inline without newlines, so the
// splice is done by hand to keep the file's layout.
func appendMember(data []byte, obj gjson.Result, key string, value []byte, unit, nl string, depth int) ([]byte, error) {
	// gjson includes trailing whitespace in the root's Raw.
	raw := strings.TrimRight(obj.Raw, " \t\r\n")
	start := obj.Index
	if start < 0 || start+len(raw) > len(data) || string(data[start:start+len(raw)]) != raw {
		return nil, errors.New("internal error: cannot locate object in input")
	}
	closeIdx := start + len(raw) - 1
	member := "\"" + key + "\": " + string(value)
	inner := strings.TrimSpace(raw[1 : len(raw)-1])

	var b bytes.Buffer
	if inner == "" {
		b.Write(data[:start+1])
		b.WriteString(nl + strings.Repeat(unit, depth) + member + nl + strings.Repeat(unit, depth-1))
		b.Write(data[closeIdx:])
		return b.Bytes(), nil
	}
	// Insert right after the last member so the existing whitespace before
	// '}' stays, and Remove (which strips ",<ws>member") restores the input.
	last := closeIdx - 1
	for last > start && data[last] <= ' ' {
		last--
	}
	b.Write(data[:last+1])
	b.WriteString("," + nl + strings.Repeat(unit, depth) + member)
	b.Write(data[last+1:])
	return b.Bytes(), nil
}
