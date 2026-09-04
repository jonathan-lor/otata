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

// unitName is the systemd unit, and its file's basename. systemd keys the
// unit's enabled and masked state by name, so it must not change after release.
const unitName = "otata.service"

// unitPath is where a user's own units live, and where `systemctl --user
// mask` plants its link to /dev/null: $XDG_CONFIG_HOME/systemd/user.
func unitPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", unitName)
}

// systemctlTimeout bounds every systemctl call. A wedged user bus must not
// hang status, whichever question was being asked.
const systemctlTimeout = 10 * time.Second

// systemctl runs one `systemctl --user` query and returns what it printed on
// stdout. The exit status rides along: is-enabled exits non-zero for a
// disabled or masked unit, among others, and the word on stdout is the
// answer either way.
func systemctl(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).Output()
}

// systemctlRun is systemctl for the mutations, whose failure is explained by
// what the CLI printed rather than by its exit status.
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

// stderrWords is what a failed query printed, or the exec error when it
// printed nothing.
func stderrWords(err error) string {
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		if msg := strings.TrimSpace(string(exit.Stderr)); msg != "" {
			return msg
		}
	}
	return err.Error()
}

// newSupervisor is systemd wherever systemctl exists, and none where it does
// not: a Linux with another init (WSL 1, Alpine, a slim container) has no
// manager to install a unit into. Whether this process can reach the user's
// manager is a separate question, asked once here. A shell with no session
// behind it (cron, sudo -u, a container with the home mounted) cannot, and
// the supervisor then answers from the unit file alone.
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

// systemd is the Linux supervisor: a unit in the user's own manager, which
// runs while the user has a session, as a launch agent runs while one is
// logged into the console. The manager is asked what it is doing; the
// definition is read from the file, which is what runs at next login.
type systemd struct {
	// unreachable is why the probe found no user manager, or nil. The file
	// still says what runs at next login and can still be removed, but
	// nothing can be asked of the manager, so nothing is: every systemctl
	// call answers with this instead of spawning. Answering with none in
	// this state hid an installed unit and made `autostart off` a no-op.
	unreachable error

	// The unit file, read once per process: status asks four times, and
	// Install and Remove are what change it. They drop the memo.
	spec     agentSpec
	specOK   bool
	specRead bool
	// The word is-enabled printed, read once per process for the same
	// reason: Disabled and DisabledHint both ask it. Install, Enable and
	// Remove change it.
	enabled     string
	enabledRead bool
}

// query and run are systemctl and systemctlRun, or the probe's answer when
// the manager was never reachable.
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

// readUnitFile reads the installed unit. A masked unit is a link to
// /dev/null at the same path, which reads as empty, and so as no unit.
func readUnitFile() (agentSpec, bool) {
	data, err := os.ReadFile(unitPath())
	if err != nil {
		return agentSpec{}, false
	}
	return parseUnitFile(data)
}

// Loaded reports whether the manager is running the unit or trying to:
// active, or activating between the restarts Restart=on-failure schedules.
// Signaling the server in either state only gets it respawned, so stopping
// goes through the manager. A unit that hit its start limit is failed, and
// not loaded: the caller's answer to an unloaded unit is Load.
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

// Disabled reads what `systemctl --user disable` and `mask` leave behind. A
// disabled unit has lost the link that starts it at login; a masked one
// cannot be started at all. Either is the user's switch, so either breaks
// what autostart promises, and neither is touched anywhere but Enable.
func (s *systemd) Disabled() bool { return enabledStateDisabled(s.enabledState()) }

// enabledStateDisabled reads the word is-enabled prints. Everything else it
// can say (enabled, static, linked, alias, indirect, not-found) leaves the
// unit startable, and starting at login where a link exists.
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

// Enable clears both of the user's switches: unmask, then enable, which
// rewrites the login link that disable removed. Both are no-ops when
// nothing was switched off.
func (s *systemd) Enable() {
	s.enabledRead = false
	_ = s.run("unmask", unitName)
	_ = s.run("enable", unitName)
}

// Install writes the unit and enables it, so that after Install the manager
// would run it at next login, as it would a plist in LaunchAgents. On systemd
// the file alone is only known by name; the default.target.wants link that
// `enable` writes is what starts it. daemon-reload comes between, or `start`
// would not find a new unit and would run the old definition of a changed
// one. The write is atomic, so a torn unit is never what the manager reads.
// A failure after the write leaves the file, and the caller removes it: a
// file with no login link would read as autostart on for a unit that never
// returns.
//
// A program systemd would refuse is refused here, as the program's refusal,
// so the caller can stage a copy where the manager accepts one rather than
// learn it from `start` failing on a bad unit file.
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

// Load asks the manager to start the unit. A unit that hit its start limit
// is refused a start until the limit's interval has passed, and only
// reset-failed flushes the counter, so that runs first: the way back should
// not be a wait nobody was told about.
func (s *systemd) Load() error {
	_ = s.run("reset-failed", unitName)
	return s.run("start", unitName)
}

// Unload stops the unit and leaves it installed and enabled: down now, back
// at login, which is what `otata stop` means.
func (s *systemd) Unload() error {
	if !s.Loaded() {
		return nil
	}
	return s.run("stop", unitName)
}

// Remove takes the unit out entirely: stopped, its login link dropped, the
// file deleted, and the manager told, so nothing of it lingers in memory as
// a failed unit. disable runs before the delete because it reads the file's
// [Install] section to learn which link to drop. A mask at the path is the
// user's switch, not otata's unit, and stays.
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

// StartHint says how to get a server when none is installed, or, when the
// manager cannot be reached from this shell, why that is and where it can.
func (s *systemd) StartHint() string {
	if s.unreachable != nil {
		return fmt.Sprintf("no systemd user manager is reachable from this shell (%s); run 'otata autostart on' from a login session, or 'otata serve' in a foreground terminal", s.unreachable)
	}
	return "run 'otata autostart on' once so the server runs under systemd --user, or 'otata serve' in a foreground terminal"
}

// DisabledHint names the switch that is set: a mask and a disable are undone
// by different commands.
func (s *systemd) DisabledHint() string {
	if strings.HasPrefix(s.enabledState(), "masked") {
		return "run 'systemctl --user unmask " + unitName + "'"
	}
	return "run 'systemctl --user enable " + unitName + "'"
}

// ---------- the unit file ----------

// unitFile builds the unit. Its values follow two quoting rules, and they
// differ. ExecStart= and Environment= are split into words, so a value is
// wrapped in double quotes with backslash and quote escaped; the log path
// after append: is the rest of the line, taken as it is. Specifiers (%)
// expand in all three, so a literal percent is doubled everywhere. $ is
// left alone: Environment= expands no variables, and the program named by
// ExecStart= may not be one, so both take it literally.
//
// Restart=on-failure is the nearest thing to KeepAlive with SuccessfulExit
// false: a crash or a non-zero exit brings the server back. It is not the
// same thing. systemd counts SIGHUP, SIGINT, SIGTERM and SIGPIPE as clean
// exits, so a server something else terminates stays down until the next
// login, where launchd would respawn it. otata's own stop goes through the
// manager and never depends on that. The start limit is left at systemd's
// default; a unit that gives up is "installed but not loaded", which status
// reports with the command that reloads it.
func unitFile(spec agentSpec) ([]byte, error) {
	if err := unitSafe(spec); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=otata install server\n\n[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s serve\n", unitWord(spec.Program))
	fmt.Fprintf(&b, "Environment=%s\n", unitWord("OTATA_ROOT="+spec.Root))
	fmt.Fprintf(&b, "Environment=%s\n", unitWord("OTATA_PORT="+strconv.Itoa(spec.Port)))
	fmt.Fprintf(&b, "Environment=%s\n", unitWord("OTATA_PATH="+spec.ServePath))
	b.WriteString("Restart=on-failure\n")
	if spec.Log != "" {
		fmt.Fprintf(&b, "StandardOutput=append:%s\n", unitRaw(spec.Log))
		fmt.Fprintf(&b, "StandardError=append:%s\n", unitRaw(spec.Log))
	}
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String()), nil
}

// unitSafe refuses what a unit file cannot carry. systemd will not run a
// program from a path holding a quote, a backslash or a control character,
// however it is escaped, nor one that is not UTF-8: that is the manager
// refusing the program, and the caller's answer is a copy elsewhere. In any
// other value a control character or invalid UTF-8 has systemd drop the line
// or the assignment, and no copy would help, so that is an ordinary error.
func unitSafe(spec agentSpec) error {
	unsafe := func(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) || !utf8.ValidString(s) }
	if strings.ContainsAny(spec.Program, `"'\`) || unsafe(spec.Program) {
		return &errAgentProgram{Program: spec.Program,
			Reason: "systemd will not run a program from a path holding a quote, backslash, control character or invalid UTF-8"}
	}
	for _, v := range []struct{ name, value string }{{"root", spec.Root}, {"serve path", spec.ServePath}, {"log path", spec.Log}} {
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

// unitRaw escapes a value systemd takes whole, where only the specifier
// character means anything.
func unitRaw(s string) string { return strings.ReplaceAll(s, "%", "%%") }

// parseUnitFile reads the definition back out of the file. The file rather
// than `systemctl show`: the file is what runs at next login, where show
// reports the copy in memory, and show's ExecStart is a structured dump
// rather than the line. The lexer follows systemd's quoting for a well-formed
// value, which every value this package writes is. A hand-edited value that
// is malformed can read differently: an unbalanced quote drops the value
// here as it does there, but an unknown escape is kept verbatim as systemd
// keeps it in ExecStart=, where in Environment= systemd drops the whole
// assignment, and a line continuation is not joined.
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
			// An empty ExecStart= resets the list, which is systemd's way of
			// letting a later line replace an earlier one.
			spec.Program = ""
			if words := unitWords(value); len(words) > 0 {
				// The prefixes systemd allows before the path (-, @, :, +, !, |) are not part of it.
				spec.Program = strings.TrimLeft(words[0], "-@:+!|")
			}
		case "Environment":
			if value == "" {
				// An empty Environment= resets the list, as an empty ExecStart= does.
				spec.Root, spec.Port, spec.ServePath = "", 0, ""
			}
			for _, w := range unitWords(value) {
				name, v, _ := strings.Cut(w, "=")
				switch name {
				case "OTATA_ROOT":
					spec.Root = v
				case "OTATA_PORT":
					spec.Port, _ = strconv.Atoi(v)
				case "OTATA_PATH":
					spec.ServePath = v
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

// unitWords splits a setting's value as systemd does: on whitespace, with
// double or single quotes grouping a word, backslash escapes honored inside
// and outside quotes, and %% for a literal percent. An unbalanced quote is
// no value at all, and an escape systemd does not know is kept as written,
// backslash and all.
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

// unescape is the character a backslash escape stands for, and whether
// systemd knows the escape: its C escapes, \s for a space, and the three
// characters that quote or escape.
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

// unspecify undoes the one specifier escape this package writes. Any other
// %x is a specifier only systemd can expand, and is left as written.
func unspecify(s string) string { return strings.ReplaceAll(s, "%%", "%") }
