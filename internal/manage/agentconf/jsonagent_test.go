package agentconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

var (
	mapleHTTP = Entry{
		Name:    "maple",
		URL:     "http://127.0.0.1:47321/mcp",
		Headers: map[string]string{"Authorization": "Bearer test-token"},
	}
	notesStdio = Entry{
		Name:    "notes",
		Command: "/opt/maple/bin/maple",
		Args:    []string{"run", "notes"},
	}
	jsonAgents = []Agent{Claude, CodeBuddy}
)

func readTestdata(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustSet(t *testing.T, a Agent, data []byte, e Entry) []byte {
	t.Helper()
	out, err := a.Set(data, e)
	if err != nil {
		t.Fatalf("%s Set(%s): %v", a.ID(), e.Name, err)
	}
	if !json.Valid(out) {
		t.Fatalf("%s Set(%s) produced invalid JSON:\n%s", a.ID(), e.Name, out)
	}
	return out
}

func assertBytes(t *testing.T, label string, got, want []byte) {
	t.Helper()
	if string(got) != string(want) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", label, got, want)
	}
}

func testSetGolden(t *testing.T, a Agent) {
	input := readTestdata(t, a.ID(), "input.json")
	for _, tc := range []struct {
		golden string
		entry  Entry
	}{
		{"set-http.golden.json", mapleHTTP},
		{"set-stdio.golden.json", notesStdio},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			out := mustSet(t, a, input, tc.entry)
			assertBytes(t, "first Set", out, readTestdata(t, a.ID(), tc.golden))
			again := mustSet(t, a, out, tc.entry)
			assertBytes(t, "second Set (idempotence)", again, out)
		})
	}
}

func TestClaudeSetPreservesRest(t *testing.T) { testSetGolden(t, Claude) }

func TestCodeBuddySetPreservesRest(t *testing.T) { testSetGolden(t, CodeBuddy) }

func TestRemoveOnlyTarget(t *testing.T) {
	for _, a := range jsonAgents {
		t.Run(a.ID(), func(t *testing.T) {
			input := readTestdata(t, a.ID(), "input.json")
			for _, e := range []Entry{mapleHTTP, notesStdio} {
				out, err := a.Remove(mustSet(t, a, input, e), e.Name)
				if err != nil {
					t.Fatal(err)
				}
				assertBytes(t, "Remove("+e.Name+")", out, input)
			}
		})
	}
}

func TestRemoveAbsentIsNoop(t *testing.T) {
	for _, a := range jsonAgents {
		for _, in := range []string{"", "{}", `{"other":1}`, `{"mcpServers":{"x":{}}}`} {
			out, err := a.Remove([]byte(in), "maple")
			if err != nil {
				t.Fatalf("%s Remove on %q: %v", a.ID(), in, err)
			}
			assertBytes(t, a.ID()+" Remove on "+in, out, []byte(in))
		}
	}
}

func TestRemoveRestoresEmptyServers(t *testing.T) {
	in := []byte("{\n  \"mcpServers\": {},\n  \"other\": 1\n}\n")
	for _, a := range jsonAgents {
		out, err := a.Remove(mustSet(t, a, in, mapleHTTP), "maple")
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, a.ID(), out, in)
	}
}

func TestRemoveUserEntryWithPathChars(t *testing.T) {
	// import --remove-originals deletes user entries, whose names may contain
	// characters that are gjson/sjson path syntax.
	in := []byte("{\n  \"mcpServers\": {\n    \"a.b\": {\"url\": \"u\"},\n    \"a\": {\"b\": 1}\n  }\n}\n")
	want := []byte("{\n  \"mcpServers\": {\n    \"a\": {\"b\": 1}\n  }\n}\n")
	for _, a := range jsonAgents {
		out, err := a.Remove(in, "a.b")
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, a.ID(), out, want)
	}
}

func TestSetIntoEmptyAndBareFiles(t *testing.T) {
	inputs := []string{"", "\n", "{}", "{}\n", " {\n}\n", `{"other":1}`, `{"mcpServers": {}}`, `{"mcpServers": null}`, "{\n  \"other\": 1\n}\n"}
	for _, a := range jsonAgents {
		for _, in := range inputs {
			out := mustSet(t, a, []byte(in), mapleHTTP)
			if got := gjson.GetBytes(out, "mcpServers.maple.url").String(); got != mapleHTTP.URL {
				t.Errorf("%s Set into %q: url = %q\n%s", a.ID(), in, got, out)
			}
			if strings.Contains(in, "other") && gjson.GetBytes(out, "other").Int() != 1 {
				t.Errorf("%s Set into %q lost other key:\n%s", a.ID(), in, out)
			}
		}
	}
}

func TestSetLayout(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "empty file",
			in:   "",
			want: "{\n  \"mcpServers\": {\n    \"notes\": {\n      \"type\": \"stdio\",\n      \"command\": \"/opt/maple/bin/maple\",\n      \"args\": [\n        \"run\",\n        \"notes\"\n      ]\n    }\n  }\n}\n",
		},
		{
			name: "missing servers goes last",
			in:   "{\n  \"b\": 1,\n  \"a\": 2\n}\n",
			want: "{\n  \"b\": 1,\n  \"a\": 2,\n  \"mcpServers\": {\n    \"notes\": {\n      \"type\": \"stdio\",\n      \"command\": \"/opt/maple/bin/maple\",\n      \"args\": [\n        \"run\",\n        \"notes\"\n      ]\n    }\n  }\n}\n",
		},
		{
			// Keeping "}}" lets Remove restore a compact file byte for byte.
			name: "compact object",
			in:   `{"other":1}`,
			want: "{\"other\":1,\n  \"mcpServers\": {\n    \"notes\": {\n      \"type\": \"stdio\",\n      \"command\": \"/opt/maple/bin/maple\",\n      \"args\": [\n        \"run\",\n        \"notes\"\n      ]\n    }\n  }}",
		},
		{
			name: "tab indent",
			in:   "{\n\t\"mcpServers\": {\n\t\t\"x\": {}\n\t}\n}\n",
			want: "{\n\t\"mcpServers\": {\n\t\t\"x\": {},\n\t\t\"notes\": {\n\t\t\t\"type\": \"stdio\",\n\t\t\t\"command\": \"/opt/maple/bin/maple\",\n\t\t\t\"args\": [\n\t\t\t\t\"run\",\n\t\t\t\t\"notes\"\n\t\t\t]\n\t\t}\n\t}\n}\n",
		},
		{
			name: "four space indent and empty servers",
			in:   "{\n    \"mcpServers\": {}\n}",
			want: "{\n    \"mcpServers\": {\n        \"notes\": {\n            \"type\": \"stdio\",\n            \"command\": \"/opt/maple/bin/maple\",\n            \"args\": [\n                \"run\",\n                \"notes\"\n            ]\n        }\n    }\n}",
		},
		{
			name: "crlf line endings",
			in:   "{\r\n  \"mcpServers\": {\r\n    \"x\": {}\r\n  }\r\n}\r\n",
			want: "{\r\n  \"mcpServers\": {\r\n    \"x\": {},\r\n    \"notes\": {\r\n      \"type\": \"stdio\",\r\n      \"command\": \"/opt/maple/bin/maple\",\r\n      \"args\": [\r\n        \"run\",\r\n        \"notes\"\r\n      ]\r\n    }\r\n  }\r\n}\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustSet(t, Claude, []byte(tc.in), notesStdio)
			assertBytes(t, tc.name, out, []byte(tc.want))
			// Remove only restores the input when Set did not have to create
			// the mcpServers section itself.
			if strings.Contains(tc.in, "mcpServers") {
				back, err := Claude.Remove(out, notesStdio.Name)
				if err != nil {
					t.Fatal(err)
				}
				assertBytes(t, tc.name+" round trip", back, []byte(tc.in))
			}
		})
	}
}

func TestSetReplacesInPlace(t *testing.T) {
	in := readTestdata(t, "claude", "set-http.golden.json")
	e := mapleHTTP
	e.Headers = map[string]string{"Authorization": "Bearer rotated"}
	out := mustSet(t, Claude, in, e)
	want := strings.Replace(string(in), "Bearer test-token", "Bearer rotated", 1)
	assertBytes(t, "replace", out, []byte(want))

	mid := []byte("{\n  \"mcpServers\": {\n    \"maple\": {\"url\": \"old\"},\n    \"z\": {}\n  }\n}\n")
	out = mustSet(t, CodeBuddy, mid, mapleHTTP)
	wantMid := "{\n  \"mcpServers\": {\n    \"maple\": {\n      \"url\": \"http://127.0.0.1:47321/mcp\",\n      \"transportType\": \"streamable-http\",\n      \"headers\": {\n        \"Authorization\": \"Bearer test-token\"\n      }\n    },\n    \"z\": {}\n  }\n}\n"
	assertBytes(t, "replace middle", out, []byte(wantMid))
}

func TestSetRejectsBadEntry(t *testing.T) {
	for _, e := range []Entry{{Name: "", URL: "u"}, {Name: "x"}} {
		if _, err := Claude.Set([]byte("{}"), e); err == nil {
			t.Errorf("Set(%+v) succeeded, want error", e)
		}
	}
}

func TestEscaping(t *testing.T) {
	cmd := `/Users/a b/"q"/maple`
	e := Entry{Name: "esc", Command: cmd, Args: []string{"run", `x\y`, "a&b<c>"}}
	for _, a := range jsonAgents {
		out := mustSet(t, a, readTestdata(t, a.ID(), "input.json"), e)
		got := gjson.GetBytes(out, "mcpServers.esc")
		if got.Get("command").String() != cmd {
			t.Errorf("%s command = %q, want %q", a.ID(), got.Get("command").String(), cmd)
		}
		var args []string
		if err := json.Unmarshal([]byte(got.Get("args").Raw), &args); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(args, e.Args) {
			t.Errorf("%s args = %q, want %q", a.ID(), args, e.Args)
		}
		// encoding/json's default HTML-safe escaping would turn '&' into an
		// escape sequence, making the user's file harder to read.
		if !strings.Contains(got.Raw, `"a&b<c>"`) {
			t.Errorf("%s output HTML-escapes '&':\n%s", a.ID(), out)
		}
	}
}

func TestCanonicalOrderInsensitive(t *testing.T) {
	a := []byte(`{"mcpServers": {"maple": {"type": "http", "url": "u", "headers": {"B": "2", "A": "1"}, "n": 1.50}}}`)
	b := []byte("{\n  \"mcpServers\": {\n    \"maple\": {\n      \"headers\": {\"A\": \"1\", \"B\": \"2\"},\n      \"n\": 1.50,\n      \"url\": \"u\",\n      \"type\": \"http\"\n    }\n  }\n}\n")
	for _, ag := range jsonAgents {
		ca, okA, err := ag.Canonical(a, "maple")
		if err != nil || !okA {
			t.Fatalf("%s Canonical(a) = %v, %v", ag.ID(), okA, err)
		}
		cb, okB, err := ag.Canonical(b, "maple")
		if err != nil || !okB {
			t.Fatalf("%s Canonical(b) = %v, %v", ag.ID(), okB, err)
		}
		assertBytes(t, ag.ID(), ca, cb)
		want := `{"headers":{"A":"1","B":"2"},"n":1.50,"type":"http","url":"u"}`
		assertBytes(t, ag.ID()+" canonical form", ca, []byte(want))

		if _, ok, err := ag.Canonical(a, "absent"); ok || err != nil {
			t.Errorf("%s Canonical(absent) = %v, %v; want false, nil", ag.ID(), ok, err)
		}
		if _, ok, err := ag.Canonical(nil, "maple"); ok || err != nil {
			t.Errorf("%s Canonical(empty) = %v, %v; want false, nil", ag.ID(), ok, err)
		}
	}
}

func TestCanonicalMatchesWrittenEntry(t *testing.T) {
	for _, a := range jsonAgents {
		x, _, err := a.Canonical(mustSet(t, a, readTestdata(t, a.ID(), "input.json"), mapleHTTP), "maple")
		if err != nil {
			t.Fatal(err)
		}
		y, _, err := a.Canonical(mustSet(t, a, nil, mapleHTTP), "maple")
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, a.ID(), x, y)
	}
}

func TestListForeign(t *testing.T) {
	got, err := CodeBuddy.List(readTestdata(t, "codebuddy", "list.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Foreign{
		{Name: "remote-a", URL: "https://mcp.example.com/a/mcp", Transport: config.TransportHTTP, Headers: map[string]string{"Authorization": "Bearer ${A_TOKEN}"}},
		{Name: "remote-b", URL: "https://mcp.example.com/b/mcp", Transport: config.TransportHTTP, Headers: map[string]string{"X-Api-Key": "${B_KEY}"}},
		{Name: "local", Command: "npx", Args: []string{"-y", "example-mcp"}, Env: map[string]string{"EXAMPLE_KEY": "${EXAMPLE_KEY}"}},
		{Name: "remote-c", URL: "https://mcp.example.com/c/mcp", Transport: config.TransportHTTP},
		{Name: "remote-d", URL: "https://mcp.example.com/d/sse", Transport: config.TransportSSE},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodeBuddy.List =\n%#v\nwant\n%#v", got, want)
	}

	got, err = Claude.List(readTestdata(t, "claude", "list.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = []Foreign{
		{Name: "remote-http", URL: "https://mcp.example.com/mcp", Transport: config.TransportHTTP, Headers: map[string]string{"Authorization": "Bearer ${EXAMPLE_TOKEN}"}},
		{Name: "remote-sse", URL: "https://mcp.example.com/sse", Transport: config.TransportSSE},
		{Name: "local", Command: "npx", Args: []string{"-y", "example-mcp"}, Env: map[string]string{"EXAMPLE_KEY": "${EXAMPLE_KEY}"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Claude.List =\n%#v\nwant\n%#v", got, want)
	}
}

func TestListEmpty(t *testing.T) {
	for _, a := range jsonAgents {
		for _, in := range []string{"", "{}", `{"mcpServers": {}}`, `{"mcpServers": null}`} {
			got, err := a.List([]byte(in))
			if err != nil || len(got) != 0 {
				t.Errorf("%s List(%q) = %v, %v; want empty", a.ID(), in, got, err)
			}
		}
	}
}

func TestParseErrorHasLine(t *testing.T) {
	cases := []struct {
		in   string
		line string
	}{
		{"{\n  \"a\": 1,\n  \"b\": 2\n  \"c\": 3\n}\n", "line 4"},
		{"{\n  \"a\": 1,\n", "line 2"},
		{"[1, 2]\n", "line 1"},
		{"{\n  \"x\": 1,\n  \"mcpServers\": []\n}\n", "line 3"},
	}
	for _, a := range jsonAgents {
		for _, tc := range cases {
			in := []byte(tc.in)
			_, errSet := a.Set(in, mapleHTTP)
			_, errRemove := a.Remove(in, "maple")
			_, _, errCanon := a.Canonical(in, "maple")
			_, errList := a.List(in)
			for op, err := range map[string]error{"Set": errSet, "Remove": errRemove, "Canonical": errCanon, "List": errList} {
				if err == nil || !strings.Contains(err.Error(), tc.line) {
					t.Errorf("%s %s(%q) error = %v; want it to mention %q", a.ID(), op, tc.in, err, tc.line)
				}
			}
		}
	}
}

func TestPaths(t *testing.T) {
	p := config.Paths{Home: "/h"}
	if got := Claude.Path(p); got != "/h/.claude.json" {
		t.Errorf("Claude.Path = %q", got)
	}
	if got := CodeBuddy.Path(p); got != "/h/.codebuddy/mcp.json" {
		t.Errorf("CodeBuddy.Path = %q", got)
	}
}

func TestAll(t *testing.T) {
	var ids []string
	for _, a := range All() {
		ids = append(ids, a.ID())
	}
	if want := []string{"claude", "codebuddy"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("All() IDs = %v, want %v", ids, want)
	}
}
