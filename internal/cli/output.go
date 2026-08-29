// Package cli owns how commands talk to their caller.
//
// Output is formatted text unless --json is passed. The JSON form
// is the contract an agent can parse which provides stable error codes
// and details it can act on. Color follows the usual conventions of on when
// stdout is a terminal, and off when it is not or NO_COLOR is set.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Payload is what a command returns. Data carries the machine-readable result;
// Human renders the same information for a person.
type Payload interface {
	Human(w io.Writer)
}

type envelope struct {
	OK      bool     `json:"ok"`
	Command string   `json:"command"`
	Data    any      `json:"data,omitempty"`
	Error   *Failure `json:"error,omitempty"`
}

// jsonOutput is set by --json. It is a process-wide setting because every
// command emits through this package and the flag is global.
var jsonOutput bool

// SetJSON selects JSON output for the rest of the process.
func SetJSON(on bool) { jsonOutput = on }

// isTTY reports whether f looks like a terminal.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// colorTo reports whether text written to f should carry ANSI color: only
// when f is a terminal and NO_COLOR (no-color.org) is unset.
func colorTo(f *os.File) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return isTTY(f)
}

// textWriter returns f, or f behind a filter that drops ANSI color when f
// should not carry it. The human renderers write color unconditionally so
// they stay simple; this is the one place that decides whether it survives.
func textWriter(f *os.File) io.Writer {
	if colorTo(f) {
		return f
	}
	return &stripANSI{w: f}
}

// Emit writes a successful result in whichever form was selected.
func Emit(command string, data Payload) {
	if jsonOutput {
		writeJSON(os.Stdout, envelope{OK: true, Command: command, Data: data})
		return
	}
	data.Human(textWriter(os.Stdout))
}

// EmitError writes a failure and returns the process exit code to use:
// 2 when the command was called wrongly, and 1 for everything else.
// Text goes to stderr so a caller redirecting stdout still sees it.
// JSON goes to stdout, where the caller is already parsing.
func EmitError(command string, err error) int {
	return EmitFailure(command, nil, err)
}

// EmitFailure is EmitError for a command that ran to completion and still
// failed, such as doctor with a failing check. The result travels with
// the error so the caller sees what was checked, and ok/exit agree.
func EmitFailure(command string, data Payload, err error) int {
	f := AsFailure(err)
	if jsonOutput {
		writeJSON(os.Stdout, envelope{OK: false, Command: command, Data: data, Error: f})
	} else {
		if data != nil {
			data.Human(textWriter(os.Stdout))
		}
		w := textWriter(os.Stderr)
		fmt.Fprintf(w, "\033[1;31merror:\033[0m %s\n", Clean(f.Message))
		if f.Hint != "" {
			fmt.Fprintf(w, "  %s\n", Clean(f.Hint))
		}
	}
	if f.Code == CodeInvalidArgs {
		return 2
	}
	return 1
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// A serialization failure must not exit silently; the caller is parsing.
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not encode result: %v\n", err)
	}
}

// Section and Line render the human form consistently across commands.
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n", Clean(title))
}

// Line formats for a person. The format string is ours and may carry ANSI
// color; the arguments are not. A display name comes out of an Info.plist,
// untrusted for --artifact, so every string argument has its control
// characters removed. JSON output is unaffected: the encoder escapes them.
func Line(w io.Writer, format string, args ...any) {
	cleaned := make([]any, len(args))
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			cleaned[i] = Clean(s)
		} else {
			cleaned[i] = arg
		}
	}
	fmt.Fprintf(w, "    "+format+"\n", cleaned...)
}

// Clean removes what a terminal would interpret: C0 controls other than newline
// and tab, DEL, and the C1 range some terminals treat as 8-bit escapes. An ESC
// in a title can retitle the window, hide the URL after it, or fake a line.
func Clean(s string) string {
	clean := true
	for _, r := range s {
		if isControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r == 0x7f:
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	}
	return false
}

// stripANSI drops CSI sequences (ESC [ ... m, the color codes) from what
// passes through it.
// Arguments are already Cleaned of ESC, so the only sequences in the stream
// are the ones our own format strings put there. A sequence split across two
// writes is held back until it completes.
type stripANSI struct {
	w       io.Writer
	pending []byte // an unfinished escape sequence from the previous write
}

func (s *stripANSI) Write(p []byte) (int, error) {
	in := p
	if len(s.pending) > 0 {
		in = append(s.pending, p...)
		s.pending = nil
	}
	var out bytes.Buffer
	out.Grow(len(in))
	for i := 0; i < len(in); {
		if in[i] != 0x1b {
			out.WriteByte(in[i])
			i++
			continue
		}
		// A CSI sequence is ESC [ then parameter bytes, then a final byte in
		// 0x40–0x7e. Anything else after ESC is passed through untouched.
		j := i + 1
		if j >= len(in) {
			s.pending = append([]byte(nil), in[i:]...)
			break
		}
		if in[j] != '[' {
			out.WriteByte(in[i])
			i++
			continue
		}
		j++
		for j < len(in) && in[j] >= 0x30 && in[j] <= 0x3f {
			j++
		}
		if j >= len(in) {
			s.pending = append([]byte(nil), in[i:]...)
			break
		}
		if in[j] >= 0x40 && in[j] <= 0x7e {
			i = j + 1 // drop the whole sequence
			continue
		}
		// Malformed: keep the ESC and let the rest through as text.
		out.WriteByte(in[i])
		i++
	}
	if _, err := s.w.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}
