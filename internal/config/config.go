package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultPort is used when config.yaml has no port.
const DefaultPort = 47321

// GatewayName is the gateway's own entry name in the agents' configs; a
// direct server with this name would collide with it.
const GatewayName = "maple"

// EffectivePort is port with its default applied.
func EffectivePort(port int) int {
	if port == 0 {
		return DefaultPort
	}
	return port
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

// LoopbackAddr is where the gateway listens. Clients send it as the Host
// too, since the gateway rejects foreign Host values.
func LoopbackAddr(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

// Mode and transport values accepted in config.yaml.
const (
	ModeShared     = "shared"
	ModePerSession = "per-session"
	ModeDirect     = "direct"

	TransportHTTP = "http"
	TransportSSE  = "sse"
)

// Config is the content of config.yaml.
type Config struct {
	Port    int                `yaml:"port"`
	Servers map[string]*Server `yaml:"servers"`
}

// Server is one upstream MCP server. Exactly one of URL (remote) and Command
// (stdio) is set. The json tags only serve Hash and --json output.
type Server struct {
	URL           string            `yaml:"url,omitempty" json:"url,omitempty"`
	Transport     string            `yaml:"transport,omitempty" json:"transport,omitempty"`
	Headers       map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Command       string            `yaml:"command,omitempty" json:"command,omitempty"`
	Args          []string          `yaml:"args,omitempty" json:"args,omitempty"`
	Env           map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Mode          string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	DisabledTools []string          `yaml:"disabled_tools,omitempty" json:"disabled_tools,omitempty"`
	Description   string            `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled       *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IsEnabled reports whether the server is enabled; unset means enabled.
func (s *Server) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// IsStdio reports whether the server is a local process rather than remote.
func (s *Server) IsStdio() bool { return s.Command != "" }

// IsDirect reports whether the agents run the server themselves through
// maple run instead of the gateway serving it.
func (s *Server) IsDirect() bool { return s.IsStdio() && s.EffectiveMode() == ModeDirect }

// EffectiveMode is the stdio process mode with its default applied; remote
// servers have no mode.
func (s *Server) EffectiveMode() string {
	if !s.IsStdio() {
		return ""
	}
	if s.Mode == "" {
		return ModeShared
	}
	return s.Mode
}

// EffectiveTransport is the remote transport with its default applied; stdio
// servers have no transport.
func (s *Server) EffectiveTransport() string {
	if s.IsStdio() {
		return ""
	}
	if s.Transport == "" {
		return TransportHTTP
	}
	return s.Transport
}

// Hash identifies the server's configuration, e.g. as a cache key. It hashes
// JSON rather than YAML because encoding/json sorts map keys, so the result
// does not depend on how the file was written.
func (s *Server) Hash() string {
	b, err := json.Marshal(s)
	if err != nil {
		// Only strings, slices, maps of strings and a bool: cannot fail.
		panic(err)
	}
	return ContentHash(b)
}

// ConnHash is Hash without the fields that do not change how the upstream is
// reached, so editing them neither restarts it nor drops its cached tools.
func (s *Server) ConnHash() string {
	c := *s
	c.Description, c.DisabledTools, c.Enabled = "", nil, nil
	return c.Hash()
}

// SecretRefs lists the secret names referenced from the expansion sites,
// deduplicated and sorted. Malformed references are skipped; Validate reports
// them.
func (s *Server) SecretRefs() []string {
	seen := map[string]bool{}
	for _, v := range s.expansionSites() {
		refs, _ := Refs(v)
		for _, r := range refs {
			seen[r] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

func (s *Server) expansionSites() []string {
	sites := []string{s.URL}
	sites = append(sites, s.Args...)
	for _, v := range s.Headers {
		sites = append(sites, v)
	}
	for _, v := range s.Env {
		sites = append(sites, v)
	}
	return sites
}

// ContentHash is the "sha256:<hex>" digest used wherever maple compares
// content, such as the gateway's config_hash and the CLI's reload check.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Parse decodes and validates config.yaml. Every error it returns is a
// *ValidationError carrying the offending line.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, yamlError(err, nil)
	}
	idx := newNodeIndex(&root)

	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, yamlError(err, idx)
	}
	if idx.port == nil {
		c.Port = DefaultPort
	}
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	if err := validate(&c, idx); err != nil {
		return nil, err
	}
	return &c, nil
}

var (
	yamlLineRe     = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)
	unknownFieldRe = regexp.MustCompile(`^field (.+) not found in type \S+$`)
)

// yamlError turns yaml.v3's errors, which embed "line N:" in their text, into
// ValidationErrors so callers see one format. Only the first is kept.
func yamlError(err error, idx *nodeIndex) error {
	msg := err.Error()
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		msg = te.Errors[0]
	}
	m := yamlLineRe.FindStringSubmatch(msg)
	if m == nil {
		return &ValidationError{Msg: strings.TrimPrefix(msg, "yaml: ")}
	}
	line, _ := strconv.Atoi(m[1])
	msg = m[2]
	if f := unknownFieldRe.FindStringSubmatch(msg); f != nil {
		msg = fmt.Sprintf("unknown field %q", f[1])
	}
	return &ValidationError{Line: line, Server: idx.serverAt(line), Msg: msg}
}

// nodeIndex locates YAML nodes so validation errors can name a line. A nil
// *nodeIndex (Config built in code) yields line 0 everywhere.
type nodeIndex struct {
	port    *yaml.Node
	servers map[string]serverNodes
}

type serverNodes struct {
	key, value *yaml.Node
}

func newNodeIndex(root *yaml.Node) *nodeIndex {
	idx := &nodeIndex{servers: map[string]serverNodes{}}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return idx
	}
	top := root.Content[0]
	idx.port = mappingValue(top, "port")
	if srv := mappingValue(top, "servers"); srv != nil && srv.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(srv.Content); i += 2 {
			k := srv.Content[i]
			if _, dup := idx.servers[k.Value]; !dup {
				idx.servers[k.Value] = serverNodes{key: k, value: srv.Content[i+1]}
			}
		}
	}
	return idx
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// serverAt names the server whose subtree has a node on line. yaml.v3
// reports the line of a node (the key for unknown fields, the value for type
// mismatches), so an exact match is enough.
func (idx *nodeIndex) serverAt(line int) string {
	if idx == nil {
		return ""
	}
	for name, sn := range idx.servers {
		if hasLine(sn.key, line) || hasLine(sn.value, line) {
			return name
		}
	}
	return ""
}

func hasLine(n *yaml.Node, line int) bool {
	if n.Line == line {
		return true
	}
	for _, c := range n.Content {
		if hasLine(c, line) {
			return true
		}
	}
	return false
}

func (idx *nodeIndex) portLine() int {
	if idx == nil || idx.port == nil {
		return 0
	}
	return idx.port.Line
}

func (idx *nodeIndex) serverLine(name string) int {
	if idx == nil {
		return 0
	}
	if sn, ok := idx.servers[name]; ok {
		return sn.key.Line
	}
	return 0
}

// fieldLine is the line of servers.<name>.<field>, falling back to the server
// key when the field is absent.
func (idx *nodeIndex) fieldLine(name, field string) int {
	if n := idx.field(name, field); n != nil {
		return n.Line
	}
	return idx.serverLine(name)
}

func (idx *nodeIndex) field(name, field string) *yaml.Node {
	if idx == nil {
		return nil
	}
	return mappingValue(idx.servers[name].value, field)
}

// mapEntryLine is the line of the value of servers.<name>.<field>.<key>.
func (idx *nodeIndex) mapEntryLine(name, field, key string) int {
	if n := mappingValue(idx.field(name, field), key); n != nil {
		return n.Line
	}
	return idx.fieldLine(name, field)
}

// itemLine is the line of servers.<name>.<field>[i].
func (idx *nodeIndex) itemLine(name, field string, i int) int {
	if n := idx.field(name, field); n != nil && n.Kind == yaml.SequenceNode && i < len(n.Content) {
		return n.Content[i].Line
	}
	return idx.fieldLine(name, field)
}
