package builder

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// ConfigPrebuilt marks a payload that arrived already built. It records
// provenance rather than a configuration: no project defines a "prebuilt" one,
// so it must never be handed back to a toolchain.
const ConfigPrebuilt = "prebuilt"

// Prebuilt accepts an already-built payload, which is what lets every
// framework through: Flutter, React Native and KMP all emit an .ipa or an
// .apk, and this consumes one without knowing which produced it. The
// extension is what says which platform it is. It is not a Builder: there
// is nothing to detect and nothing to build, only a file to check.
func Prebuilt(path string) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("no artifact at %s", path)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory, not a payload", path)
	}
	switch {
	case strings.HasSuffix(strings.ToLower(path), ".ipa"):
		return Result{PayloadPath: path, Platform: artifact.IOS, Config: ConfigPrebuilt}, nil
	case strings.HasSuffix(strings.ToLower(path), ".apk"):
		return Result{PayloadPath: path, Platform: artifact.Android, Config: ConfigPrebuilt}, nil
	default:
		return Result{}, fmt.Errorf("unrecognized payload %s, expected .ipa or .apk", path)
	}
}
