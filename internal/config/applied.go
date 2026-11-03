package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

const appliedVersion = 1

// Applied records the entries maple wrote into each agent's config and the
// hash of each entry at write time, to detect later hand edits.
type Applied struct {
	Version int                      `json:"version"`
	Agents  map[string]*AgentApplied `json:"agents"`
}

// AgentApplied maps an entry name to its content hash.
type AgentApplied struct {
	Entries map[string]string `json:"entries"`
}

// LoadApplied reads applied.json; a missing file is an empty record.
func LoadApplied(p Paths) (*Applied, error) {
	data, err := os.ReadFile(p.Applied())
	if errors.Is(err, fs.ErrNotExist) {
		return &Applied{Version: appliedVersion, Agents: map[string]*AgentApplied{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var a Applied
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Applied(), err)
	}
	if a.Agents == nil {
		a.Agents = map[string]*AgentApplied{}
	}
	return &a, nil
}

// SaveApplied atomically writes applied.json.
func SaveApplied(p Paths, a *Applied) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureDir(p); err != nil {
		return err
	}
	return atomicfile.WriteFile(p.Applied(), append(data, '\n'), 0o600)
}
