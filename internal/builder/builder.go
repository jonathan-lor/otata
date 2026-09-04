// Package builder turns a project directory into a payload.
//
// One builder per platform and mode, chosen by For. Nothing outside this
// package knows what Xcode, Gradle or CocoaPods are, and a payload built by
// anything else arrives through Prebuilt, which bypasses the builders entirely.
package builder

import (
	"context"
	"fmt"

	"github.com/jonathan-lor/otata/internal/artifact"
)

type Options struct {
	// Container is the buildable thing Detect found: the workspace or project
	// for Xcode. Build is handed it rather than looking again.
	Container string
	Config    string // Debug / Release
	Scheme    string // optional override
	Work      string // scratch space for archives and logs
	Log       func(string)
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
	}
	return nil, fmt.Errorf("%s builds are not supported yet", platform)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, args...))
	}
}
