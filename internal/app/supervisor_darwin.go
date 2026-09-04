//go:build darwin

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jonathan-lor/otata/internal/atomicfile"
)

// launchLabel is the launchd job label and the plist's basename. Reverse-DNS
// under anakepha.com, (LLC owned by me, Jonathan Lor); macOS keys the Login Items
// disabled state by label, so it must not change after release.
const launchLabel = "com.anakepha.otata"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func launchDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func launchTarget() string { return launchDomain() + "/" + launchLabel }

func newSupervisor() supervisor { return &launchd{} }

// launchd is the macOS supervisor: a LaunchAgent in the user's gui domain,
// which exists only while a user is logged into the console.
type launchd struct {
	// The plist, read once per process: parsing it is a subprocess, and
	// status asked four times. Install and Remove are what change it, and
	// they drop the memo.
	spec     agentSpec
	specOK   bool
	specRead bool
}

func (l *launchd) Available() bool { return true }
func (l *launchd) Kind() string    { return "launch agent" }

func (l *launchd) Installed() (agentSpec, bool) {
	if !l.specRead {
		l.spec, l.specOK = parseAgentPlist()
		l.specRead = true
	}
	return l.spec, l.specOK
}

// parseAgentPlist parses the installed plist with plutil rather than searching
// for literal markup: a plist converted to binary, reformatted, or edited by
// any other tool would otherwise be misread.
func parseAgentPlist() (agentSpec, bool) {
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", launchAgentPath()).Output()
	if err != nil {
		return agentSpec{}, false
	}
	var plist struct {
		ProgramArguments     []string          `json:"ProgramArguments"`
		EnvironmentVariables map[string]string `json:"EnvironmentVariables"`
		StandardOutPath      string            `json:"StandardOutPath"`
	}
	if json.Unmarshal(out, &plist) != nil || len(plist.ProgramArguments) == 0 {
		return agentSpec{}, false
	}
	env := plist.EnvironmentVariables
	spec := agentSpec{Program: plist.ProgramArguments[0], Root: env["OTATA_ROOT"], ServePath: env["OTATA_PATH"], Log: plist.StandardOutPath}
	spec.Port, _ = strconv.Atoi(env["OTATA_PORT"])
	return spec, true
}

// Loaded asks launchd about the label, whoever it belongs to.
func (l *launchd) Loaded() bool {
	return exec.Command("launchctl", "print", launchTarget()).Run() == nil
}

// Disabled reads the state the System Settings Login Items toggle (macOS 13+)
// and `launchctl disable` record. In that state bootstrap fails with an error
// naming none of this and RunAtLoad will not fire.
func (l *launchd) Disabled() bool {
	out, err := exec.Command("launchctl", "print-disabled", launchDomain()).Output()
	if err != nil {
		return false
	}
	return disabledIn(string(out), launchLabel)
}

/*
disabledIn reads `launchctl print-disabled` output and reports whether
label is disabled there. It's the state the System Settings Login Items toggle
and `launchctl disable` record.

It's split from the fetching just so the parse is testable.
Lines read `"label" => disabled` on current macOS and `"label" => true` on older ones.
Both mean disabled, and a label absent from the list is enabled.
*/
func disabledIn(out, label string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, `"`+label+`"`)
		if !found {
			continue
		}
		return strings.Contains(rest, "disabled") || strings.Contains(rest, "true")
	}
	return false
}

func (l *launchd) Enable() {
	_ = exec.Command("launchctl", "enable", launchTarget()).Run()
}

// Install writes the plist atomically: with a torn one, launchd would retry
// the corrupt file at every login while otata, unable to parse it, reported
// autostart off.
func (l *launchd) Install(spec agentSpec) error {
	l.specRead = false
	return atomicfile.WriteData(filepath.Dir(launchAgentPath()), launchAgentPath(), 0o644, launchPlist(spec))
}

func (l *launchd) Load() error {
	out, err := exec.Command("launchctl", "bootstrap", launchDomain(), launchAgentPath()).CombinedOutput()
	if err != nil {
		return launchctlError(out, err)
	}
	return nil
}

// Unload boots the job out. This is how a KeepAlive server is stopped,
// because signaling it only makes launchd respawn it.
func (l *launchd) Unload() error {
	if !l.Loaded() {
		return nil
	}
	out, err := exec.Command("launchctl", "bootout", launchTarget()).CombinedOutput()
	if err != nil {
		return launchctlError(out, err)
	}
	return nil
}

func (l *launchd) Remove() error {
	l.specRead = false
	_ = exec.Command("launchctl", "bootout", launchTarget()).Run()
	if err := os.Remove(launchAgentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l *launchd) StartHint() string {
	return "run 'otata autostart on' once so the server runs under launchd, or 'otata serve' in a foreground terminal"
}

func (l *launchd) DisabledHint() string {
	return "enable otata in System Settings > General > Login Items, or run 'launchctl enable " + launchTarget() + "'"
}

// launchctlError is what launchctl printed, or the exec error when it
// printed nothing.
func launchctlError(out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

// launchPlist builds the agent definition with real XML escaping because a home
// directory or install path containing & or < would otherwise produce a plist
// launchctl refuses, which gets misreported as a TCC problem.
//
// KeepAlive restarts every death except a deliberate exit 0. The plain true
// restarts even those, so the server could never exit on purpose and stay
// down; launchd would resurrect it every ThrottleInterval, forever.
func launchPlist(spec agentSpec) []byte {
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key>
    <array><string>%s</string><string>serve</string></array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>OTATA_ROOT</key><string>%s</string>
        <key>OTATA_PORT</key><string>%d</string>
        <key>OTATA_PATH</key><string>%s</string>
    </dict>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key>
    <dict><key>SuccessfulExit</key><false/></dict>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, xmlText(spec.Program), xmlText(spec.Root), spec.Port, xmlText(spec.ServePath), xmlText(spec.Log), xmlText(spec.Log))
}
