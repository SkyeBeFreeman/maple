package gateway

import (
	"sync"
	"testing"
)

func TestRedact(t *testing.T) {
	r := NewRedactor([]string{"abcdef", "ab"})
	if got := r.Redact("token abcdef leaked ab"); got != "token *** leaked ab" {
		t.Fatalf("got %q", got)
	}
	// Longer secrets win over their prefixes.
	r = NewRedactor([]string{"abcd", "abcdefgh"})
	if got := r.Redact("x abcdefgh y abcd"); got != "x *** y ***" {
		t.Fatalf("got %q", got)
	}
	var nilR *Redactor
	if got := nilR.Redact("abcdef"); got != "abcdef" {
		t.Fatalf("nil redactor: %q", got)
	}
}

func TestRedactorAddConcurrent(t *testing.T) {
	r := NewRedactor(nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); r.Add("secret-value", "xy") }()
		go func() { defer wg.Done(); _ = r.Redact("secret-value") }()
	}
	wg.Wait()
	if got := r.Redact("a secret-value b xy"); got != "a *** b xy" {
		t.Fatalf("got %q", got)
	}
}
