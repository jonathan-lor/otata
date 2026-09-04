//go:build darwin

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jonathan-lor/otata/internal/atomicfile"
	"github.com/jonathan-lor/otata/internal/cli"
)

// launchLabel is the launchd job label and the plist's basename. Reverse-DNS
// under anakepha.com, which signs releases; macOS keys the Login Items
// disabled state by label, so it must not change after release.
const launchLabel = "com.anakepha.otata"

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func launchDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func launchTarget() string { return launchDomain() + "/" + launchLabel }

// AutostartEnabled reports whether THIS root's server returns at login, which is
// a question about the plist. launchd loads whatever is in LaunchAgents at login
// regardless of what's loaded now. One agent per user, so a plist for
// another root is not this root's autostart.
func (a *App) AutostartEnabled() bool { return a.agentIsOurs() }

// agentIsOurs reports whether the installed agent serves this root and port.
func (a *App) agentIsOurs() bool {
	spec, ok := a.readAgentPlist()
	return ok && agentMatches(spec, a.Root, a.Config.Port)
}

// agentLoaded reports whether launchd currently manages OUR server. While it
// does, KeepAlive respawns anything signaled, so this, not the plist,
// decides how the server must be stopped. Another root's agent is not ours.
func (a *App) agentLoaded() bool { return jobLoaded() && a.agentIsOurs() }

// jobLoaded asks launchd about the label, whoever it belongs to.
func jobLoaded() bool {
	return exec.Command("launchctl", "print", launchTarget()).Run() == nil
}

// agentDisabled reports whether the user has switched the agent off at the
// launchd level: the System Settings Login Items toggle on macOS 13+, or
// `launchctl disable`. In that state bootstrap fails with an error naming
// none of this and RunAtLoad will not fire, so anything about to prescribe a
// reload must ask first.
func agentDisabled() bool {
	out, err := exec.Command("launchctl", "print-disabled", launchDomain()).Output()
	if err != nil {
		return false
	}
	return disabledIn(string(out), launchLabel)
}

// errAgentDisabled is the refusal for a disabled agent, worded once for everything that reports the state
func errAgentDisabled() *cli.Failure {
	return cli.Fail(cli.CodeServerDown, "the launch agent is disabled, so macOS will not load it now or at login").
		WithHint("enable otata in System Settings > General > Login Items, or run 'launchctl enable " + launchTarget() + "', then retry")
}

// foreignAgentLoaded reports a loaded otata launch agent that is not this
// root's, with what its plist says it serves. One label exists per user, so a
// loaded job that is not ours is exactly one other root's agent.
func (a *App) foreignAgentLoaded() (agentSpec, bool) {
	if !jobLoaded() || a.agentIsOurs() {
		return agentSpec{}, false
	}
	return a.readAgentPlist()
}

// readAgentPlist is the installed plist, read once per process. The paths
// that install or remove it forget the answer.
func (a *App) readAgentPlist() (agentSpec, bool) {
	if !a.agentRead {
		a.agent, a.agentOK = parseAgentPlist()
		a.agentRead = true
	}
	return a.agent, a.agentOK
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
	}
	if json.Unmarshal(out, &plist) != nil || len(plist.ProgramArguments) == 0 {
		return agentSpec{}, false
	}
	spec := agentSpec{Program: plist.ProgramArguments[0], Root: plist.EnvironmentVariables["OTATA_ROOT"]}
	spec.Port, _ = strconv.Atoi(plist.EnvironmentVariables["OTATA_PORT"])
	return spec, true
}

// bootoutAgent unloads the job. This is how you stop a KeepAlive server because
// signaling it only makes launchd respawn it. It is called only from paths
// that have already established the job is ours, or are about to replace it.
func bootoutAgent() error {
	if !jobLoaded() {
		return nil // not loaded
	}
	if out, err := exec.Command("launchctl", "bootout", launchTarget()).CombinedOutput(); err != nil {
		return cli.Failf(cli.CodeInternal, "could not unload the launch agent: %s", string(out))
	}
	return nil
}

// startHint runs when the server is down and no agent is installed.
const startHint = "run 'otata autostart on' once so the server runs under launchd, or 'otata serve' in a foreground terminal"

// reloadAgent brings this root's installed agent to a bound server.
// It re-bootstraps a plist that isn't loaded which is the state `otata stop` leaves.
// Only StartServer calls it, after establishing the agent is ours.
func (a *App) reloadAgent() error {
	if agentDisabled() {
		// Bootstrap would fail with launchctl's own words, which name neither
		// the cause nor the Settings toggle that flips it back.
		return errAgentDisabled()
	}
	if jobLoaded() {
		// Installed AND loaded, yet nothing bound: the agent's process is hung
		// (a TCC-protected binary) or crash-looping. Nothing here can mend
		// that, so we just report it
		return cli.Failf(cli.CodeServerDown,
			"the launch agent is loaded but nothing has bound port %d", a.Config.Port).
			WithHint("see " + a.Store.ServerLog() + "; re-run 'otata autostart on' to reinstall the agent")
	}
	if out, err := exec.Command("launchctl", "bootstrap", launchDomain(), launchAgentPath()).CombinedOutput(); err != nil {
		return cli.Failf(cli.CodeInternal, "could not reload the launch agent: %s", string(out))
	}
	for range 50 {
		if a.ServerRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cli.Failf(cli.CodeServerDown, "reloaded the launch agent but nothing bound port %d", a.Config.Port).
		WithHint("see " + a.Store.ServerLog())
}

// EnableAutostart makes the server return at login, but not before it.
// With, FileVault the volume is encrypted until someone unlocks the machine, and
// nothing runs at any launchd level until then.
//
// The agent points at the invoked path, symlinks unresolved: under a package
// manager the symlink is the stable name (/opt/homebrew/bin/otata) and its
// target is a versioned path the next upgrade deletes, so resolving here
// embedded a program launchd could not find after the first `brew upgrade`. It
// falls back to a staged copy only on evidence.
func (a *App) EnableAutostart() error {
	exe, err := os.Executable()
	if err != nil {
		return cli.Failf(cli.CodeInternal, "could not locate the otata binary: %v", err)
	}

	// One agent per user. Installing over one that serves a different root
	// would silently take autostart away from that root: the real store,
	// typically, from a shell with a scratch OTATA_ROOT set.
	if spec, ok := a.readAgentPlist(); ok && !agentMatches(spec, a.Root, a.Config.Port) {
		return cli.Failf(cli.CodeInvalidArgs,
			"the launch agent already serves %s; there is one per user", describeAgent(spec)).
			WithHint("run 'otata autostart off' to remove it, then 'otata autostart on' here")
	}

	err = a.installAgent(exe, 4*time.Second)
	if err == nil {
		return nil
	}
	// Only "loaded, but nothing bound" is evidence for the staged-copy fallback
	// below. Any other failure (a foreign process on the port, a plist launchctl
	// rejects) is returned as itself. It used to fall through regardless and
	// blame the binary's location for a problem that had nothing to do with it.
	if _, ok := errors.AsType[*errAgentNoBind](err); !ok {
		return err
	}

	// It did not come up. The usual cause is a binary launchd cannot read: macOS
	// protects ~/Documents, ~/Desktop and ~/Downloads with TCC, and this does NOT
	// fail cleanly: the process starts and hangs in dyld loading its own image
	// while launchd reports the job running. Testing beats guessing from the
	// path because TCC also covers iCloud Drive, external volumes, and per-user grants.
	staged, stageErr := stageAgentBinary(a.Store.StagedBinary(), exe)
	if stageErr != nil {
		_ = a.DisableAutostart()
		return stageErr
	}
	err = a.installAgent(staged, 6*time.Second)
	if err == nil {
		return nil
	}
	_ = a.DisableAutostart()
	if _, ok := errors.AsType[*errAgentNoBind](err); ok {
		return cli.Failf(cli.CodeServerDown,
			"the launch agent will not start the server, from %s or from a copy", exe).
			WithHint("see " + a.Store.ServerLog())
	}
	return err
}

// errAgentNoBind is the one installAgent failure that says nothing about why:
// launchd accepted the job and reports it running, but no server bound the port.
// Its own type, so EnableAutostart can tell it from failures that do say why.
type errAgentNoBind struct{ port int }

func (e *errAgentNoBind) Error() string {
	return fmt.Sprintf("agent loaded but nothing bound port %d", e.port)
}

// installAgent loads a plist and waits for the server to actually bind. Binding
// is the only honest success signal because launchd reports a job that hangs on load as
// "running", so asking launchd proves nothing.
func (a *App) installAgent(program string, wait time.Duration) error {
	// Whatever happens below writes or removes the plist.
	defer a.forgetAgentPlist()
	// Unload BEFORE stopping anything. With KeepAlive set, a signaled server is
	// respawned faster than it can be confirmed dead, so stopping first can
	// never succeed and the bootout that would have fixed it never runs.
	if err := bootoutAgent(); err != nil {
		return err
	}
	if err := a.StopServer(); err != nil {
		return err
	}

	plist := launchPlist(program, a.Root, a.Config.Port, a.Config.ServePath, a.Store.ServerLog())
	// Atomic because with a torn plist, launchd will retry the corrupt file
	// at every login while otata, unable to parse it, reports autostart off.
	if err := atomicfile.WriteData(filepath.Dir(launchAgentPath()), launchAgentPath(), 0o644, plist); err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	/*
		`autostart on` asks for autostart as explicitly as the Login Items
		toggle switched it off, so the disable flag is cleared here, on this
		path alone. The repair paths (reloadAgent) still refuse a disabled agent
		instead of overriding the toggle. A disabled service cannot be
		bootstrapped, and the failure is launchctl's catch-all "Input/output
		error", which doesn't name the cause or the fix.
	*/
	_ = exec.Command("launchctl", "enable", launchTarget()).Run()
	if out, err := exec.Command("launchctl", "bootstrap", launchDomain(), launchAgentPath()).CombinedOutput(); err != nil {
		_ = os.Remove(launchAgentPath())
		if agentDisabled() {
			// The enable did not stick, so something beyond otata (an MDM policy) holds the switch
			return errAgentDisabled()
		}
		return cli.Failf(cli.CodeInternal, "could not load the launch agent: %s", string(out))
	}

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if a.ServerRunning() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Leave nothing behind because an orphaned plist would otherwise be loaded at
	// next login and reported as autostart on, for a server that never binds.
	_ = exec.Command("launchctl", "bootout", launchTarget()).Run()
	_ = os.Remove(launchAgentPath())
	return &errAgentNoBind{port: a.Config.Port}
}

// launchPlist builds the agent definition with real XML escaping because a home
// directory or install path containing & or < would otherwise produce a plist
// launchctl refuses, which gets misreported as a TCC problem.
//
// Root, port and serve path are all embedded, so the agent serves exactly what the
// command that installed it saw.
//
// KeepAlive restarts every death except a deliberate exit 0. The plain true
// restarts even those, so the server could never exit on purpose and stay
// down; launchd would resurrect it every ThrottleInterval, forever.
func launchPlist(program, root string, port int, servePath, log string) []byte {
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
`, launchLabel, xmlText(program), xmlText(root), port, xmlText(servePath), xmlText(log), xmlText(log))
}

func (a *App) DisableAutostart() error {
	defer a.forgetAgentPlist()
	_ = exec.Command("launchctl", "bootout", launchTarget()).Run()
	if err := os.Remove(launchAgentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	return nil
}

// stageAgentBinary copies the binary to dest, the store's own place for it,
// which is never inside a protected directory. Staged and renamed so a
// running agent never reads a partially written image.
func stageAgentBinary(dest, exe string) (string, error) {
	err := atomicfile.Write(filepath.Dir(dest), dest, 0o755, func(w io.Writer) error {
		src, err := os.Open(exe)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err != nil {
		return "", cli.Failf(cli.CodeInternal, "could not stage %s: %v", exe, err)
	}
	// The copy carries its source's modification time, so the drift check
	// can tell them apart, or not, by size and time alone, without hashing
	// two binaries on every status.
	if info, err := os.Stat(exe); err == nil {
		_ = os.Chtimes(dest, info.ModTime(), info.ModTime())
	}
	return dest, nil
}

// AutostartProgram reports what the installed agent runs, and whether it has
// drifted from the binary in use. Different spellings can still be one file
// (a symlink beside its target), and a symlink's target moves under it on
// upgrade, so content decides, as cheaply as it can be settled.
func (a *App) AutostartProgram() (program string, stale bool) {
	spec, ok := a.readAgentPlist()
	if !ok {
		return "", false
	}
	program = spec.Program
	exe, err := os.Executable()
	if err != nil {
		return program, false
	}
	return program, filesDiffer(program, exe)
}
