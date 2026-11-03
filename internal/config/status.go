package config

// Server states reported by the gateway.
const (
	StateRunning = "running"
	StateIdle    = "idle"
	StateStopped = "stopped"
	StateError   = "error"
)

// Status is the body of the gateway's GET /status.
type Status struct {
	Version     string         `json:"version"`
	ConfigHash  string         `json:"config_hash"`
	ReloadError string         `json:"reload_error"`
	Servers     []ServerStatus `json:"servers"`
}

// ServerStatus is one upstream server in Status.
type ServerStatus struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	PID       int    `json:"pid"`
	Tools     int    `json:"tools"`
	LastError string `json:"last_error"`
}
