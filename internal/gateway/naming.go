// Package gateway holds the MCP gateway server and the pure pieces it is
// assembled from.
package gateway

import (
	"strconv"
	"strings"
)

// maxToolName is the longest tool name agents accept.
const maxToolName = 64

// ToolName returns the exposed name of an upstream tool, unique within taken,
// and records it there; a nil taken checks nothing.
func ToolName(server, tool string, taken map[string]bool) string {
	// After sanitizing the name is pure ASCII, so byte cuts are rune-safe.
	base := sanitize(server) + "__" + sanitize(tool)
	if len(base) > maxToolName {
		base = base[:maxToolName]
	}
	name := base
	for i := 2; taken[name]; i++ {
		suffix := "_" + strconv.Itoa(i)
		b := base
		if len(b) > maxToolName-len(suffix) {
			b = b[:maxToolName-len(suffix)]
		}
		name = b + suffix
	}
	if taken != nil {
		taken[name] = true
	}
	return name
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, s)
}
