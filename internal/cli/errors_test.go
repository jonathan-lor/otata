package cli

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// AsFailure's should not panic on nil.
func TestAsFailureHandlesNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("AsFailure(nil) panicked: %v", r)
		}
	}()
	f := AsFailure(nil)
	if f == nil || f.Code != CodeInternal {
		t.Errorf("AsFailure(nil) = %+v", f)
	}
}

// A nil *Failure in an error interface is non-nil, so Error() must not
// dereference; otherwise the first such return panics instead of reporting.
func TestNilFailureErrorDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("(*Failure)(nil).Error() panicked: %v", r)
		}
	}()
	var f *Failure
	var err error = f
	_ = err.Error()
	if got := AsFailure(err); got == nil {
		t.Error("AsFailure returned nil for a typed-nil error")
	}
}

func TestAsFailurePreservesCode(t *testing.T) {
	original := Fail(CodeNoProject, "nothing here").WithHint("cd somewhere else")
	wrapped := fmt.Errorf("while publishing: %w", original)
	got := AsFailure(wrapped)
	if got.Code != CodeNoProject {
		t.Errorf("code = %q, want %q", got.Code, CodeNoProject)
	}
	if got.Hint == "" {
		t.Error("hint was lost through wrapping")
	}
	if !errors.Is(wrapped, error(original)) {
		t.Error("errors.Is could not match the wrapped failure")
	}
}

// The exit status is 2 for a usage error, 1 otherwise, and a failure may
// carry its own: a signal's, which a shell expects as 128 plus its number.
func TestExitStatus(t *testing.T) {
	cases := []struct {
		name string
		f    *Failure
		want int
	}{
		{"usage error", Fail(CodeInvalidArgs, "x"), 2},
		{"ordinary failure", Fail(CodeBuildFailed, "x"), 1},
		{"a signal's own status", Fail(CodeInterrupted, "x").WithExit(143), 143},
		{"an explicit status wins over the code", Fail(CodeInvalidArgs, "x").WithExit(130), 130},
	}
	for _, c := range cases {
		if got := c.f.exitStatus(); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}

func TestUnclassifiedErrorBecomesInternal(t *testing.T) {
	got := AsFailure(errors.New("something went wrong"))
	if got.Code != CodeInternal || got.Message != "something went wrong" {
		t.Errorf("got %+v", got)
	}
}

// A string argument to Line comes from somewhere like an Info.plist, which is
// untrusted for --artifact. The format string is ours and keeps its color;
// the values lose anything a terminal would act on.
func TestLineStripsControlCharactersFromValues(t *testing.T) {
	var out strings.Builder
	Line(&out, "\033[1;32m%s\033[0m %s", "Notes\x1b]0;pwned\x07\x1b[2K", "v1.0\r\n\u009bhidden")
	got := out.String()
	if !strings.HasPrefix(got, "    \033[1;32mNotes") {
		t.Errorf("our own color was stripped or the prefix changed: %q", got)
	}
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2K", "\r", "\u009b"} {
		if strings.Contains(strings.TrimPrefix(got, "    \033[1;32m"), bad) {
			t.Errorf("control sequence %q survived into terminal output: %q", bad, got)
		}
	}
	// Only the control characters go.
	if !strings.Contains(got, "Notes]0;pwned[2K") || !strings.Contains(got, "v1.0\nhidden") {
		t.Errorf("printable text was damaged: %q", got)
	}
	// Non-string arguments and plain strings pass through untouched.
	out.Reset()
	Line(&out, "%d %s %.1f", 3, "A & B: 日本語", 1.5)
	if out.String() != "    3 A & B: 日本語 1.5\n" {
		t.Errorf("ordinary values were altered: %q", out.String())
	}
}
