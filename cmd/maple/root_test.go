package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

func TestRootVersion(t *testing.T) {
	cmd := newRootCmd("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := out.String(), "maple version 1.2.3"; !strings.Contains(got, want) {
		t.Fatalf("output = %q, want it to contain %q", got, want)
	}
}

func TestRootJSONFlag(t *testing.T) {
	cmd := newRootCmd("dev")

	// Persistent, so every subcommand added later inherits --json.
	f := cmd.PersistentFlags().Lookup("json")
	if f == nil {
		t.Fatal("persistent flag --json is not registered")
	}
	if f.Value.Type() != "bool" || f.DefValue != "false" {
		t.Fatalf("--json: type = %q, default = %q; want bool, false", f.Value.Type(), f.DefValue)
	}
}

func TestFileKeychainWarns(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	var errOut bytes.Buffer
	t.Setenv("MAPLE_KEYCHAIN", "")
	newKeychain(p, &errOut)
	if errOut.Len() != 0 {
		t.Errorf("real keychain warned: %q", errOut.String())
	}
	t.Setenv("MAPLE_KEYCHAIN", "file")
	newKeychain(p, &errOut)
	if got := errOut.String(); got != "warning: MAPLE_KEYCHAIN=file stores secrets in plaintext; for tests only\n" {
		t.Errorf("warning = %q", got)
	}
}
