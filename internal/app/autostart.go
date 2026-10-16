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

// AutostartEnabled reports whether this root's unit is installed, so the
// server returns at login.
func (a *App) AutostartEnabled() bool { return a.agentIsOurs() }

// agentIsOurs reports whether the installed unit serves this root and port.
func (a *App) agentIsOurs() bool {
	spec, ok := a.autostart().Installed()
	return ok && agentMatches(spec, a.Root, a.Config.Port)
}

// agentLoaded reports whether the manager is running our unit. While it is,
// a signaled server just respawns.
func (a *App) agentLoaded() bool { return a.autostart().Loaded() && a.agentIsOurs() }

// foreignAgentLoaded returns the loaded unit if it serves another root.
func (a *App) foreignAgentLoaded() (agentSpec, bool) {
	if !a.autostart().Loaded() || a.agentIsOurs() {
		return agentSpec{}, false
	}
	return a.autostart().Installed()
}

// errAgentDisabled is the refusal for a unit the user disabled in the
// manager. Only `autostart on` overrides that.
func (a *App) errAgentDisabled() *cli.Failure {
	sup := a.autostart()
	return cli.Failf(cli.CodeServerDown, "the %s is disabled, so it will not return at login", sup.Kind()).
		WithHint(sup.DisabledHint() + ", then retry")
}

// defaultBindWait is how long a freshly loaded unit gets to bind the port.
const defaultBindWait = 4 * time.Second

// waitForBind waits for this root's server to hold the port. A hung unit
// still reports as running, so binding is the only real signal.
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

// reloadAgent loads our installed unit, as `otata stop` leaves it, and waits
// for it to bind.
func (a *App) reloadAgent() error {
	sup := a.autostart()
	if sup.Disabled() {
		return a.errAgentDisabled()
	}
	if sup.Loaded() {
		// Loaded but not bound: hung or crash-looping, which a reload can't fix.
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

// EnableAutostart installs a unit that runs the server at login. The unit
// runs the invoked path without resolving symlinks, since a package
// manager's symlink outlives the versioned path it points to. A staged copy
// is used only if the manager can't run the binary where it is.
func (a *App) EnableAutostart() error {
	sup := a.autostart()
	if !sup.Available() {
		return cli.Fail(cli.CodeInvalidArgs, "no service manager is available here to keep the server alive").
			WithHint(sup.StartHint())
	}
	exe, err := os.Executable()
	if err != nil {
		return cli.Failf(cli.CodeInternal, "could not locate the otata binary: %v", err)
	}

	// One unit per user: don't take it from another root.
	if spec, ok := sup.Installed(); ok && !agentMatches(spec, a.Root, a.Config.Port) {
		return cli.Failf(cli.CodeInvalidArgs,
			"the %s already serves %s; there is one per user", sup.Kind(), describeAgent(spec)).
			WithHint("run 'otata autostart off' to remove it, then 'otata autostart on' here")
	}

	err = a.installAgent(exe, defaultBindWait)
	if err == nil {
		return nil
	}
	// Only a refused program or a unit that never bound points at the binary's
	// location.
	if !programUnusable(err) {
		return err
	}

	// Retry from a copy under the root. On macOS, a binary in a TCC-protected
	// directory hangs in dyld while launchd reports it running, and systemd
	// refuses some paths outright or can't exec from a noexec mount.
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

// programUnusable reports whether the manager can't run the binary where it is.
func programUnusable(err error) bool {
	_, noBind := errors.AsType[*errAgentNoBind](err)
	_, refused := errors.AsType[*errAgentProgram](err)
	return noBind || refused
}

// errAgentNoBind is a unit the manager accepted that never bound the port.
type errAgentNoBind struct{ port int }

func (e *errAgentNoBind) Error() string {
	return fmt.Sprintf("unit loaded but nothing bound port %d", e.port)
}

// errAgentProgram is the manager refusing up front to run the binary from
// its path, as systemd does for a path with a quote in it.
type errAgentProgram struct {
	Program string
	Reason  string
}

func (e *errAgentProgram) Error() string {
	return fmt.Sprintf("cannot run %s: %s", e.Program, e.Reason)
}

// installAgent installs a unit running program and waits for it to bind.
func (a *App) installAgent(program string, wait time.Duration) error {
	sup := a.autostart()
	// Unload first, or the manager respawns the server StopServer signals.
	if err := sup.Unload(); err != nil {
		return cli.Failf(cli.CodeInternal, "could not unload the %s: %v", sup.Kind(), err)
	}
	if err := a.StopServer(); err != nil {
		return err
	}

	// The unit embeds root and port. Everything else comes from the root's config.
	spec := agentSpec{Program: program, Root: a.Root, Port: a.Config.Port, Log: a.Store.ServerLog()}
	if err := sup.Install(spec); err != nil {
		// A refused program wrote nothing, and EnableAutostart retries from a copy.
		// Any other failure may have left a unit file, which would read as enabled.
		if _, ok := errors.AsType[*errAgentProgram](err); ok {
			return err
		}
		_ = sup.Remove()
		return cli.Failf(cli.CodeInternal, "could not install the %s: %v", sup.Kind(), err)
	}
	// `autostart on` is explicit enough to clear the user's disable switch. The
	// repair paths never do.
	sup.Enable()
	if err := sup.Load(); err != nil {
		_ = sup.Remove()
		if sup.Disabled() {
			// The enable didn't stick, so something like an MDM policy holds the switch.
			return a.errAgentDisabled()
		}
		return cli.Failf(cli.CodeInternal, "could not load the %s: %v", sup.Kind(), err)
	}
	if a.waitForBind(wait) {
		return nil
	}
	// Don't leave a unit that would load at login and never bind.
	_ = sup.Remove()
	return &errAgentNoBind{port: a.Config.Port}
}

func (a *App) DisableAutostart() error {
	if err := a.autostart().Remove(); err != nil {
		return cli.Failf(cli.CodeInternal, "%v", err)
	}
	return nil
}

// stageAgentBinary copies exe to dest atomically, so a unit never runs a
// partial image.
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
	// Keep the source's mtime so filesDiffer can compare without hashing.
	if info, err := os.Stat(exe); err == nil {
		_ = os.Chtimes(dest, info.ModTime(), info.ModTime())
	}
	return dest, nil
}

// AutostartProgram returns what the installed unit runs, and whether that
// differs from this binary.
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
