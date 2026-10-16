package builder

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// ConfigPrebuilt is the config recorded for an --artifact payload.
const ConfigPrebuilt = "prebuilt"

// Prebuilt checks an already-built payload, whose extension gives its platform.
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
