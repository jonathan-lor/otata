package cli

import (
	"errors"
	"fmt"
)

// Failure is the machine-readable half of an error. Codes are a small closed
// set an agent can branch on, and a new failure mode should get a new code.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`

	// Exit overrides the status the process exits with, for a failure whose
	// convention predates otata's: a command a signal stopped exits 128 plus
	// the signal's number, which is what a shell reports for one it killed.
	// Zero means the code decides.
	Exit int `json:"-"`
}

func (f *Failure) Error() string {
	// A nil *Failure in an error interface is non-nil, so this must not
	// dereference; otherwise the first such return panics instead of reporting.
	if f == nil {
		return "unknown failure"
	}
	return f.Message
}

// The closed set of error codes. Keep in sync with the table of codes in docs/cli-reference.md.
const (
	CodeNoProject       = "no_project"        // nothing buildable here
	CodeAmbiguousScheme = "ambiguous_scheme"  // several candidates, none obvious
	CodeNeedsSetup      = "needs_setup"       // a prerequisite step of the project's toolchain has not been run
	CodeBuildFailed     = "build_failed"      // the toolchain returned non-zero
	CodeSigningFailed   = "signing_failed"    // cert, profile or device registration
	CodeFreeProfile     = "free_profile"      // signed by a personal team; iOS refuses those over the air
	CodeServerDown      = "server_down"       // local server not running
	CodeTransportDown   = "transport_down"    // transport present but unusable
	CodeNoTransport     = "no_transport"      // no usable transport detected
	CodeSlugConflict    = "slug_conflict"     // another project owns this slug
	CodeBuildInProgress = "build_in_progress" // a live publish already holds this slug
	CodeNotFound        = "not_found"         // no such published app
	CodeUnhealthy       = "unhealthy"         // doctor found something wrong
	CodeInterrupted     = "interrupted"       // a signal stopped the command; nothing is left half-done
	CodeInvalidArgs     = "invalid_args"      // caller's mistake
	CodeInternal        = "internal"          // ours
)

func Fail(code, message string) *Failure {
	return &Failure{Code: code, Message: message}
}

func Failf(code, format string, args ...any) *Failure {
	return &Failure{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithHint attaches the action a caller should take next.
func (f *Failure) WithHint(hint string) *Failure { f.Hint = hint; return f }

// WithDetails attaches structured context: a scheme list, a log path.
func (f *Failure) WithDetails(d any) *Failure { f.Details = d; return f }

// WithExit sets the exit status, for the failures that carry their own.
func (f *Failure) WithExit(status int) *Failure { f.Exit = status; return f }

// exitStatus is what the process exits with for this failure: an explicit
// status if one was set, else 2 for a usage error and 1 for everything else.
func (f *Failure) exitStatus() int {
	switch {
	case f.Exit != 0:
		return f.Exit
	case f.Code == CodeInvalidArgs:
		return 2
	}
	return 1
}

// AsFailure coerces any error into one, so an unclassified error still reaches
// the caller as valid JSON rather than as a panic or bare text.
func AsFailure(err error) *Failure {
	// Prevent panic on a nil error.
	if err == nil {
		return Fail(CodeInternal, "an unspecified error occurred")
	}
	if f, ok := errors.AsType[*Failure](err); ok && f != nil {
		return f
	}
	return Fail(CodeInternal, err.Error())
}
