package builder

import (
	"fmt"
	"os"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Passthrough accepts an already-built payload, which lets us support every framework.
// Flutter, React Native and KMP all emit a .ipa, and this consumes one without knowing which produced it.
type Passthrough struct{ Path string }

// ConfigPrebuilt marks a payload that arrived already built. It records
// provenance rather than a configuration: no project defines a "prebuilt" one,
// so it must never be handed back to a toolchain.
const ConfigPrebuilt = "prebuilt"

func (p *Passthrough) Name() string { return "artifact" }

func (p *Passthrough) Detect(string) (bool, string) { return p.Path != "", p.Path }

func (p *Passthrough) Build(Options) (Result, error) {
	info, err := os.Stat(p.Path)
	if err != nil {
		return Result{}, fmt.Errorf("no artifact at %s", p.Path)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory, not a payload", p.Path)
	}
	switch {
	case strings.HasSuffix(strings.ToLower(p.Path), ".ipa"):
		return Result{PayloadPath: p.Path, Platform: artifact.IOS, Config: ConfigPrebuilt}, nil
	case strings.HasSuffix(strings.ToLower(p.Path), ".apk"):
		return Result{}, fmt.Errorf("Android payloads are not served yet")
	default:
		return Result{}, fmt.Errorf("unrecognized payload %s, expected .ipa", p.Path)
	}
}
