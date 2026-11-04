package gateway

import (
	"strings"
	"testing"
)

func TestToolName(t *testing.T) {
	taken := map[string]bool{}
	if got := ToolName("tapd", "get story", taken); got != "tapd__get-story" {
		t.Fatalf("got %q", got)
	}
	if !taken["tapd__get-story"] {
		t.Fatal("result not recorded in taken")
	}
	if got := ToolName("s", "工具", taken); got != "s__--" {
		t.Fatalf("multibyte: got %q", got)
	}

	long := strings.Repeat("x", 70)
	a := ToolName("srv", long+"a", taken)
	b := ToolName("srv", long+"b", taken)
	c := ToolName("srv", long+"c", taken)
	if len(a) != 64 || a != ("srv__" + long)[:64] {
		t.Fatalf("a = %q (%d)", a, len(a))
	}
	if b != a[:62]+"_2" || c != a[:62]+"_3" {
		t.Fatalf("b = %q, c = %q", b, c)
	}
	if len(b) > 64 || len(c) > 64 {
		t.Fatal("suffixed names exceed 64")
	}

	if got := ToolName("a b", "c", map[string]bool{}); got != "a-b__c" {
		t.Fatalf("empty taken: got %q", got)
	}

	// A short colliding name keeps its full base.
	if got := ToolName("tapd", "get/story", taken); got != "tapd__get-story_2" {
		t.Fatalf("got %q", got)
	}
}
