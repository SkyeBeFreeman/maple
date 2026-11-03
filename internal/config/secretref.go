package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var secretNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// IsSecretName reports whether name may be stored and referenced as ${name}.
func IsSecretName(name string) bool { return secretNameRe.MatchString(name) }

// SecretError reports that the value of a referenced secret could not be
// obtained.
type SecretError struct {
	Name string
	Err  error
}

func (e *SecretError) Error() string { return fmt.Sprintf("secret %s: %v", e.Name, e.Err) }

func (e *SecretError) Unwrap() error { return e.Err }

// Refs returns the secret names referenced as ${NAME} in s, in order of
// appearance. A "$" not followed by "{" is literal; anything else starting
// with "${" that is not a valid reference is an error, since there is no
// escape for a literal "${".
func Refs(s string) ([]string, error) {
	var names []string
	err := scan(s, func(string) {}, func(name string) error {
		names = append(names, name)
		return nil
	})
	return names, err
}

// Expand replaces every ${NAME} in s with lookup(NAME). Lookup failures are
// returned as *SecretError wrapping the lookup's error.
func Expand(s string, lookup func(name string) (string, error)) (string, error) {
	var b strings.Builder
	err := scan(s, func(lit string) { b.WriteString(lit) }, func(name string) error {
		v, err := lookup(name)
		if err != nil {
			return &SecretError{Name: name, Err: err}
		}
		b.WriteString(v)
		return nil
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}

func scan(s string, literal func(string), ref func(name string) error) error {
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			literal(s)
			return nil
		}
		literal(s[:i])
		end := strings.IndexByte(s[i:], '}')
		if end < 0 {
			// Do not echo the tail: it may be a pasted credential.
			return errors.New(`unterminated "${"`)
		}
		name := s[i+2 : i+end]
		if !secretNameRe.MatchString(name) {
			return fmt.Errorf("invalid secret reference %q: name must match [A-Z_][A-Z0-9_]*", s[i:i+end+1])
		}
		if err := ref(name); err != nil {
			return err
		}
		s = s[i+end+1:]
	}
}

// Expand returns a deep copy of s with secrets expanded in url, header
// values, args and env values, plus the distinct non-empty secret values it
// substituted so callers can redact them from logs. Each name is looked up
// once, since every keychain read may prompt the user.
func (s *Server) Expand(lookup func(name string) (string, error)) (*Server, []string, error) {
	got := map[string]string{}
	exp := func(v string) (string, error) {
		return Expand(v, func(name string) (string, error) {
			if val, ok := got[name]; ok {
				return val, nil
			}
			val, err := lookup(name)
			if err == nil {
				got[name] = val
			}
			return val, err
		})
	}
	expMap := func(m map[string]string) (map[string]string, error) {
		if m == nil {
			return nil, nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			e, err := exp(v)
			if err != nil {
				return nil, err
			}
			out[k] = e
		}
		return out, nil
	}

	c := *s
	var err error
	if c.URL, err = exp(s.URL); err != nil {
		return nil, nil, err
	}
	if c.Headers, err = expMap(s.Headers); err != nil {
		return nil, nil, err
	}
	if c.Env, err = expMap(s.Env); err != nil {
		return nil, nil, err
	}
	if s.Args != nil {
		c.Args = make([]string, len(s.Args))
		for i, a := range s.Args {
			if c.Args[i], err = exp(a); err != nil {
				return nil, nil, err
			}
		}
	}
	if s.DisabledTools != nil {
		c.DisabledTools = append([]string(nil), s.DisabledTools...)
	}
	if s.Enabled != nil {
		e := *s.Enabled
		c.Enabled = &e
	}

	// Empty values are left out: redacting "" would mangle every log line.
	seen := map[string]bool{}
	values := make([]string, 0, len(got))
	for _, v := range got {
		if v != "" && !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	sort.Strings(values)
	return &c, values, nil
}
