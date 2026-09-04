// Package builder turns a project directory into a payload.
//
// Builders are pluggable and detected. Nothing outside this package knows what
// Gradle or CocoaPods are, and --artifact bypasses this layer entirely.
package builder

import (
	"context"
	"fmt"

	"github.com/jonathan-lor/otata/internal/artifact"
)

type Options struct {
	Dir    string // project directory
	Config string // Debug / Release
	Scheme string // optional override
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
	Name() string
	// Detect reports whether this builder can handle dir, and where the
	// buildable thing actually lives. Cross-platform frameworks keep it in a
	// subdirectory rather than at the repository root.
	Detect(dir string) (bool, string)
	// Build produces the payload. Cancelling ctx stops the toolchain along
	// with every process it started, and Build returns ctx.Err().
	Build(ctx context.Context, opts Options) (Result, error)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, args...))
	}
}
