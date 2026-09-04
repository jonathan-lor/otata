//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hostileSpec carries what a home directory or install path can hold and a
// unit file has to survive: spaces, an ampersand, angle brackets, quotes, a
// backslash, a dollar and a percent, which is systemd's specifier character.
// The program stays within what systemd will exec, which excludes quotes and
// backslashes.
func hostileSpec(program string) agentSpec {
	return agentSpec{
		Program:   program,
		Root:      `/home/a & b "q" 'r' %p ${HOME} \back/.otata`,
		Port:      9123,
		ServePath: "/builds<1>",
		Log:       `/home/a & b "q" %p ${HOME}/server.log`,
	}
}

// What Install writes, Installed reads back exactly, and the unit carries the
// two settings the promise rests on: back after a crash, and started at login.
func TestUnitFileRoundTrips(t *testing.T) {
	spec := hostileSpec("/opt/a & b %p ${HOME} <x>/otata")
	unit, err := unitFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := parseUnitFile(unit)
	if !ok || got != spec {
		t.Errorf("read back %+v\nwant      %+v\nunit:\n%s", got, spec, unit)
	}
	for _, want := range []string{
		"Restart=on-failure\n",
		"WantedBy=default.target\n",
		"ExecStart=\"/opt/a & b %%p ${HOME} <x>/otata\" serve\n",
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
}

// systemd's own parser is the oracle for the escaping. verify resolves the
// program path and checks that it is executable, so a path that decoded
// wrongly is reported as missing, and the check runs offline against a link
// to this test binary under a hostile directory name. Skipped where
// systemd-analyze or a session is absent, which is a machine that could not
// run the unit either.
func TestUnitFileIsReadBySystemdAsWritten(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Skip("no session: systemd-analyze --user needs a runtime directory")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "a & b %p ${HOME} <x>")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "otata")
	if err := os.Symlink(exe, program); err != nil {
		t.Fatal(err)
	}
	verify := func(spec agentSpec) (string, error) {
		t.Helper()
		unit, err := unitFile(spec)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), unitName)
		if err := os.WriteFile(path, unit, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("systemd-analyze", "--user", "verify", path).CombinedOutput()
		// verify exits 0 while dropping a malformed Environment= line with
		// "Invalid syntax, ignoring", so a complaint about this unit is a
		// failure whatever the status. Other units' warnings name their own paths.
		if err == nil {
			for line := range strings.Lines(string(out)) {
				if strings.HasPrefix(line, path) || strings.HasPrefix(line, unitName+":") {
					err = fmt.Errorf("systemd complained: %s", strings.TrimSpace(line))
					break
				}
			}
		}
		return string(unit) + "\n" + string(out), err
	}
	if out, err := verify(hostileSpec(program)); err != nil {
		t.Fatalf("systemd rejected the unit: %v\n%s", err, out)
	}
	// The oracle is live: the same unit naming a program that is not there is refused.
	if out, err := verify(hostileSpec(filepath.Join(dir, "missing"))); err == nil {
		t.Errorf("systemd accepted a unit whose program does not exist:\n%s", out)
	}
}

// systemd will not run a program from a path holding a quote or backslash, a
// control character or invalid UTF-8, so Install says so up front as the
// program's refusal, which is what sends EnableAutostart to a staged copy.
// The same in any other value would have systemd drop the line, and no copy
// would help, so that is an ordinary error.
func TestUnitFileRefusesWhatSystemdCannotRun(t *testing.T) {
	for _, program := range []string{`/x/Jon's tools/otata`, `/x/say "hi"/otata`, `/x/back\slash/otata`, "/x/new\nline/otata", "/x/\xff/otata"} {
		_, err := unitFile(hostileSpec(program))
		if _, ok := errors.AsType[*errAgentProgram](err); !ok {
			t.Errorf("program %q: got %v, want the program's refusal", program, err)
		}
	}
	for name, root := range map[string]string{"a directive": "/home/x\n[Service]\nExecStart=/evil", "not UTF-8": "/home/\xff/.otata"} {
		spec := hostileSpec("/opt/otata")
		spec.Root = root
		_, err := unitFile(spec)
		if err == nil {
			t.Errorf("a root holding %s was written", name)
		}
		if _, ok := errors.AsType[*errAgentProgram](err); ok {
			t.Errorf("a root holding %s was blamed on the program", name)
		}
	}
}

// The lexer reads systemd's own syntax, so a unit another hand has edited
// reads as systemd reads it: single quotes, escapes outside quotes, several
// assignments on one Environment= line, a prefix on the program, and an
// empty ExecStart= or Environment= resetting the earlier ones.
func TestParseUnitFileReadsSystemdsOwnSyntax(t *testing.T) {
	unit := `
[Unit]
Description=x
# a comment
[Service]
ExecStart=/old/otata serve
ExecStart=
ExecStart=-'/opt/my tools/otata' serve
Environment=OTATA_ROOT=/stale OTATA_PORT=1
Environment=
Environment=OTATA_PORT=8787 "OTATA_ROOT=/home/a b/.otata" OTATA_PATH=/a\sb
StandardOutput=append:/home/a b/%%p/server.log

[Install]
WantedBy=default.target
`
	want := agentSpec{Program: "/opt/my tools/otata", Root: "/home/a b/.otata", Port: 8787, ServePath: "/a b", Log: "/home/a b/%p/server.log"}
	if got, ok := parseUnitFile([]byte(unit)); !ok || got != want {
		t.Errorf("parsed %+v, want %+v", got, want)
	}
	// No ExecStart is no unit. A mask is a link to /dev/null, which reads as empty.
	for name, data := range map[string]string{
		"empty":               "",
		"no ExecStart":        "[Service]\nEnvironment=OTATA_ROOT=/x\n",
		"ExecStart elsewhere": "[Unit]\nExecStart=/x serve\n",
		"unbalanced quote":    "[Service]\nExecStart=\"/x/otata serve\n",
	} {
		if _, ok := parseUnitFile([]byte(data)); ok {
			t.Errorf("%s: read as an installed unit", name)
		}
	}
}

func TestUnitWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`a b`, []string{"a", "b"}},
		{`"a b" c`, []string{"a b", "c"}},
		{`'a b'c`, []string{"a bc"}},
		{`"q\"uote" 'back\\slash'`, []string{`q"uote`, `back\slash`}},
		{`tab\there`, []string{"tab\there"}},
		{`100%% "50%% off"`, []string{"100%", "50% off"}},
		{"tab\tsplit", []string{"tab", "split"}},
		// An escape systemd does not know is kept as written, backslash and
		// all, so it neither joins two words nor eats a percent.
		{`a\ b`, []string{`a\ b`}},
		{`x\%%`, []string{`x\%`}},
		// An unbalanced quote is no value at all, as systemd drops it.
		{`"unterminated`, nil},
		{`a 'b`, nil},
		{``, nil},
		{`   `, nil},
	}
	for _, c := range cases {
		if got := unitWords(c.in); !slices.Equal(got, c.want) {
			t.Errorf("unitWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The words systemd prints for a unit's state, mapped onto the two questions
// App asks: is the manager running it or trying to, and did the user switch
// it off.
func TestSystemdStateWords(t *testing.T) {
	for state, want := range map[string]bool{
		"active": true, "activating": true, "reloading": true,
		"inactive": false, "deactivating": false, "failed": false, "": false,
	} {
		if got := activeStateLoaded(state); got != want {
			t.Errorf("activeStateLoaded(%q) = %v, want %v", state, got, want)
		}
	}
	for state, want := range map[string]bool{
		"enabled": false, "enabled-runtime": false, "static": false, "linked": false,
		"alias": false, "indirect": false, "not-found": false, "": false,
		"disabled": true, "masked": true, "masked-runtime": true,
	} {
		if got := enabledStateDisabled(state); got != want {
			t.Errorf("enabledStateDisabled(%q) = %v, want %v", state, got, want)
		}
	}
}

// A shell with no reachable user manager (cron, sudo -u, a container with
// the home mounted) still has the unit file, which is what runs at next
// login. Installed reads it and Remove deletes it, nothing is asked of a
// manager that is not there, and the hint says why autostart is refused.
// Answering with none in this state hid the unit and made `autostart off`
// a no-op. The unit directory is redirected, so nothing here touches the
// real one, and the manager is never spawned.
func TestUnreachableManagerStillReadsAndRemovesTheUnit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	spec := hostileSpec("/opt/otata")
	unit, err := unitFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, unit, 0o644); err != nil {
		t.Fatal(err)
	}

	s := &systemd{unreachable: errors.New("Failed to connect to bus: no medium found")}
	if s.Available() {
		t.Error("an unreachable manager reported available")
	}
	if hint := s.StartHint(); !strings.Contains(hint, "no medium found") || !strings.Contains(hint, "otata serve") {
		t.Errorf("the hint neither says why nor what to do: %q", hint)
	}
	if got, ok := s.Installed(); !ok || got != spec {
		t.Errorf("Installed = %+v, %v; want the unit on disk", got, ok)
	}
	if s.Loaded() || s.Disabled() {
		t.Error("a manager that cannot be asked reported the unit loaded or disabled")
	}
	if err := s.Load(); err == nil || !strings.Contains(err.Error(), "no medium found") {
		t.Errorf("Load = %v, want the probe's reason", err)
	}
	if err := s.Install(spec); err == nil {
		t.Error("Install wrote a unit it could not tell the manager about")
	}
	if err := s.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unit file survived Remove: %v", err)
	}
	if _, ok := s.Installed(); ok {
		t.Error("Installed still reports the removed unit")
	}

	// A mask is the user's switch, not otata's unit: it reads as no unit,
	// and Remove leaves it where it is.
	if err := os.Symlink(os.DevNull, path); err != nil {
		t.Fatal(err)
	}
	s = &systemd{unreachable: s.unreachable}
	if _, ok := s.Installed(); ok {
		t.Error("a mask read as an installed unit")
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(path); err != nil || target != os.DevNull {
		t.Errorf("Remove disturbed the user's mask: %q, %v", target, err)
	}
}
