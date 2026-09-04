package app

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
)

// fakeSupervisor is a service manager that does what it is told and remembers
// what it was told, so the orchestration above it can be proven without
// launchd or systemd: what is installed, in what order things happen, and
// what is left behind when a unit never binds or a program is refused.
type fakeSupervisor struct {
	available bool
	installed *agentSpec
	loaded    bool
	disabled  bool
	loadErr   error
	// installErr is a failure after the definition was written, as a
	// daemon-reload or enable the manager refused: the fake keeps the unit
	// installed, as the file stays on disk.
	installErr error
	// unusable is a program the fake refuses to run from where it is, as
	// systemd refuses a path holding a quote. installs records every
	// program Install was asked for, refused or not.
	unusable string
	installs []string
	calls    []string
}

func (f *fakeSupervisor) Available() bool { return f.available }
func (f *fakeSupervisor) Kind() string    { return "fake unit" }
func (f *fakeSupervisor) Installed() (agentSpec, bool) {
	if f.installed == nil {
		return agentSpec{}, false
	}
	return *f.installed, true
}
func (f *fakeSupervisor) Loaded() bool   { return f.loaded }
func (f *fakeSupervisor) Disabled() bool { return f.disabled }
func (f *fakeSupervisor) Enable()        { f.calls = append(f.calls, "enable") }
func (f *fakeSupervisor) Install(spec agentSpec) error {
	f.calls = append(f.calls, "install")
	f.installs = append(f.installs, spec.Program)
	if spec.Program == f.unusable {
		return &errAgentProgram{Program: spec.Program, Reason: "the fake refuses it"}
	}
	f.installed = &spec
	return f.installErr
}
func (f *fakeSupervisor) Load() error {
	f.calls = append(f.calls, "load")
	if f.loadErr != nil {
		return f.loadErr
	}
	f.loaded = true
	return nil
}
func (f *fakeSupervisor) Unload() error {
	f.calls = append(f.calls, "unload")
	f.loaded = false
	return nil
}
func (f *fakeSupervisor) Remove() error {
	f.calls = append(f.calls, "remove")
	f.installed, f.loaded = nil, false
	return nil
}
func (f *fakeSupervisor) StartHint() string    { return "start it yourself" }
func (f *fakeSupervisor) DisabledHint() string { return "flip the fake switch" }

// supervised is a scratch App on port 1, where nothing ever binds, with the
// fake in charge and the bind waits shortened to what a test can afford.
func supervised(t *testing.T, f *fakeSupervisor) *App {
	t.Helper()
	a := freshApp(t)
	a.sup = f
	a.bindWait = 50 * time.Millisecond
	return a
}

// A unit that loads but never binds is not autostart: the orchestration tries
// the invoked binary, then a staged copy, and leaves nothing installed when
// neither comes up, so the next login does not load a unit for a server that
// never binds.
func TestEnableAutostartLeavesNothingWhenNoUnitBinds(t *testing.T) {
	f := &fakeSupervisor{available: true}
	a := supervised(t, f)

	err := a.EnableAutostart()
	fail := cli.AsFailure(err)
	if err == nil || fail.Code != cli.CodeServerDown || !strings.Contains(fail.Message, "from a copy") {
		t.Fatalf("got %v, want a server_down naming both attempts", err)
	}
	if f.installed != nil || f.loaded {
		t.Errorf("a unit was left behind: %+v loaded=%v", f.installed, f.loaded)
	}
	// Both attempts install, enable, load, and remove what did not bind, in that order.
	want := []string{"unload", "install", "enable", "load", "remove", "unload", "install", "enable", "load", "remove", "remove"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v\nwant    %v", f.calls, want)
	}
	// The staged copy is a real copy of the running binary, with its time.
	staged, err := os.Stat(a.Store.StagedBinary())
	if err != nil {
		t.Fatalf("no staged copy: %v", err)
	}
	exe, _ := os.Executable()
	if info, err := os.Stat(exe); err == nil && !staged.ModTime().Equal(info.ModTime()) {
		t.Error("the staged copy does not carry its source's modification time")
	}
}

// One unit per user: another root's is not overwritten, and nothing is
// touched in refusing.
func TestEnableAutostartRefusesAnotherRootsUnit(t *testing.T) {
	f := &fakeSupervisor{available: true, installed: &agentSpec{Program: "/x/otata", Root: "/elsewhere", Port: 8787}}
	a := supervised(t, f)
	err := a.EnableAutostart()
	if fail := cli.AsFailure(err); err == nil || fail.Code != cli.CodeInvalidArgs || !strings.Contains(fail.Message, "/elsewhere") {
		t.Fatalf("got %v, want a refusal naming the other root", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("refusing touched the manager: %v", f.calls)
	}
}

// Where no service manager exists, autostart is refused up front with the
// manager's own advice, and nothing is stopped or written on the way.
func TestEnableAutostartRefusesWhereNoManagerExists(t *testing.T) {
	f := &fakeSupervisor{}
	a := supervised(t, f)
	err := a.EnableAutostart()
	fail := cli.AsFailure(err)
	if err == nil || fail.Code != cli.CodeInvalidArgs || fail.Hint != f.StartHint() {
		t.Fatalf("got %v, want invalid_args carrying the start hint", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("refusing touched the manager: %v", f.calls)
	}
}

// A load the manager refuses removes the unit it was refused for, and the
// disabled switch is reported as itself rather than as the manager's words.
func TestInstallAgentRemovesAUnitTheManagerWillNotLoad(t *testing.T) {
	f := &fakeSupervisor{available: true, loadErr: errors.New("Input/output error")}
	a := supervised(t, f)
	err := a.installAgent("/x/otata", time.Second)
	if fail := cli.AsFailure(err); err == nil || fail.Code != cli.CodeInternal || !strings.Contains(fail.Message, "Input/output error") {
		t.Errorf("got %v, want the manager's words as an internal error", err)
	}
	if f.installed != nil {
		t.Error("the unit the manager refused was left installed")
	}

	// The fake's Enable clears nothing, as an MDM-held switch would not.
	f = &fakeSupervisor{available: true, loadErr: errors.New("Input/output error"), disabled: true}
	a = supervised(t, f)
	err = a.installAgent("/x/otata", time.Second)
	if fail := cli.AsFailure(err); err == nil || fail.Code != cli.CodeServerDown || !strings.Contains(fail.Hint, f.DisabledHint()) {
		t.Errorf("got %v, want the disabled refusal with its hint", err)
	}
}

// StartServer through the unit: a disabled one is refused with where to flip
// it; a loaded one that has not bound is reported, not reloaded; an unloaded
// one is loaded and given its wait.
func TestReloadAgentReportsWhatItCannotMend(t *testing.T) {
	ours := func(a *App) *agentSpec { return &agentSpec{Program: "/x/otata", Root: a.Root, Port: a.Config.Port} }

	f := &fakeSupervisor{available: true, disabled: true}
	a := supervised(t, f)
	f.installed = ours(a)
	err := a.StartServer()
	if fail := cli.AsFailure(err); err == nil || fail.Code != cli.CodeServerDown || !strings.Contains(fail.Hint, "flip the fake switch") {
		t.Errorf("disabled: got %v", err)
	}

	f = &fakeSupervisor{available: true, loaded: true}
	a = supervised(t, f)
	f.installed = ours(a)
	err = a.StartServer()
	if fail := cli.AsFailure(err); err == nil || !strings.Contains(fail.Message, "loaded but nothing has bound") {
		t.Errorf("loaded and unbound: got %v", err)
	}
	if slices.Contains(f.calls, "load") {
		t.Error("a loaded unit was loaded again")
	}

	f = &fakeSupervisor{available: true}
	a = supervised(t, f)
	f.installed = ours(a)
	err = a.StartServer()
	if fail := cli.AsFailure(err); err == nil || !strings.Contains(fail.Message, "reloaded the fake unit but nothing bound") {
		t.Errorf("unloaded: got %v", err)
	}
	if !slices.Contains(f.calls, "load") {
		t.Error("an unloaded unit was not loaded")
	}
}

// Stopping goes through the manager while it manages our server, and refuses
// to touch a server another root's unit keeps alive.
func TestStopServerUnloadsOurUnit(t *testing.T) {
	f := &fakeSupervisor{available: true, loaded: true}
	a := supervised(t, f)
	f.installed = &agentSpec{Program: "/x/otata", Root: a.Root, Port: a.Config.Port}
	if err := a.StopServer(); err != nil {
		t.Fatal(err)
	}
	if f.loaded || !slices.Contains(f.calls, "unload") {
		t.Errorf("our loaded unit was not unloaded: loaded=%v calls=%v", f.loaded, f.calls)
	}
}

// A manager that refuses the program from where it sits says so before
// anything is loaded, and that is the second road to the staged copy: the
// copy is tried, and when it does not bind either, the failure names both
// attempts as it does when the first attempt loaded and never bound.
func TestEnableAutostartStagesACopyWhenTheManagerRefusesTheProgram(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSupervisor{available: true, unusable: exe}
	a := supervised(t, f)

	err = a.EnableAutostart()
	fail := cli.AsFailure(err)
	if err == nil || fail.Code != cli.CodeServerDown || !strings.Contains(fail.Message, "from a copy") {
		t.Fatalf("got %v, want a server_down naming both attempts", err)
	}
	if want := []string{exe, a.Store.StagedBinary()}; !slices.Equal(f.installs, want) {
		t.Errorf("installed programs = %v, want %v", f.installs, want)
	}
	// The refusal came before anything was enabled or loaded; the copy went the whole way.
	want := []string{"unload", "install", "unload", "install", "enable", "load", "remove", "remove"}
	if !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v\nwant    %v", f.calls, want)
	}
	if f.installed != nil || f.loaded {
		t.Errorf("a unit was left behind: %+v loaded=%v", f.installed, f.loaded)
	}
}

// The copy can be refused too, when the root itself sits at a path the
// manager will not run from. Then the failure carries the manager's reason
// and names the root as the thing to move.
func TestEnableAutostartReportsAProgramRefusedFromTheCopyToo(t *testing.T) {
	f := &fakeSupervisor{available: true}
	a := supervised(t, f)
	f.unusable = a.Store.StagedBinary()

	err := a.EnableAutostart()
	fail := cli.AsFailure(err)
	if err == nil || fail.Code != cli.CodeServerDown {
		t.Fatalf("got %v, want server_down", err)
	}
	for _, want := range []string{"nor from a copy", "the fake refuses it", a.Root} {
		if !strings.Contains(fail.Message, want) {
			t.Errorf("message lacks %q: %s", want, fail.Message)
		}
	}
	if !strings.Contains(fail.Hint, "OTATA_ROOT") {
		t.Errorf("hint does not name the root as movable: %q", fail.Hint)
	}
	if f.installed != nil {
		t.Errorf("a unit was left behind: %+v", f.installed)
	}
}

// An Install that fails after writing the definition (systemd's daemon-reload
// or enable refused) leaves nothing behind: a file with no login link would
// read as autostart on for a unit that never returns. The failure is not the
// program's, so no copy is staged either.
func TestEnableAutostartRemovesAUnitItCouldNotFinishInstalling(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSupervisor{available: true, installErr: errors.New("Failed to connect to bus")}
	a := supervised(t, f)

	err = a.EnableAutostart()
	fail := cli.AsFailure(err)
	if err == nil || fail.Code != cli.CodeInternal || !strings.Contains(fail.Message, "Failed to connect to bus") {
		t.Fatalf("got %v, want the manager's words as an internal error", err)
	}
	if f.installed != nil {
		t.Error("the half-installed unit was left behind")
	}
	if want := []string{"unload", "install", "remove"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
	if want := []string{exe}; !slices.Equal(f.installs, want) {
		t.Errorf("a copy was staged for a failure that was not the program's: %v", f.installs)
	}
}
