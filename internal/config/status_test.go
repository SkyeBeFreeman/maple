package config

import (
	"encoding/json"
	"testing"
)

func TestStatusJSON(t *testing.T) {
	s := Status{
		Version: "v1", ConfigHash: "sha256:x", ReloadError: "bad",
		Servers: []ServerStatus{{Name: "a", State: StateRunning, PID: 7, Tools: 3, LastError: "e"}},
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":"v1","config_hash":"sha256:x","reload_error":"bad","servers":[{"name":"a","state":"running","pid":7,"tools":3,"last_error":"e"}]}`
	if string(raw) != want {
		t.Errorf("json = %s\nwant   %s", raw, want)
	}
	var back Status
	if err := json.Unmarshal(raw, &back); err != nil || back.Servers[0].PID != 7 {
		t.Errorf("unmarshal = %+v, %v", back, err)
	}
}
