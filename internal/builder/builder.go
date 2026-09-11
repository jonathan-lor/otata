// Package builder turns a project directory into a payload.
//
// One builder per platform and mode, chosen by For. Nothing outside this
// package knows what Xcode, Gradle or CocoaPods are, and a payload built by
// anything else arrives through Prebuilt, which bypasses the builders entirely.
package builder

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

type Options struct {
	// Container is the buildable thing Detect found: the workspace or project
	// for Xcode, the Gradle root for Android. Build is handed it rather than
	// looking again.
	Container string
	// Config is the build configuration, Debug or Release. Required: the
	// default is the caller's to choose and announce, not this package's.
	Config string
	// Scheme is the Xcode scheme to build and is iOS only. Module and
	// Flavor are the Gradle module and product flavor and Android only. Each is
	// optional, and the caller refuses the other platform's before a
	// builder sees them.
	Scheme string
	Module string
	Flavor string
	Work   string // scratch space for archives and logs
	Log    func(string)
}

// Result is deliberately just the payload because metadata is always read back out of the payload.
type Result struct {
	PayloadPath string
	Platform    artifact.Platform
	Config      string
	LogPath     string
}

type Builder interface {
	// Detect reports where the buildable thing lives under dir, or why none
	// does. Cross-platform frameworks keep it in a subdirectory rather than
	// at the repository root, which is why the answer is a path and not a bool.
	Detect(dir string) (container string, err error)
	// Build produces the payload from what Detect found. Cancelling ctx stops
	// the toolchain along with every process it started, and Build returns
	// ctx.Err().
	Build(ctx context.Context, opts Options) (Result, error)
}

// For returns the builder for a platform. mode selects among a platform's
// builders where it has several: for iOS, "" or "build" is the incremental
// build and "archive" the archive-and-export path. A platform with no
// builder is refused here, which is the one place a new one is added.
func For(platform artifact.Platform, mode string) (Builder, error) {
	switch platform {
	case artifact.IOS:
		switch mode {
		case "", "build":
			return &XcodeBuild{}, nil
		case "archive":
			return &Xcode{}, nil
		}
		return nil, fmt.Errorf("unknown builder %q; --builder takes archive or build", mode)
	case artifact.Android:
		switch mode {
		case "", "build":
			return &Gradle{}, nil
		}
		return nil, fmt.Errorf("unknown builder %q for an Android build; --builder archive is Xcode's", mode)
	}
	return nil, fmt.Errorf("%s builds are not supported yet.", platform)
}

// run runs a toolchain command with its output in the given writers, from
// dir when one is given. The command gets a process group of its own, and
// cancelling ctx signals that whole group: xcodebuild fans out into clang,
// swift-frontend and script phases, and killing only the parent left those
// writing into the work directory after the publish that started them was
// gone. Cancellation is reported as ctx.Err(), whatever the killed
// process's exit looked like.
func run(ctx context.Context, stdout, stderr io.Writer, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	ownProcessGroup(cmd)
	// After the group is signalled, a parent that has not exited by then is
	// killed outright rather than waited on forever.
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// runLogged runs a toolchain command with both its streams in log.
func runLogged(ctx context.Context, log *os.File, name string, args ...string) error {
	return run(ctx, log, log, "", name, args...)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, args...))
	}
}
