//go:build linux

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonathan-lor/otata/internal/atomicfile"
)

// unitName must never change: systemd keys enabled and masked state by name.
const unitName = "otata.service"

// unitPath is the unit file in $XDG_CONFIG_HOME/systemd/user.
func unitPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

// systemctlTimeout keeps a wedged user bus from hanging otata.
const systemctlTimeout = 10 * time.Second

// systemctl runs a `systemctl --user` query. Its stdout matters even on a
// non-zero exit, which is-enabled returns for a disabled unit.
func systemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).Output()
}

// systemctlRun runs a `systemctl --user` change, returning its output as the error.
func systemctlRun(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

func stderrWords(err error) string {
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			return msg
		}
	}
	return err.Error()
}

// newSupervisor returns systemd where systemctl exists. If the user manager
// can't be reached (cron, sudo -u), it still reads and removes the unit file.
func newSupervisor() supervisor {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return none{}
	}
	s := &systemd{}
	if _, err := systemctl("show-environment"); err != nil {
		s.unreachable = errors.New(stderrWords(err))
	}
	return s
}

// systemd is the Linux supervisor, a unit in the user's systemd manager.
type systemd struct {
	// unreachable is why the user manager can't be reached, or nil. While set,
	// every systemctl call returns it instead of running.
	unreachable error

	// The unit file, read once. Install and Remove reset it.
	spec     agentSpec
	specOK   bool
	specRead bool
	// is-enabled's answer, read once. Install, Enable and Remove reset it.
	enabled     string
	enabledRead bool
}

func (s *systemd) query(args ...string) ([]byte, error) {
	if s.unreachable != nil {
		return nil, s.unreachable
	}
	return systemctl(args...)
}

func (s *systemd) run(args ...string) error {
	if s.unreachable != nil {
		return s.unreachable
	}
	return systemctlRun(args...)
}

func (s *systemd) Available() bool { return s.unreachable == nil }
func (s *systemd) Kind() string    { return "systemd user service" }

func (s *systemd) Installed() (agentSpec, bool) {
	if !s.specRead {
		s.spec, s.specOK = readUnitFile()
		s.specRead = true
	}
	return s.spec, s.specOK
}

// readUnitFile reads the installed unit. A mask, a link to /dev/null, reads
// as no unit.
func readUnitFile() (agentSpec, bool) {
	data, err := os.ReadFile(unitPath())
	if err != nil {
		return agentSpec{}, false
	}
	return parseUnitFile(data)
}

// Loaded includes activating, between Restart=on-failure restarts. A unit
// that hit its start limit is failed, and not loaded.
func (s *systemd) Loaded() bool {
	out, err := s.query("show", "-p", "ActiveState", "--value", unitName)
	if err != nil {
		return false
	}
	return activeStateLoaded(strings.TrimSpace(string(out)))
}

func activeStateLoaded(state string) bool {
	switch state {
	case "active", "activating", "reloading":
		return true
	}
	return false
}

// Disabled reports a unit the user disabled or masked.
func (s *systemd) Disabled() bool { return enabledStateDisabled(s.enabledState()) }

// enabledStateDisabled reads is-enabled's output. Only disabled and masked
// keep a unit from returning at login.
func enabledStateDisabled(state string) bool {
	return state == "disabled" || strings.HasPrefix(state, "masked")
}

func (s *systemd) enabledState() string {
	if !s.enabledRead {
		out, _ := s.query("is-enabled", unitName)
		s.enabled = strings.TrimSpace(string(out))
		s.enabledRead = true
	}
	return s.enabled
}

// Enable undoes a mask and a disable. Both are no-ops otherwise.
func (s *systemd) Enable() {
	s.enabledRead = false
	_ = s.run("unmask", unitName)
	_ = s.run("enable", unitName)
}

// Install writes the unit, reloads systemd so start sees the new definition,
// and enables it, since only the enable link starts it at login. On a failure
// after the write, the caller removes the file.
func (s *systemd) Install(spec agentSpec) error {
	if s.unreachable != nil {
		return s.unreachable
	}
	s.specRead, s.enabledRead = false, false
	unit, err := unitFile(spec)
	if err != nil {
		return err
	}
	path := unitPath()
	if err := atomicfile.WriteData(filepath.Dir(path), path, 0o644, unit); err != nil {
		return err
	}
	if err := s.run("daemon-reload"); err != nil {
		return err
	}
	return s.run("enable", unitName)
}

// Load resets the failure count first, so a unit at its start limit can
// start again.
func (s *systemd) Load() error {
	_ = s.run("reset-failed", unitName)
	return s.run("start", unitName)
}

// Unload stops the unit and leaves it enabled, so it returns at login.
func (s *systemd) Unload() error {
	if !s.Loaded() {
		return nil
	}
	return s.run("stop", unitName)
}

// Remove stops, disables and deletes the unit. disable runs first because it
// reads the file's [Install] section. A user's mask stays.
func (s *systemd) Remove() error {
	s.specRead, s.enabledRead = false, false
	_ = s.run("stop", unitName)
	_ = s.run("disable", unitName)
	path := unitPath()
	if target, err := os.Readlink(path); err != nil || target != os.DevNull {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_ = s.run("daemon-reload")
	_ = s.run("reset-failed", unitName)
	return nil
}

func (s *systemd) StartHint() string {
	if s.unreachable != nil {
		return fmt.Sprintf("no systemd user manager is reachable from this shell (%s); run 'otata autostart on' from a login session, or 'otata serve' in a foreground terminal", s.unreachable)
	}
	return "run 'otata autostart on' once so the server runs under systemd --user, or 'otata serve' in a foreground terminal"
}

func (s *systemd) DisabledHint() string {
	if strings.HasPrefix(s.enabledState(), "masked") {
		return "run 'systemctl --user unmask " + unitName + "'"
	}
	return "run 'systemctl --user enable " + unitName + "'"
}

// unitFile builds the unit. ExecStart= and Environment= values are quoted
// words, the append: log path is taken raw, and % is doubled everywhere.
// Restart=on-failure approximates launchd's KeepAlive, but systemd treats a
// SIGTERM or SIGHUP exit as clean and doesn't restart it.
func unitFile(spec agentSpec) ([]byte, error) {
	if err := unitSafe(spec); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=otata install server\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", unitWord(spec.Program))
	fmt.Fprintf(&b, "Environment=%s\n", unitWord("OTATA_ROOT="+spec.Root))
	fmt.Fprintf(&b, "Environment=%s\n", unitWord("OTATA_PORT="+strconv.Itoa(spec.Port)))
	b.WriteString("Restart=on-failure\n")
	if spec.Log != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\n", unitRaw(spec.Log))
		fmt.Fprintf(&b, "StandardError=append:%s\n", unitRaw(spec.Log))
	}
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String()), nil
}

// unitSafe refuses what a unit can't carry. A program path systemd won't run
// is errAgentProgram, so the caller can try a copy. Bad bytes in any other
// value are a plain error.
func unitSafe(spec agentSpec) error {
	unsafe := func(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) || !utf8.ValidString(s) }
	if strings.ContainsAny(spec.Program, `"'\`) || unsafe(spec.Program) {
		return &errAgentProgram{Program: spec.Program,
			Reason: "systemd will not run a program from a path holding a quote, backslash, control character or invalid UTF-8"}
	}
	for _, v := range []struct{ name, value string }{{"root", spec.Root}, {"log path", spec.Log}} {
		if unsafe(v.value) {
			return fmt.Errorf("the %s %q holds a control character or invalid UTF-8, which a systemd unit cannot carry", v.name, v.value)
		}
	}
	return nil
}

// unitWord quotes one word for a setting systemd splits into words.
func unitWord(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s) + `"`
}

// unitRaw escapes a value systemd takes whole.
func unitRaw(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// parseUnitFile reads the unit back from the file, which is what runs at next
// login, rather than from systemctl show. It follows systemd's quoting for
// any value unitFile writes.
func parseUnitFile(data []byte) (agentSpec, bool) {
	var spec agentSpec
	section := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", line[0] == '#', line[0] == ';':
			continue
		case line[0] == '[':
			section = line
			continue
		case section != "[Service]":
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "ExecStart":
			// An empty ExecStart= resets the list.
			spec.Program = ""
			if words := unitWords(value); len(words) > 0 {
				// Strip systemd's exec prefixes (-, @, :, +, !, |).
				spec.Program = strings.TrimLeft(words[0], "-@:+!|")
			}
		case "Environment":
			if value == "" {
				// An empty Environment= resets the list too.
				spec.Root, spec.Port = "", 0
			}
			for _, w := range unitWords(value) {
				name, v, _ := strings.Cut(w, "=")
				switch name {
				case "OTATA_ROOT":
					spec.Root = v
				case "OTATA_PORT":
					spec.Port, _ = strconv.Atoi(v)
				}
			}
		case "StandardOutput":
			if p, ok := strings.CutPrefix(value, "append:"); ok {
				spec.Log = unspecify(p)
			}
		}
	}
	return spec, spec.Program != ""
}

// unitWords splits a value into words as systemd does. An unbalanced quote
// yields nothing, and an unknown escape is kept as written.
func unitWords(s string) []string {
	var words []string
	var w strings.Builder
	inWord := false
	var quote byte
	flush := func() {
		if inWord {
			words = append(words, unspecify(w.String()))
			w.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote, inWord = c, true
		case c == '\\' && i+1 < len(s):
			i++
			if u, known := unescape(s[i]); known {
				w.WriteByte(u)
			} else {
				w.WriteByte('\\')
				w.WriteByte(s[i])
			}
			inWord = true
		case quote == 0 && (c == ' ' || c == '\t'):
			flush()
		default:
			w.WriteByte(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil
	}
	flush()
	return words
}

// unescape decodes a backslash escape, reporting whether systemd knows it.
func unescape(c byte) (byte, bool) {
	switch c {
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'r':
		return '\r', true
	case 'a':
		return '\a', true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'v':
		return '\v', true
	case 's':
		return ' ', true
	case '\\', '"', '\'':
		return c, true
	}
	return c, false
}

// unspecify undoes %%, the only specifier escape unitFile writes.
func unspecify(s string) string { return strings.ReplaceAll(s, "%%", "%") }
