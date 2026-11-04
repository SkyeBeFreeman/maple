package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

func TestServeMissingConfig(t *testing.T) {
	e := manage.Env{Paths: config.Paths{Home: t.TempDir()}, Keychain: keychain.NewMemory()}
	err := serve(context.Background(), e, "test")
	if err == nil || !strings.Contains(err.Error(), "run: maple install") {
		t.Fatalf("serve without config.yaml = %v, want a hint to run maple install", err)
	}
}

// An invalid config.yaml must not stop the gateway (launchd would restart it
// in a loop): it listens on the port the file names and reports the error.
func TestServeInvalidConfigReportsStatus(t *testing.T) {
	e := manage.Env{Paths: config.Paths{Home: t.TempDir()}, Keychain: keychain.NewMemory()}
	port := testutil.FreePort(t)
	if err := os.MkdirAll(e.Paths.MapleDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "port: " + strconv.Itoa(port) + "\nservers:\n  bad name!:\n    url: http://x\n"
	if err := os.WriteFile(e.Paths.Config(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.Paths.Token(), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, e, "test") }()
	returned := false
	defer func() {
		cancel()
		if returned {
			return
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("serve did not return")
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			returned = true
			t.Fatalf("serve returned early: %v", err)
		default:
		}
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/status", nil)
		req.Header.Set("Authorization", "Bearer tok")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			var st config.Status
			derr := json.NewDecoder(resp.Body).Decode(&st)
			_ = resp.Body.Close()
			if derr == nil && strings.Contains(st.ReloadError, "bad name!") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("/status never reported the config error")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
