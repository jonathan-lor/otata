package cli

import (
	"errors"
	"fmt"
)

// Failure is an error with a stable code for agents to branch on.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Details any    `json:"details,omitempty"`

	// Exit overrides the exit status, like 128+N for a signal. Zero lets the
	// code decide.
	Exit int `json:"-"`
}

func (f *Failure) Error() string {
	// A nil *Failure can still arrive as a non-nil error.
	if f == nil {
		return "unknown failure"
	}
	return f.Message
}

// The error codes. Keep docs/cli-reference.md and skills/otata/SKILL.md in step.
const (
	CodeNoProject       = "no_project"        // nothing buildable here
	CodeAmbiguousScheme = "ambiguous_scheme"  // several candidates, none obvious
	CodeNeedsSetup      = "needs_setup"       // a toolchain prerequisite is missing
	CodeBuildFailed     = "build_failed"      // the toolchain returned non-zero
	CodeSigningFailed   = "signing_failed"    // cert, profile or device registration
	CodeFreeProfile     = "free_profile"      // signed by a personal team; iOS refuses those over the air
	CodeServerDown      = "server_down"       // local server not running
	CodeTransportDown   = "transport_down"    // transport present but unusable
	CodeNoTransport     = "no_transport"      // unknown transport, or manual without a base URL
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

// exitStatus is Exit if set, else 2 for a usage error and 1 for anything else.
func (f *Failure) exitStatus() int {
	switch {
	case f.Exit != 0:
		return f.Exit
	case f.Code == CodeInvalidArgs:
		return 2
	}
	return 1
}

// AsFailure turns any error into a Failure, so it can still go out as JSON.
func AsFailure(err error) *Failure {
	if err == nil {
		return Fail(CodeInternal, "an unspecified error occurred")
	}
	if f, ok := errors.AsType[*Failure](err); ok && f != nil {
		return f
	}
	return Fail(CodeInternal, err.Error())
}
