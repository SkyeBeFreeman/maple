package main

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/SkyeBeFreeman/maple/internal/manage"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

func TestParseKV(t *testing.T) {
	m, err := parseKV("env", []string{"A=1", "B=x=y", "C="})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"A": "1", "B": "x=y", "C": ""}; !maps.Equal(m, want) {
		t.Fatalf("parseKV = %v, want %v", m, want)
	}
	for _, bad := range []string{"A", "=1"} {
		if _, err := parseKV("env", []string{bad}); err == nil {
			t.Errorf("parseKV(%q) accepted", bad)
		}
	}
	if m, err := parseKV("env", nil); m != nil || err != nil {
		t.Errorf("parseKV(nil) = %v, %v", m, err)
	}
}

func TestMergeKV(t *testing.T) {
	got := mergeKV(map[string]string{"A": "1", "B": "2"}, map[string]string{"A": "", "C": "3"})
	if want := map[string]string{"B": "2", "C": "3"}; !maps.Equal(got, want) {
		t.Fatalf("mergeKV = %v, want %v", got, want)
	}
	if got := mergeKV(map[string]string{"A": "1"}, map[string]string{"A": ""}); got != nil {
		t.Fatalf("mergeKV emptied = %v, want nil", got)
	}
}

func TestEditTools(t *testing.T) {
	got := editTools([]string{"a"}, []string{"c", "b", "a"}, []string{"b"})
	if want := []string{"a", "c"}; !slices.Equal(got, want) {
		t.Fatalf("editTools = %v, want %v", got, want)
	}
	if got := editTools([]string{"a"}, nil, []string{"a"}); got != nil {
		t.Fatalf("editTools emptied = %v, want nil", got)
	}
}

func TestPickNumbers(t *testing.T) {
	c := []agentconf.Foreign{{Name: "x"}, {Name: "y"}, {Name: "z"}}
	if got := pickNumbers("3, 1 1 9 q", c); !slices.Equal(got, []string{"z", "x"}) {
		t.Fatalf("pickNumbers = %v", got)
	}
	if got := pickNumbers("ALL", c); len(got) != 3 {
		t.Fatalf("pickNumbers(all) = %v", got)
	}
	if got := pickNumbers("", c); got != nil {
		t.Fatalf("pickNumbers(empty) = %v", got)
	}
}

func TestImportViewHidesValues(t *testing.T) {
	res := &manage.ImportResult{Candidates: []agentconf.Foreign{{
		Name: "x", URL: "https://example.com", Headers: map[string]string{"Authorization": "Bearer sekrit"},
	}, {Name: "y", Command: "y", Env: map[string]string{"API_KEY": "sekrit"}}}}
	b, err := json.Marshal(importView(res))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sekrit") || !strings.Contains(string(b), "API_KEY") || !strings.Contains(string(b), "Authorization") {
		t.Fatalf("import output = %s", b)
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		args    []string
		name    string
		command []string
		wantErr bool
	}{
		{args: []string{"x"}, name: "x"},
		{args: []string{"x", "--", "npx", "-y", "pkg"}, name: "x", command: []string{"npx", "-y", "pkg"}},
		{args: []string{"x", "npx"}, wantErr: true},
		{args: []string{"x", "y", "--", "npx"}, wantErr: true},
		{args: []string{"x", "--"}, wantErr: true},
	}
	for _, c := range cases {
		var name string
		var command []string
		var err error
		cmd := &cobra.Command{Use: "add", RunE: func(cmd *cobra.Command, args []string) error {
			name, command, err = splitCommand(cmd, args)
			return nil
		}}
		cmd.SetArgs(c.args)
		if xerr := cmd.Execute(); xerr != nil {
			t.Fatal(xerr)
		}
		if (err != nil) != c.wantErr || name != c.name || !slices.Equal(command, c.command) {
			t.Errorf("splitCommand(%q) = %q, %q, %v", c.args, name, command, err)
		}
	}
}
