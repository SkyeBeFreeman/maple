package config

import "go.yaml.in/yaml/v3"

// PeekPort returns the top-level port of a config.yaml that may otherwise be
// invalid, so the gateway can still listen and report the error in /status.
// It falls back to DefaultPort when the port is absent or unusable.
func PeekPort(data []byte) int {
	var doc struct {
		Port int `yaml:"port"`
	}
	// The other fields are ignored on purpose: only the port must be read.
	if err := yaml.Unmarshal(data, &doc); err != nil || !validPort(doc.Port) {
		return DefaultPort
	}
	return doc.Port
}
