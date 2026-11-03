package agentconf

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

func decodeTOML(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := toml.Unmarshal(data, &m); err != nil {
		t.Fatalf("output is not valid TOML: %v\n%s", err, data)
	}
	return m
}

// withoutServer decodes data and drops mcp_servers.<name>, so two documents
// can be compared on everything maple must not touch.
func withoutServer(t *testing.T, data []byte, name string) map[string]any {
	t.Helper()
	m := decodeTOML(t, data)
	if servers, ok := m["mcp_servers"].(map[string]any); ok {
		delete(servers, name)
		if len(servers) == 0 {
			delete(m, "mcp_servers")
		}
	}
	return m
}

func serverOf(t *testing.T, data []byte, name string) any {
	t.Helper()
	servers, _ := decodeTOML(t, data)["mcp_servers"].(map[string]any)
	return servers[name]
}

func mustSetTOML(t *testing.T, data []byte, e Entry) []byte {
	t.Helper()
	out, err := Codex.Set(data, e)
	if err != nil {
		t.Fatalf("Codex Set(%s): %v", e.Name, err)
	}
	decodeTOML(t, out)
	return out
}

func mustRemoveTOML(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	out, err := Codex.Remove(data, name)
	if err != nil {
		t.Fatalf("Codex Remove(%s): %v", name, err)
	}
	decodeTOML(t, out)
	return out
}

func TestCodexSetPreservesRest(t *testing.T) {
	input := readTestdata(t, "codex", "input.toml")
	for _, tc := range []struct {
		golden string
		entry  Entry
	}{
		{"set-http.golden.toml", mapleHTTP},
		{"set-stdio.golden.toml", notesStdio},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			out := mustSetTOML(t, input, tc.entry)
			assertBytes(t, "first Set", out, readTestdata(t, "codex", tc.golden))
			again := mustSetTOML(t, out, tc.entry)
			assertBytes(t, "second Set (idempotence)", again, out)
			assertBytes(t, "Remove after Set", mustRemoveTOML(t, out, tc.entry.Name), input)
		})
	}
}

func TestCodexReplaceAndRemove(t *testing.T) {
	in := readTestdata(t, "codex", "with-maple.toml")
	assertBytes(t, "replace", mustSetTOML(t, in, mapleHTTP), readTestdata(t, "codex", "replace.golden.toml"))
	assertBytes(t, "remove", mustRemoveTOML(t, in, "maple"), readTestdata(t, "codex", "remove.golden.toml"))

	golden := readTestdata(t, "codex", "set-http.golden.toml")
	rotated := mapleHTTP
	rotated.Headers = map[string]string{"Authorization": "Bearer rotated"}
	want := strings.Replace(string(golden), "Bearer test-token", "Bearer rotated", 1)
	assertBytes(t, "rotate token", mustSetTOML(t, golden, rotated), []byte(want))

	// import --remove-originals deletes user entries, including their
	// sub-tables, and names that are not valid maple names.
	input := readTestdata(t, "codex", "input.toml")
	wantNoRepl := "# Codex settings (sample)\nmodel = \"example-model\"\napproval_policy = \"on-request\"\n\n# profile for experiments\n[profiles.x]\nmodel = \"other-model\"\n"
	assertBytes(t, "remove user entry", mustRemoveTOML(t, input, "node_repl"), []byte(wantNoRepl))

	dotted := []byte("[mcp_servers.\"a.b\"]\nurl = \"u\"\n\n[mcp_servers.a]\nb = 1\n")
	assertBytes(t, "remove dotted name", mustRemoveTOML(t, dotted, "a.b"), []byte("[mcp_servers.a]\nb = 1\n"))

	for _, s := range []string{"", "\n", "a = 1\n", "[mcp_servers.x]\nurl = \"u\"\n", "[mcp_servers]\n"} {
		assertBytes(t, "remove absent from "+s, mustRemoveTOML(t, []byte(s), "maple"), []byte(s))
	}
}

func TestCodexHeaderForms(t *testing.T) {
	// Every spelling of the same table header must be recognised, otherwise
	// Set would append a duplicate table and break the file.
	for _, header := range []string{
		`[mcp_servers.maple]`,
		`[mcp_servers."maple"]`,
		`[mcp_servers.'maple']`,
		`  [ mcp_servers . "maple" ]   # gateway`,
		"[mcp_servers.maple]\t# tab before comment",
	} {
		in := []byte("a = 1\n\n" + header + "\nurl = \"old\"\n\n[profiles.x]\nb = 2\n")
		want := "a = 1\n\n[mcp_servers.maple]\nurl = \"http://127.0.0.1:47321/mcp\"\nhttp_headers = { Authorization = \"Bearer test-token\" }\n\n[profiles.x]\nb = 2\n"
		assertBytes(t, "Set over "+header, mustSetTOML(t, in, mapleHTTP), []byte(want))
		assertBytes(t, "Remove "+header, mustRemoveTOML(t, in, "maple"), []byte("a = 1\n\n[profiles.x]\nb = 2\n"))
	}
}

func TestCodexEdgeLayouts(t *testing.T) {
	block := "[mcp_servers.maple]\nurl = \"http://127.0.0.1:47321/mcp\"\nhttp_headers = { Authorization = \"Bearer test-token\" }\n"
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	cases := []struct{ name, in, want string }{
		{"empty file", "", block},
		{"blank file", "\n\n", block},
		{"no trailing newline", "a = 1", "a = 1\n\n" + block},
		{"comment without newline", "# only", "# only\n\n" + block},
		{"one trailing newline", "a = 1\n", "a = 1\n\n" + block},
		{"blank line already", "a = 1\n\n", "a = 1\n\n" + block},
		{"extra blank lines", "a = 1\n\n \n\t\n", "a = 1\n\n" + block},
		{"crlf", crlf("# c\n[x]\na = 1\n"), crlf("# c\n[x]\na = 1\n\n" + block)},
		{"crlf replace", crlf("[mcp_servers.maple]\nurl = \"old\"\n\n[x]\na = 1\n"), crlf(block + "\n[x]\na = 1\n")},
		{
			name: "maple with env sub-table in the middle",
			in:   "[mcp_servers.maple]\nurl = \"old\"\n\n[mcp_servers.maple.env]\nA = \"1\"\n\n[x]\na = 1\n",
			want: block + "\n[x]\na = 1\n",
		},
		{
			// The sub-table directly follows its parent, so both must be one
			// block; replacing them separately would eat the blank line
			// before [x].
			name: "maple with adjacent env sub-table",
			in:   "[mcp_servers.maple]\nurl = \"old\"\n[mcp_servers.maple.env]\nA = \"1\"\n\n[x]\na = 1\n",
			want: block + "\n[x]\na = 1\n",
		},
		{
			name: "maple last without trailing newline",
			in:   "[x]\na = 1\n\n[mcp_servers.maple]\nurl = \"old\"",
			want: "[x]\na = 1\n\n" + block,
		},
		{
			// A header-looking line inside a multi-line string is data, not a
			// table; treating it as one would corrupt the string.
			name: "header inside multi-line string",
			in:   "notes = \"\"\"\n[mcp_servers.maple]\nurl = \"fake\"\n\"\"\"\n",
			want: "notes = \"\"\"\n[mcp_servers.maple]\nurl = \"fake\"\n\"\"\"\n\n" + block,
		},
		{
			name: "array of arrays spanning lines",
			in:   "[mcp_servers.maple]\nargs = [\n[\"a\"],\n]\n\n[x]\na = 1\n",
			want: block + "\n[x]\na = 1\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustSetTOML(t, []byte(tc.in), mapleHTTP)
			assertBytes(t, tc.name, out, []byte(tc.want))
			if !reflect.DeepEqual(withoutServer(t, out, "maple"), withoutServer(t, []byte(tc.in), "maple")) {
				t.Errorf("non-maple content changed:\n%s", out)
			}
			wantServer := map[string]any{
				"url":          mapleHTTP.URL,
				"http_headers": map[string]any{"Authorization": "Bearer test-token"},
			}
			if got := serverOf(t, out, "maple"); !reflect.DeepEqual(got, wantServer) {
				t.Errorf("maple entry = %#v, want %#v", got, wantServer)
			}
			if strings.Contains(tc.in, "\r\n") && strings.Count(string(out), "\n") != strings.Count(string(out), "\r\n") {
				t.Errorf("output mixes line endings: %q", out)
			}
		})
	}
}

func TestCodexEscaping(t *testing.T) {
	e := Entry{
		Name:    "esc",
		Command: `/Users/a b/"q"/maple`,
		Args:    []string{"run", `x\y`, "tab\there", "é", "ctl\x01\x7f", "nl\r\n"},
	}
	out := mustSetTOML(t, readTestdata(t, "codex", "input.toml"), e)
	want := map[string]any{"command": e.Command, "args": []any{"run", `x\y`, "tab\there", "é", "ctl\x01\x7f", "nl\r\n"}}
	if got := serverOf(t, out, "esc"); !reflect.DeepEqual(got, want) {
		t.Errorf("entry = %#v, want %#v\n%s", got, want, out)
	}
	if !strings.Contains(string(out), `command = "/Users/a b/\"q\"/maple"`) {
		t.Errorf("command not written as a basic string:\n%s", out)
	}

	h := Entry{Name: "maple", URL: "http://x/mcp?a=1&b=2", Headers: map[string]string{"X Custom": `v"1`, "B-Key": "2", "A_Key": "1"}}
	out = mustSetTOML(t, nil, h)
	wantText := "[mcp_servers.maple]\nurl = \"http://x/mcp?a=1&b=2\"\nhttp_headers = { A_Key = \"1\", B-Key = \"2\", \"X Custom\" = \"v\\\"1\" }\n"
	assertBytes(t, "headers", out, []byte(wantText))

	if _, err := Codex.Set(nil, Entry{Name: "bad", Command: "\xff"}); err == nil {
		t.Error("Set with invalid UTF-8 succeeded, want error")
	}
}

func TestCodexSetRejectsBadEntry(t *testing.T) {
	for _, e := range []Entry{{Name: "", URL: "u"}, {Name: "x"}, {Name: "a.b", URL: "u"}, {Name: "a b", URL: "u"}} {
		if _, err := Codex.Set(nil, e); err == nil {
			t.Errorf("Set(%+v) succeeded, want error", e)
		}
	}
}

func TestCodexRejectsInlineDefinition(t *testing.T) {
	// Entries defined through dotted keys or inline tables are the user's own
	// syntax; rewriting them line by line could break the file, so editing
	// is refused while reading still works.
	for _, tc := range []struct {
		in string
		// An inline mcp_servers table cannot be extended by a new
		// [mcp_servers.<name>] table, so no entry can be added there.
		otherEditable bool
	}{
		{"[mcp_servers]\nmaple = { url = \"u\" }\n", true},
		{"[mcp_servers]\nmaple.url = \"u\"\n", true},
		{"mcp_servers.maple.url = \"u\"\n", true},
		{"mcp_servers = { maple = { url = \"u\" } }\n", false},
		{"[mcp_servers]\nmaple.url = \"u\"\n\n[mcp_servers.maple.env]\nA = \"1\"\n", true},
	} {
		in := tc.in
		const wantErr = "dotted keys or an inline table"
		if out, err := Codex.Set([]byte(in), mapleHTTP); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Set on %q = %v; want an error mentioning %q\n%s", in, err, wantErr, out)
		}
		if out, err := Codex.Remove([]byte(in), "maple"); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Remove on %q = %v; want an error mentioning %q\n%s", in, err, wantErr, out)
		}
		if _, ok, err := Codex.Canonical([]byte(in), "maple"); !ok || err != nil {
			t.Errorf("Canonical on %q = %v, %v; want true, nil", in, ok, err)
		}
		out, err := Codex.Set([]byte(in), notesStdio)
		switch {
		case tc.otherEditable && err != nil:
			t.Errorf("Set(notes) on %q: %v", in, err)
		case tc.otherEditable:
			if !strings.HasPrefix(string(out), in) {
				t.Errorf("Set(notes) on %q changed existing bytes:\n%s", in, out)
			}
		case err == nil || !strings.Contains(err.Error(), "inline table"):
			t.Errorf("Set(notes) on %q error = %v; want an inline table error", in, err)
		}
	}
}

func TestCodexCanonicalAndList(t *testing.T) {
	forms := []string{
		"[mcp_servers.maple]\nurl = \"u\"\nhttp_headers = { B = \"2\", A = \"1\" }\nn = 3\n",
		"[mcp_servers.maple]\nn = 3\nurl = \"u\"\n\n[mcp_servers.maple.http_headers]\nB = \"2\"\nA = \"1\"\n",
		"[mcp_servers]\nmaple = { http_headers = { A = \"1\", B = \"2\" }, url = \"u\", n = 3 }\n",
	}
	want := `{"http_headers":{"A":"1","B":"2"},"n":3,"url":"u"}`
	for i, f := range forms {
		got, ok, err := Codex.Canonical([]byte(f), "maple")
		if err != nil || !ok {
			t.Fatalf("Canonical(form %d) = %v, %v", i, ok, err)
		}
		assertBytes(t, fmt.Sprintf("form %d", i), got, []byte(want))
	}
	for _, in := range []string{"", "a = 1\n", "[mcp_servers.x]\nurl = \"u\"\n"} {
		if _, ok, err := Codex.Canonical([]byte(in), "maple"); ok || err != nil {
			t.Errorf("Canonical(%q) = %v, %v; want false, nil", in, ok, err)
		}
	}

	x, _, err := Codex.Canonical(mustSetTOML(t, readTestdata(t, "codex", "input.toml"), mapleHTTP), "maple")
	if err != nil {
		t.Fatal(err)
	}
	y, _, err := Codex.Canonical(mustSetTOML(t, nil, mapleHTTP), "maple")
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, "written entry", x, y)

	got, err := Codex.List(readTestdata(t, "codex", "list.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantList := []Foreign{
		{Name: "node_repl", Command: "node", Args: []string{"repl.js"}, Env: map[string]string{"NODE_ENV": "development"}},
		{Name: "remote-a", URL: "https://mcp.example.com/a/mcp", Transport: config.TransportHTTP, Headers: map[string]string{"X-Client": "sample", "Authorization": "Bearer ${A_TOKEN}"}},
		{Name: "remote-b", URL: "https://mcp.example.com/b/mcp", Transport: config.TransportHTTP, Headers: map[string]string{"X-Api-Key": "${B_KEY}"}},
	}
	if !reflect.DeepEqual(got, wantList) {
		t.Errorf("Codex.List =\n%#v\nwant\n%#v", got, wantList)
	}

	got, err = Codex.List(readTestdata(t, "codex", "input.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantInput := []Foreign{{Name: "node_repl", Command: "node", Args: []string{"repl.js", "--port", "0"}, Env: map[string]string{"NODE_ENV": "development"}}}
	if !reflect.DeepEqual(got, wantInput) {
		t.Errorf("Codex.List(input) =\n%#v\nwant\n%#v", got, wantInput)
	}

	for _, in := range []string{"", "a = 1\n", "[mcp_servers]\n", "[mcp_servers]\nx = 1\n"} {
		got, err := Codex.List([]byte(in))
		if err != nil || len(got) != 0 {
			t.Errorf("List(%q) = %v, %v; want empty", in, got, err)
		}
	}
}

func TestCodexParseErrorLine(t *testing.T) {
	cases := []struct {
		in   string
		line string
	}{
		{"a = 1\nb = \nc = 2\n", "line 2"},
		{"[x]\na = 1\n[x]\nb = 2\n", "line 3"},
		{"a = 1\nb = [1,\n", "line 2"},
		{"# c\n\nmcp_servers = 1\n", "line 3"},
		{"a = 1\n[[mcp_servers]]\nx = 1\n", "line 2"},
	}
	for _, tc := range cases {
		in := []byte(tc.in)
		outSet, errSet := Codex.Set(in, mapleHTTP)
		outRemove, errRemove := Codex.Remove(in, "maple")
		_, _, errCanon := Codex.Canonical(in, "maple")
		_, errList := Codex.List(in)
		for op, err := range map[string]error{"Set": errSet, "Remove": errRemove, "Canonical": errCanon, "List": errList} {
			if err == nil || !strings.Contains(err.Error(), tc.line) {
				t.Errorf("%s(%q) error = %v; want it to mention %q", op, tc.in, err, tc.line)
			}
		}
		if outSet != nil || outRemove != nil {
			t.Errorf("edit of %q returned content despite the error", tc.in)
		}
	}
}

func TestCodexCheckCatchesLeaks(t *testing.T) {
	// The post-edit check is the last guard before the user's file is
	// written, so it must reject any change outside the target entry.
	before := func() map[string]any { return decodeTOML(t, []byte("a = 1\n[mcp_servers.x]\nurl = \"u\"\n")) }
	want := map[string]any{"url": "m"}
	for _, tc := range []struct {
		name string
		out  string
		want any
	}{
		{"neighbour changed", "a = 2\n[mcp_servers.x]\nurl = \"u\"\n[mcp_servers.maple]\nurl = \"m\"\n", want},
		{"other entry lost", "a = 1\n[mcp_servers.maple]\nurl = \"m\"\n", want},
		{"entry differs", "a = 1\n[mcp_servers.x]\nurl = \"u\"\n[mcp_servers.maple]\nurl = \"other\"\n", want},
		{"entry left behind", "a = 1\n[mcp_servers.x]\nurl = \"u\"\n[mcp_servers.maple]\nurl = \"m\"\n", nil},
		{"invalid TOML", "a = \n", want},
	} {
		if err := checkCodex(before(), []byte(tc.out), "maple", tc.want); err == nil {
			t.Errorf("%s: checkCodex accepted %q", tc.name, tc.out)
		}
	}
	ok := "a = 1\n[mcp_servers.x]\nurl = \"u\"\n[mcp_servers.maple]\nurl = \"m\"\n"
	if err := checkCodex(before(), []byte(ok), "maple", want); err != nil {
		t.Errorf("checkCodex rejected a correct edit: %v", err)
	}
	nan := []byte("f = nan\n")
	if err := checkCodex(decodeTOML(t, nan), nan, "maple", nil); err != nil {
		t.Errorf("checkCodex rejected an unchanged document with nan: %v", err)
	}
}

func TestCodexPath(t *testing.T) {
	if got := Codex.Path(config.Paths{Home: "/h"}); got != "/h/.codex/config.toml" {
		t.Errorf("Codex.Path = %q", got)
	}
	if Codex.ID() != "codex" {
		t.Errorf("Codex.ID = %q", Codex.ID())
	}
}
