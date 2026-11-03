package gateway

import (
	"sort"
	"strings"
	"sync"
)

// minSecret skips tiny values whose redaction would mangle ordinary text.
const minSecret = 4

// Redactor masks known secret values in text bound for logs or the model.
type Redactor struct {
	mu       sync.RWMutex
	secrets  map[string]bool
	replacer *strings.Replacer
}

// NewRedactor returns a Redactor for secrets.
func NewRedactor(secrets []string) *Redactor {
	r := &Redactor{secrets: map[string]bool{}}
	r.Add(secrets...)
	return r
}

// Add registers more secrets, e.g. as the gateway expands server configs.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for _, s := range secrets {
		if len(s) >= minSecret && !r.secrets[s] {
			r.secrets[s] = true
			changed = true
		}
	}
	if !changed {
		return
	}
	list := make([]string, 0, len(r.secrets))
	for s := range r.secrets {
		list = append(list, s)
	}
	// strings.Replacer tries old strings in argument order at each position,
	// so longer first keeps a secret's prefix from leaving its tail exposed.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	pairs := make([]string, 0, 2*len(list))
	for _, s := range list {
		pairs = append(pairs, s, "***")
	}
	r.replacer = strings.NewReplacer(pairs...)
}

// Redact replaces every known secret in s with ***. A nil Redactor is a no-op
// so callers need not wait for secrets to be known.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	rep := r.replacer
	r.mu.RUnlock()
	if rep == nil {
		return s
	}
	return rep.Replace(s)
}
