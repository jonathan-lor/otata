//go:build darwin

package app

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

// The plist embeds everything the agent needs to serve what the installing
// command saw, and survives values launchctl would otherwise choke on.
func TestLaunchPlistEmbedsRootPortAndPath(t *testing.T) {
	raw := launchPlist(agentSpec{Program: "/Users/a & b/bin/otata", Root: "/Users/a & b/.otata", Port: 9123, ServePath: "/builds<1>", Log: "/tmp/server.log"})
	cmd := exec.Command("plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("plutil rejected the plist: %v\n%s", err, raw)
	}
	var plist struct {
		ProgramArguments     []string          `json:"ProgramArguments"`
		EnvironmentVariables map[string]string `json:"EnvironmentVariables"`
		KeepAlive            struct {
			SuccessfulExit *bool `json:"SuccessfulExit"`
		} `json:"KeepAlive"`
	}
	if err := json.Unmarshal(out, &plist); err != nil {
		t.Fatal(err)
	}
	env := plist.EnvironmentVariables
	if env["OTATA_ROOT"] != "/Users/a & b/.otata" || env["OTATA_PORT"] != "9123" || env["OTATA_PATH"] != "/builds<1>" {
		t.Errorf("environment = %v", env)
	}
	if len(plist.ProgramArguments) != 2 || plist.ProgramArguments[0] != "/Users/a & b/bin/otata" || plist.ProgramArguments[1] != "serve" {
		t.Errorf("ProgramArguments = %v", plist.ProgramArguments)
	}
	// The dict form, not the plain true. A deliberate exit 0 must stay down.
	if plist.KeepAlive.SuccessfulExit == nil || *plist.KeepAlive.SuccessfulExit {
		t.Error("KeepAlive.SuccessfulExit is not false")
	}
}

// disabledIn parses `launchctl print-disabled` output, whose lines this is
// copied from a real machine: `=> disabled`/`=> enabled` on current macOS,
// with `=> true` the older spelling of disabled. Reading it wrong either
// hides a Login Items toggle-off or reports every agent as disabled.
func TestDisabledIn(t *testing.T) {
	const current = `	disabled services = {
		"com.apple.Siri.agent" => disabled
		"com.ollama.ollama" => enabled
		"com.anakepha.otata" => disabled
	}`
	const enabled = `	disabled services = {
		"com.anakepha.otata" => enabled
		"com.anakepha.otata2" => disabled
	}`
	const legacy = `	disabled services = {
		"com.anakepha.otata" => true
		"com.example.other" => false
	}`
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"disabled", current, true},
		{"enabled entry is not disabled", enabled, false},
		{"legacy true means disabled", legacy, true},
		{"absent means enabled", `	disabled services = {
		"com.example.other" => disabled
	}`, false},
		{"empty output", "", false},
	}
	for _, c := range cases {
		if got := disabledIn(c.out, "com.anakepha.otata"); got != c.want {
			t.Errorf("%s: disabledIn = %v, want %v", c.name, got, c.want)
		}
	}
	// A label that merely prefixes another must not borrow its state.
	if disabledIn(enabled, "com.anakepha.otata2") != true {
		t.Error("the longer label's own state was not read")
	}
}
