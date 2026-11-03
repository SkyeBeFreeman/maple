// Package agentconf edits the MCP sections of the agents' own config files.
// It is the only package that writes those files, and it edits them in place:
// only the named entry under the MCP section changes, every other byte stays.
package agentconf

import "github.com/SkyeBeFreeman/maple/internal/config"

// Entry is an entry maple writes. A non-empty URL makes it an HTTP entry,
// otherwise it is a stdio entry.
type Entry struct {
	Name    string
	URL     string
	Headers map[string]string
	Command string
	Args    []string
}

// Foreign is a neutral view of an existing entry in an agent config, used by
// import. Transport is "" for stdio, otherwise config.TransportHTTP or
// config.TransportSSE.
type Foreign struct {
	Name      string
	URL       string
	Transport string
	Headers   map[string]string
	Command   string
	Args      []string
	Env       map[string]string
}

// Agent edits one agent's config file content. Methods work on bytes so the
// caller owns locking, mtime checks and atomic writes.
type Agent interface {
	ID() string
	Path(p config.Paths) string
	Set(data []byte, e Entry) ([]byte, error)
	Remove(data []byte, name string) ([]byte, error)
	Canonical(data []byte, name string) (canon []byte, ok bool, err error)
	List(data []byte) ([]Foreign, error)
}

// All returns every supported agent in display order.
func All() []Agent {
	return []Agent{Claude, Codex, CodeBuddy}
}
