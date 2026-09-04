package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jonathan-lor/otata/internal/atomicfile"
	"github.com/jonathan-lor/otata/internal/cli"
)

// AutostartEnabled reports whether THIS root's server returns at login, which
// is a question about the installed unit: the manager loads whatever is
// installed at login regardless of what is loaded now. One unit per user, so
// a unit for another root is not this root's autostart.
func (a *App) AutostartEnabled() bool { return a.agentIsOurs() }

// agentIsOurs reports whether the installed unit serves this root and port.
func (a *App) agentIsOurs() bool {
	spec, ok := a.autostart().Installed()
	return ok && agentMatches(spec, a.Root, a.Config.Port)
}

// agentLoaded reports whether the manager currently manages OUR server. While
// it does, it respawns anything signaled, so this, not the definition, decides
// how the server must be stopped. Another root's unit is not ours.
func (a *App) agentLoaded() bool { return a.autostart().Loaded() && a.agentIsOurs() }

// foreignAgentLoaded reports a loaded unit that is not this root's, with what
// it says it serves. One unit exists per user, so a loaded one that is not
// ours is exactly one other root's.
func (a *App) foreignAgentLoaded() (agentSpec, bool) {
	if !a.autostart().Loaded() || a.agentIsOurs() {
		return agentSpec{}, false
	}
	return a.autostart().Installed()
}

// errAgentDisabled is the refusal for a unit the user switched off at the
// manager's own level, worded once for everything that reports the state.
// otata does not override that switch outside `autostart on`, whether or not
// the manager would let it: launchd refuses to load a disabled unit, systemd
// would start one now and leave it behind at next login.
func (a *App) errAgentDisabled() *cli.Failure {
	sup := a.autostart()
	return cli.Failf(cli.CodeServerDown, "the %s is disabled, so it will not return at login", sup.Kind()).
		WithHint(sup.DisabledHint() + ", then retry")
}

// defaultBindWait is how long a freshly loaded unit gets to bind the port
// before it is judged not to have started. The staged-copy retry gets half
// again as long, and a reload a little more.
const defaultBindWait = 4 * time.Second

// waitForBind polls until this root's server holds the port, or the wait is
// up. Binding is the only honest success signal: a manager reports a unit
// that hangs on load as running, so asking it proves nothing.
func (a *App) waitForBind(wait time.Duration) bool {
	if a.bindWait != 0 {
		wait = a.bindWait
	}
	deadline := time.Now().Add(wait)
	for {
		if a.ServerRunning() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// reloadAgent brings this root's installed unit to a bound server. It reloads
// a unit that isn't loaded, which is the state `otata stop` leaves. Only
// StartServer calls it, after establishing the unit is ours.
func (a *App) reloadAgent() error {
	sup := a.autostart()
	if sup.Disabled() {
		// The user's switch, refused as itself: a manager that refuses to
		// load a disabled unit does so in words that name neither the cause
		// nor the switch, and one that would load it anyway would lose it
		// again at next login.
		return a.errAgentDisabled()
	}
	if sup.Loaded() {
		// Installed AND loaded, yet nothing bound: the unit's process is hung
		// (a binary the manager cannot read) or crash-looping. Nothing here
		// can mend that, so it is reported.
		return cli.Failf(cli.CodeServerDown,
			"the %s is loaded but nothing has bound port %d", sup.Kind(), a.Config.Port).
			WithHint("see " + a.Store.ServerLog() + "; re-run 'otata autostart on' to reinstall it")
	}
	if err := sup.Load(); err != nil {
		return cli.Failf(cli.CodeInternal, "could not reload the %s: %v", sup.Kind(), err)
	}
	if a.waitForBind(5 * time.Second) {
		return nil
	}
	return cli.Failf(cli.CodeServerDown, "reloaded the %s but nothing bound port %d", sup.Kind(), a.Config.Port).
		WithHint("see " + a.Store.ServerLog())
}

// EnableAutostart makes the server return at login, but not before it. With
// FileVault the volume is encrypted until someone unlocks the machine, and
// nothing runs at any level until then.
//
// The unit points at the invoked path, symlinks unresolved: under a package
// manager the symlink is the stable name (/opt/homebrew/bin/otata) and its
// target is a versioned path the next upgrade deletes, so resolving here
// embedded a program the manager could not find after the first `brew
// upgrade`. It falls back to a staged copy only on evidence.
func (a *App) EnableAutostart() error {
	sup := a.autostart()
	if !sup.Available() {
		// No manager to install into: an OS with none wired up, or a Linux
		// shell with no systemd user manager behind it.
		return cli.Fail(cli.CodeInvalidArgs, "no service manager is available here to keep the server alive").
			WithHint(sup.StartHint())
	}
	exe, err := os.Executable()
	if err != nil {
		return cli.Failf(cli.CodeInternal, "could not locate the otata binary: %v", err)
	}

	// One unit per user. Installing over one that serves a different root
	// would silently take autostart away from that root: the real store,
	// typically, from a shell with a scratch OTATA_ROOT set.
	if spec, ok := sup.Installed(); ok && !agentMatches(spec, a.Root, a.Config.Port) {
		return cli.Failf(cli.CodeInvalidArgs,
			"the %s already serves %s; there is one per user", sup.Kind(), describeAgent(spec)).
			WithHint("run 'otata autostart off' to remove it, then 'otata autostart on' here")
	}

	err = a.installAgent(exe, defaultBindWait)
	if err == nil {
		return nil
	}
	// Two failures say the manager cannot run the binary from where it is,
	// and only those are evidence for the staged copy below: the manager
	// refusing the program outright, and a unit that loaded but never bound.
	// Any other failure (a foreign process on the port, a definition the
	// manager rejects) is returned as itself. It used to fall through
	// regardless and blame the binary's location for a problem that had
	// nothing to do with it.
	if !programUnusable(err) {
		return err
	}

	// It did not come up from where it is. Each manager has its own reasons,
	// and none of them apply under the root. macOS protects ~/Documents,
	// ~/Desktop and ~/Downloads with TCC, and this does NOT fail cleanly: the
	// process starts and hangs in dyld loading its own image while launchd
	// reports the job running. Testing beats guessing from the path because
	// TCC also covers iCloud Drive, external volumes, and per-user grants.
	// systemd refuses a path holding a quote or backslash up front, and a
	// noexec mount shows as a unit that never binds.
	staged, stageErr := stageAgentBinary(a.Store.StagedBinary(), exe)
	if stageErr != nil {
		_ = a.DisableAutostart()
		return stageErr
	}
	err = a.installAgent(staged, defaultBindWait*3/2)
	if err == nil {
		return nil
	}
	_ = a.DisableAutostart()
	if prog, ok := errors.AsType[*errAgentProgram](err); ok {
		// The copy sits under the root, so the root's path is the problem.
		return cli.Failf(cli.CodeServerDown,
			"the %s cannot run the server from %s, nor from a copy under %s: %s", sup.Kind(), exe, a.Root, prog.Reason).
			WithHint("install otata, or set OTATA_ROOT, at a path the " + sup.Kind() + " can run from")
	}
	if _, ok := errors.AsType[*errAgentNoBind](err); ok {
		return cli.Failf(cli.CodeServerDown,
			"the %s will not start the server, from %s or from a copy", sup.Kind(), exe).
			WithHint("see " + a.Store.ServerLog())
	}
	return err
}

// programUnusable reports the failures that mean the manager cannot run the
// binary from where it is, said up front or shown by a load that never bound.
func programUnusable(err error) bool {
	_, noBind := errors.AsType[*errAgentNoBind](err)
	_, refused := errors.AsType[*errAgentProgram](err)
	return noBind || refused
}

// errAgentNoBind is the one installAgent failure that says nothing about why:
// the manager accepted the unit and reports it running, but no server bound
// the port. Its own type, so EnableAutostart can tell it from failures that
// do say why.
type errAgentNoBind struct{ port int }

func (e *errAgentNoBind) Error() string {
	return fmt.Sprintf("unit loaded but nothing bound port %d", e.port)
}

// errAgentProgram is a manager's refusal to run the binary from where it is,
// said before anything is loaded: systemd will not exec a path holding a
// quote or backslash, however it is escaped. It is the second road to the
// staged copy. The first, a unit that loads but never binds, is how launchd
// reports the same thing about a TCC-protected path, after the fact.
type errAgentProgram struct {
	Program string
	Reason  string
}

func (e *errAgentProgram) Error() string {
	return fmt.Sprintf("cannot run %s: %s", e.Program, e.Reason)
}

// installAgent installs a unit running program and waits for the server to
// actually bind.
func (a *App) installAgent(program string, wait time.Duration) error {
	sup := a.autostart()
	// Unload BEFORE stopping anything. With the manager keeping the server
	// alive, a signaled server is respawned faster than it can be confirmed
	// dead, so stopping first can never succeed and the unload that would
	// have fixed it never runs.
	if err := sup.Unload(); err != nil {
		return cli.Failf(cli.CodeInternal, "could not unload the %s: %v", sup.Kind(), err)
	}
	if err := a.StopServer(); err != nil {
		return err
	}

	// Root, port and serve path are all embedded, so the unit serves exactly
	// what the command that installed it saw.
	spec := agentSpec{Program: program, Root: a.Root, Port: a.Config.Port, ServePath: a.Config.ServePath, Log: a.Store.ServerLog()}
	if err := sup.Install(spec); err != nil {
		// The manager refusing the program is EnableAutostart's cue to stage
		// a copy, so it travels as itself, and nothing was written. Anything
		// else is reported here, after removing what a half-finished Install
		// left: on systemd the file lands before the manager is told about
		// it, and a file with no login link would read as autostart on.
		if _, ok := errors.AsType[*errAgentProgram](err); ok {
			return err
		}
		_ = sup.Remove()
		return cli.Failf(cli.CodeInternal, "could not install the %s: %v", sup.Kind(), err)
	}
	/*
		`autostart on` asks for autostart as explicitly as the manager's own
		toggle switched it off, so the user's switch is cleared here, on this
		path alone. The repair paths (reloadAgent) still refuse a disabled unit
		instead of overriding the toggle. Cleared before Load because a manager
		that refuses to load a disabled unit does so with its catch-all, which
		names neither the cause nor the fix.
	*/
	sup.Enable()
	if err := sup.Load(); err != nil {
		_ = sup.Remove()
		if sup.Disabled() {
			// The enable did not stick, so something beyond otata (an MDM
			// policy) holds the switch.
			return a.errAgentDisabled()
		}
		return cli.Failf(cli.CodeInternal, "could not load the %s: %v", sup.Kind(), err)
	}
	if a.waitForBind(wait) {
		return nil
	}
	// Leave nothing behind because an orphaned unit would otherwise be loaded
	// at next login and reported as autostart on, for a server that never binds.
	_ = sup.Remove()
	return &errAgentNoBind{port: a.Config.Port}
}

func (a *App) DisableAutostart() error {
	if err := a.autostart().Remove(); err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	return nil
}

// stageAgentBinary copies the binary to dest, the store's own place for it,
// which is never inside a protected directory. Staged and renamed so a
// running unit never reads a partially written image.
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

// AutostartProgram reports what the installed unit runs, and whether it has
// drifted from the binary in use. Different spellings can still be one file
// (a symlink beside its target), and a symlink's target moves under it on
// upgrade, so content decides, as cheaply as it can be settled.
func (a *App) AutostartProgram() (program string, stale bool) {
	spec, ok := a.autostart().Installed()
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
