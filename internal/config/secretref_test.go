package config

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var errNotFound = errors.New("secret not found")

func mapLookup(m map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		v, ok := m[name]
		if !ok {
			return "", fmt.Errorf("lookup %s: %w", name, errNotFound)
		}
		return v, nil
	}
}

func TestExpand(t *testing.T) {
	lookup := mapLookup(map[string]string{"T": "abc", "U": "x"})
	cases := []struct{ in, want string }{
		{"Bearer ${T}", "Bearer abc"},
		{"cost $5", "cost $5"},
		{"$", "$"},
		{"a$${T}b", "a$abcb"},
		{"${T}${U}${T}", "abcxabc"},
		{"", ""},
	}
	for _, c := range cases {
		got, err := Expand(c.in, lookup)
		if err != nil || got != c.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}

	_, err := Expand("Bearer ${MISSING}", lookup)
	if !errors.Is(err, errNotFound) {
		t.Errorf("Expand missing: err = %v, want wrapping errNotFound", err)
	}
	var se *SecretError
	if !errors.As(err, &se) || se.Name != "MISSING" {
		t.Errorf("Expand missing: err = %v, want *SecretError{Name: MISSING}", err)
	}

	for _, bad := range []string{"${lower}", "${}", "${A", "${A-B}", "${1A}", "${A:-x}"} {
		if _, err := Expand(bad, lookup); err == nil {
			t.Errorf("Expand(%q) succeeded, want syntax error", bad)
		}
	}
}

func TestRefs(t *testing.T) {
	got, err := Refs("${A} and ${B_2} and ${A} cost $5")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	if want := []string{"A", "B_2", "A"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Refs = %q, want %q", got, want)
	}
	if _, err := Refs("${lower}"); err == nil {
		t.Error("Refs(${lower}) succeeded")
	}
	if got, _ := Refs("plain"); len(got) != 0 {
		t.Errorf("Refs(plain) = %q, want empty", got)
	}
}

func TestSecretErrorFormat(t *testing.T) {
	e := &SecretError{Name: "N", Err: errNotFound}
	if got, want := e.Error(), "secret N: secret not found"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(e, errNotFound) {
		t.Error("SecretError does not unwrap to Err")
	}
}

func TestServerExpand(t *testing.T) {
	orig := &Server{
		URL:     "https://x/${U}",
		Headers: map[string]string{"Authorization": "Bearer ${T}", "X": "plain"},
		Command: "npx",
		Args:    []string{"--key=${K}", "${T}"},
		Env:     map[string]string{"API": "${E}"},
		Mode:    "per-session",
		// Not an expansion site.
		Description:   "${T}",
		DisabledTools: []string{"a"},
	}
	before := orig.Hash()
	lookup := mapLookup(map[string]string{"U": "u1", "T": "t1", "K": "k1", "E": "e1"})
	got, used, err := orig.Expand(lookup)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got.URL != "https://x/u1" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Headers["Authorization"] != "Bearer t1" || got.Headers["X"] != "plain" {
		t.Errorf("Headers = %v", got.Headers)
	}
	if want := []string{"--key=k1", "t1"}; !reflect.DeepEqual(got.Args, want) {
		t.Errorf("Args = %q, want %q", got.Args, want)
	}
	if got.Env["API"] != "e1" {
		t.Errorf("Env = %v", got.Env)
	}
	if got.Description != "${T}" || got.Mode != "per-session" || got.Command != "npx" {
		t.Errorf("non-expanded fields changed: %+v", got)
	}
	if want := []string{"e1", "k1", "t1", "u1"}; !reflect.DeepEqual(used, want) {
		t.Errorf("used = %q, want %q", used, want)
	}
	if orig.Hash() != before || orig.Headers["Authorization"] != "Bearer ${T}" || orig.Args[1] != "${T}" {
		t.Errorf("original Server modified: %+v", orig)
	}
	got.DisabledTools[0] = "changed"
	if orig.DisabledTools[0] != "a" {
		t.Error("Expand copy shares DisabledTools with the original")
	}

	_, _, err = (&Server{Command: "x", Env: map[string]string{"K": "${NOPE}"}}).Expand(lookup)
	var se *SecretError
	if !errors.As(err, &se) || se.Name != "NOPE" || !errors.Is(err, errNotFound) {
		t.Errorf("Expand missing: err = %v, want *SecretError NOPE wrapping errNotFound", err)
	}
}

// Each keychain read may prompt the user, so one name is looked up once per
// Expand even when referenced several times.
func TestServerExpandLooksUpOnce(t *testing.T) {
	calls := map[string]int{}
	lookup := func(name string) (string, error) {
		calls[name]++
		return "v", nil
	}
	s := &Server{URL: "https://${T}", Headers: map[string]string{"A": "${T}"}, Args: []string{"${T}"}}
	if _, _, err := s.Expand(lookup); err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if calls["T"] != 1 {
		t.Errorf("lookup(T) called %d times, want 1", calls["T"])
	}
}

// An empty secret must not be offered for redaction: replacing "" would
// mangle every log line.
func TestServerExpandSkipsEmptyValues(t *testing.T) {
	s := &Server{Command: "x", Env: map[string]string{"A": "${E}"}}
	_, used, err := s.Expand(func(string) (string, error) { return "", nil })
	if err != nil || len(used) != 0 {
		t.Errorf("Expand = %q, %v; want no used values", used, err)
	}
}

// A value with a stray "${" may hold a pasted credential after it; the error
// is printed to terminals and logs, so it must not echo the rest of the value.
func TestUnterminatedRefDoesNotEchoValue(t *testing.T) {
	_, err := Refs("Bearer ${TOKEN sk-live-123")
	if err == nil || strings.Contains(err.Error(), "sk-live") {
		t.Errorf("Refs err = %v, want an error that omits the value", err)
	}
}

func TestIsSecretName(t *testing.T) {
	for _, ok := range []string{"A", "_X", "DEMO_TOKEN_2"} {
		if !IsSecretName(ok) {
			t.Errorf("IsSecretName(%q) = false, want true", ok)
		}
	}
	// "A}${B" would slip through a check built on Refs("${"+name+"}").
	for _, bad := range []string{"", "a", "1X", "A-B", "A}${B", "A B"} {
		if IsSecretName(bad) {
			t.Errorf("IsSecretName(%q) = true, want false", bad)
		}
	}
}
