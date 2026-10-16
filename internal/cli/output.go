// Package cli writes command output: text, in color on a terminal, or one
// JSON envelope with --json.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Payload is a command's result. It marshals to JSON, and Human prints it for people.
type Payload interface {
	Human(w io.Writer)
}

type envelope struct {
	OK      bool     `json:"ok"`
	Command string   `json:"command"`
	Data    any      `json:"data,omitempty"`
	Error   *Failure `json:"error,omitempty"`
}

// jsonOutput is set by --json for the whole process.
var jsonOutput bool

func SetJSON(on bool) { jsonOutput = on }

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// colorTo reports whether f gets color: it's a terminal and NO_COLOR is unset.
func colorTo(f *os.File) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return isTTY(f)
}

// textWriter strips the color the renderers always write, unless f should get it.
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

// EmitError writes a failure and returns the exit code. Text goes to stderr,
// and JSON to stdout, where the caller is already parsing.
func EmitError(command string, err error) int {
	return EmitFailure(command, nil, err)
}

// EmitFailure is EmitError for a failed command that has a result to show
// too, like doctor.
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
	return f.exitStatus()
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Don't fail silently when the caller is parsing.
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not encode result: %v\n", err)
	}
}

// Section prints a heading in the human output.
func Section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n\033[1;34m==>\033[0m \033[1m%s\033[0m\n", Clean(title))
}

// Line prints a line for people. The format may carry color, but string
// arguments are Cleaned, since they can come from an untrusted Info.plist.
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

// Clean removes the control characters a terminal would act on, keeping
// newline and tab.
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

// stripANSI drops CSI sequences, like the color codes, holding back one split
// across two writes.
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
		// A CSI sequence is ESC [, parameter bytes, then a final byte in 0x40–0x7e.
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
