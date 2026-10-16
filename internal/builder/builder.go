// Package builder turns a project directory into an .ipa or .apk with Xcode
// or Gradle.
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
	// Container is what Detect found: an Xcode workspace or project, or a
	// Gradle root.
	Container string
	// Config is required. The default is the caller's to choose.
	Config string
	// Scheme selects an iOS build, Module and Flavor an Android one. All are
	// optional.
	Scheme string
	Module string
	Flavor string
	Work   string // scratch space for archives and logs
	Log    func(string)
}

// Result is the payload. Its metadata is read from the payload itself.
type Result struct {
	PayloadPath string
	Platform    artifact.Platform
	Config      string
	LogPath     string
}

type Builder interface {
	// Detect returns the buildable container under dir, or why there is none.
	Detect(dir string) (container string, err error)
	// Build produces the payload. Cancelling ctx kills the toolchain and every
	// process it started, and Build returns ctx.Err().
	Build(ctx context.Context, opts Options) (Result, error)
}

// For returns the builder for a platform. On iOS, mode is "build" (the
// default, incremental) or "archive".
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

// run runs a toolchain command in its own process group, so cancelling ctx
// kills everything it spawned (xcodebuild fans out into clang and
// swift-frontend). A cancelled run returns ctx.Err().
func run(ctx context.Context, stdout, stderr io.Writer, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	ownProcessGroup(cmd)
	// Kill the parent outright if it outlives the group signal by 10 seconds.
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
