// Package launchd writes the LaunchAgent that keeps `maple serve` running and
// loads or unloads it in the user's gui domain with launchctl.
package launchd

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Label identifies the agent in launchd and names its plist file.
const Label = "io.github.skyebefreeman.maple"

// PlistPath returns where the agent's plist lives under home.
func PlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

const plistFormat = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`

// Plist renders the agent that runs `binary serve` at login, restarts it
// whenever it exits, and sends both output streams to logFile.
func Plist(binary, logFile string) []byte {
	log := escape(logFile)
	return fmt.Appendf(nil, plistFormat, escape(Label), escape(binary), log, log)
}

func escape(s string) string {
	var b bytes.Buffer
	// Writes to a bytes.Buffer cannot fail.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// runLaunchctl is a variable so tests can check argv without touching launchd.
// The absolute path keeps a PATH entry from shadowing the system launchctl.
var runLaunchctl = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
}

func guiDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func serviceTarget() string {
	return guiDomain() + "/" + Label
}

// Load (re)starts the agent from plistPath. Booting out first makes Load
// idempotent and picks up a changed plist, since bootstrap refuses a label
// that is already loaded.
func Load(ctx context.Context, plistPath string) error {
	if err := Unload(ctx); err != nil {
		return err
	}
	return launchctl(ctx, "bootstrap", guiDomain(), plistPath)
}

// Unload stops the agent and removes it from the gui domain. An agent that is
// not loaded counts as success.
func Unload(ctx context.Context) error {
	args := []string{"bootout", serviceTarget()}
	out, err := runLaunchctl(ctx, args...)
	if err == nil || notLoaded(out, err) {
		return nil
	}
	return launchctlError(args, out, err)
}

// Loaded reports whether launchd knows the agent, running or not.
func Loaded(ctx context.Context) (bool, error) {
	args := []string{"print", serviceTarget()}
	out, err := runLaunchctl(ctx, args...)
	if err == nil {
		return true, nil
	}
	// A negative code means launchctl was killed (e.g. ctx expired), which
	// says nothing about the agent.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return false, nil
	}
	return false, launchctlError(args, out, err)
}

// notLoaded recognizes launchctl's "service not found" answers. bootout
// reports ESRCH as exit status 3 on current macOS, while older releases and
// other subcommands only say so in the message.
func notLoaded(out []byte, err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return true
	}
	return bytes.Contains(out, []byte("No such process")) ||
		bytes.Contains(out, []byte("Could not find service"))
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := runLaunchctl(ctx, args...)
	if err != nil {
		return launchctlError(args, out, err)
	}
	return nil
}

func launchctlError(args []string, out []byte, err error) error {
	cmd := "launchctl " + strings.Join(args, " ")
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %w: %s", cmd, err, msg)
	}
	return fmt.Errorf("%s: %w", cmd, err)
}
