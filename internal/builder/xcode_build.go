package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

/*
XcodeBuild is the default builder. `publish --builder archive` selects the archive path instead.

The archive action rebuilds every target on every run while the build action is incremental.
A device build already signs the app and embeds its provisioning profile, so the .app can be zipped into an .ipa directly.
*/
type XcodeBuild struct{ Xcode }

func (x *XcodeBuild) Name() string { return "xcode-build" }

func (x *XcodeBuild) Build(ctx context.Context, opts Options) (Result, error) {
	ok, container := x.Detect(opts.Dir)
	if !ok {
		return Result{}, fmt.Errorf("no .xcworkspace or .xcodeproj found")
	}
	if missing := prerequisite(container); missing != nil {
		return Result{}, missing
	}
	scheme, err := x.ResolveScheme(container, opts.Scheme)
	if err != nil {
		return Result{}, err
	}
	config := opts.Config
	if config == "" {
		config = "Release"
	}

	if err := os.MkdirAll(opts.Work, 0o755); err != nil {
		return Result{}, err
	}
	logPath := filepath.Join(opts.Work, "xcodebuild.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{}, err
	}
	defer logFile.Close()

	opts.logf("building %s (%s, incremental)", scheme, config)
	args := append([]string{"build"}, projectArgs(container)...)
	args = append(args,
		"-scheme", scheme,
		"-configuration", config,
		"-destination", "generic/platform=iOS",
		"-allowProvisioningUpdates",
		"-skipMacroValidation",
		"ONLY_ACTIVE_ARCH=NO",
	)
	if err := runLogged(ctx, logFile, "xcodebuild", args...); err != nil {
		return Result{LogPath: logPath}, classifyBuildFailure(ctx, logPath, "build failed")
	}

	app, err := builtApp(ctx, container, scheme, config)
	if err != nil {
		return Result{LogPath: logPath}, err
	}

	opts.logf("packaging %s", filepath.Base(app))
	ipa, err := packageIPA(opts.Work, app)
	if err != nil {
		return Result{LogPath: logPath}, err
	}
	return Result{PayloadPath: ipa, Platform: artifact.IOS, Config: config, LogPath: logPath}, nil
}

// builtApp asks xcodebuild where the scheme's app product landed. The answer
// depends on settings this package must not guess (a project can relocate
// TARGET_BUILD_DIR), so it's read back rather than derived.
func builtApp(ctx context.Context, container, scheme, config string) (string, error) {
	args := append([]string{"-showBuildSettings", "-json"}, projectArgs(container)...)
	args = append(args, "-scheme", scheme, "-configuration", config, "-destination", "generic/platform=iOS")
	out, err := exec.CommandContext(ctx, "xcodebuild", args...).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("could not read build settings for %s", scheme)
	}
	app, err := appFromSettings(out)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(app); err != nil {
		return "", fmt.Errorf("built products missing at %s", app)
	}
	return app, nil
}

// appFromSettings picks the app product out of -showBuildSettings -json output.
// Split from the exec so the selection rule is testable.
func appFromSettings(out []byte) (string, error) {
	var entries []struct {
		BuildSettings map[string]string `json:"buildSettings"`
	}
	if err := json.Unmarshal(out, &entries); err != nil {
		return "", fmt.Errorf("could not parse build settings: %w", err)
	}
	for _, e := range entries {
		if e.BuildSettings["WRAPPER_EXTENSION"] != "app" {
			continue
		}
		dir, name := e.BuildSettings["TARGET_BUILD_DIR"], e.BuildSettings["FULL_PRODUCT_NAME"]
		if dir == "" || name == "" {
			continue
		}
		return filepath.Join(dir, name), nil
	}
	return "", fmt.Errorf("no app product in the scheme's build settings")
}

// packageIPA zips the signed .app into Payload/, which is all an .ipa is.
func packageIPA(work, app string) (string, error) {
	staging := filepath.Join(work, "pkg")
	export := filepath.Join(work, "export")
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(export)
	payload := filepath.Join(staging, "Payload")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(export, 0o755); err != nil {
		return "", err
	}
	// ditto rather than a tree copy: bundles carry metadata the signature covers.
	if out, err := exec.Command("ditto", app, filepath.Join(payload, filepath.Base(app))).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ditto failed: %s", strings.TrimSpace(string(out)))
	}
	ipa := filepath.Join(export, strings.TrimSuffix(filepath.Base(app), ".app")+".ipa")
	// -y keeps symlinks as symlinks; a signature does not survive their expansion.
	zipCmd := exec.Command("zip", "-qry", ipa, "Payload")
	zipCmd.Dir = staging
	if out, err := zipCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("zip failed: %s", strings.TrimSpace(string(out)))
	}
	return ipa, nil
}
