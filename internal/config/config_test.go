package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// specExample is the config.yaml example from spec section 6.2, verbatim.
const specExample = `port: 47321
servers:
  demo-http:
    url: https://example.com/mcp
    transport: http            # http（streamable HTTP，默认）| sse（旧版 SSE）
    headers:
      Authorization: Bearer ${DEMO_TOKEN}
    description: Example HTTP server for issue tracking
  demo-stdio:
    command: npx
    args: [-y, demo-mcp@1.2.3]
    env:
      API_KEY: ${DEMO_KEY}
    mode: shared               # shared（默认）| per-session | direct；只适用于 stdio
    disabled_tools: [delete_everything]
    enabled: true              # 默认 true
`

func TestParseSpecExample(t *testing.T) {
	c, err := Parse([]byte(specExample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", c.Port, DefaultPort)
	}
	stdio, remote := c.Servers["demo-stdio"], c.Servers["demo-http"]
	if stdio == nil || remote == nil {
		t.Fatalf("Servers = %v, want demo-stdio and demo-http", c.Servers)
	}
	if got := stdio.EffectiveMode(); got != "shared" {
		t.Errorf("demo-stdio EffectiveMode() = %q, want shared", got)
	}
	if got := remote.EffectiveTransport(); got != "http" {
		t.Errorf("demo-http EffectiveTransport() = %q, want http", got)
	}
	if !stdio.IsEnabled() || !remote.IsEnabled() {
		t.Errorf("IsEnabled() = %v/%v, want true/true", stdio.IsEnabled(), remote.IsEnabled())
	}
	if got, want := remote.Headers["Authorization"], "Bearer ${DEMO_TOKEN}"; got != want {
		t.Errorf("demo-http Authorization = %q, want %q", got, want)
	}
	if got, want := stdio.Args, []string{"-y", "demo-mcp@1.2.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("demo-stdio Args = %q, want %q", got, want)
	}
	if got, want := stdio.DisabledTools, []string{"delete_everything"}; !reflect.DeepEqual(got, want) {
		t.Errorf("demo-stdio DisabledTools = %q, want %q", got, want)
	}
}

func TestParseDefaults(t *testing.T) {
	for _, in := range []string{"", "servers: {}\n", "# only a comment\n"} {
		c, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if c.Port != DefaultPort {
			t.Errorf("Parse(%q).Port = %d, want DefaultPort", in, c.Port)
		}
		if c.Servers == nil {
			t.Errorf("Parse(%q).Servers is nil, want an empty map callers can add to", in)
		}
	}
}

func TestParseSyntaxError(t *testing.T) {
	_, err := Parse([]byte("port: 1\nservers:\n  x: [\n"))
	if err == nil {
		t.Fatal("Parse of broken YAML succeeded")
	}
	if !strings.HasPrefix(err.Error(), "config.yaml:") {
		t.Errorf("syntax error %q lacks the config.yaml prefix", err)
	}
}

func TestUnknownFieldHasLine(t *testing.T) {
	in := `port: 47321
servers:
  x:
    urll: https://example.com/mcp
`
	_, err := Parse([]byte(in))
	if err == nil {
		t.Fatal("Parse accepted unknown field urll")
	}
	msg := err.Error()
	for _, want := range []string{"config.yaml:4:", "urll", `server "x"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Line != 4 || ve.Server != "x" {
		t.Errorf("err = %#v, want *ValidationError at line 4 for server x", err)
	}
}

func TestDuplicateServerName(t *testing.T) {
	in := "servers:\n  x:\n    url: a\n  x:\n    url: b\n"
	_, err := Parse([]byte(in))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Line != 4 || !strings.Contains(ve.Msg, "x") {
		t.Fatalf("Parse duplicate name: err = %v, want ValidationError at line 4", err)
	}
}

func TestTypeErrorHasLine(t *testing.T) {
	_, err := Parse([]byte("servers:\n  x:\n    url: a\n    args: notalist\n"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Line != 4 || ve.Server != "x" {
		t.Fatalf("Parse wrong type: err = %v, want ValidationError at line 4 for x", err)
	}
}

func TestUnknownTopLevelField(t *testing.T) {
	// prot follows a server block, so it must not be blamed on that server.
	_, err := Parse([]byte("servers:\n  x:\n    url: a\nprot: 1\n"))
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Line != 4 || ve.Server != "" || !strings.Contains(ve.Msg, "prot") {
		t.Fatalf("Parse err = %#v, want top-level ValidationError at line 4 naming prot", err)
	}
}

func TestServerMethods(t *testing.T) {
	f, tr := false, true
	remote := &Server{URL: "https://x"}
	sse := &Server{URL: "https://x", Transport: "sse"}
	stdio := &Server{Command: "npx"}
	direct := &Server{Command: "npx", Mode: "direct", Enabled: &f}

	if !remote.IsEnabled() || direct.IsEnabled() || !(&Server{Enabled: &tr}).IsEnabled() {
		t.Error("IsEnabled: want nil→true, false→false, true→true")
	}
	if remote.IsStdio() || !stdio.IsStdio() {
		t.Error("IsStdio: want false for url, true for command")
	}
	if got := remote.EffectiveMode(); got != "" {
		t.Errorf("remote EffectiveMode() = %q, want empty", got)
	}
	if got := stdio.EffectiveMode(); got != "shared" {
		t.Errorf("stdio EffectiveMode() = %q, want shared", got)
	}
	if got := direct.EffectiveMode(); got != "direct" {
		t.Errorf("direct EffectiveMode() = %q, want direct", got)
	}
	if got := remote.EffectiveTransport(); got != "http" {
		t.Errorf("remote EffectiveTransport() = %q, want http", got)
	}
	if got := sse.EffectiveTransport(); got != "sse" {
		t.Errorf("sse EffectiveTransport() = %q, want sse", got)
	}
	if got := stdio.EffectiveTransport(); got != "" {
		t.Errorf("stdio EffectiveTransport() = %q, want empty", got)
	}
}

func TestSecretRefs(t *testing.T) {
	s := &Server{
		URL:     "https://x/${B}",
		Headers: map[string]string{"A": "Bearer ${A}", "C": "${B}"},
		Args:    []string{"--k=${C}", "${A}"},
		Env:     map[string]string{"E": "${D}"},
		// Not an expansion site: must not be reported.
		Description: "${NOT_A_REF}",
	}
	if got, want := s.SecretRefs(), []string{"A", "B", "C", "D"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SecretRefs() = %q, want %q", got, want)
	}
	if got := (&Server{Command: "x"}).SecretRefs(); len(got) != 0 {
		t.Errorf("SecretRefs() without refs = %q, want empty", got)
	}
}

func TestHashStable(t *testing.T) {
	a := `servers:
  x:
    command: npx
    args: [-y, pkg]
    env: {B: "2", A: "1"}
    mode: per-session
`
	b := `servers:
  x:
    mode: per-session
    env:
      A: "1"
      B: "2"
    args: [-y, pkg]
    command: npx
`
	changed := `servers:
  x:
    command: npx
    args: [-y, pkg@2]
    env: {B: "2", A: "1"}
    mode: per-session
`
	hash := func(in string) string {
		t.Helper()
		c, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		return c.Servers["x"].Hash()
	}
	ha, hb, hc := hash(a), hash(b), hash(changed)
	if ha != hb {
		t.Errorf("Hash differs by YAML field order: %s vs %s", ha, hb)
	}
	if ha == hc {
		t.Errorf("Hash did not change after editing args: %s", ha)
	}
	if !strings.HasPrefix(ha, "sha256:") || len(ha) != len("sha256:")+64 {
		t.Errorf("Hash() = %q, want sha256:<64 hex>", ha)
	}
}

func TestContentHash(t *testing.T) {
	const empty = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := ContentHash(nil); got != empty {
		t.Errorf("ContentHash(nil) = %q, want %q", got, empty)
	}
	if ContentHash([]byte("a")) == ContentHash([]byte("b")) {
		t.Error("ContentHash collides for different input")
	}
}
