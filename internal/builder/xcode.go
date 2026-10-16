package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Xcode builds an iOS app by archiving and exporting it.
type Xcode struct{}

// searchDirs is the repository root and the subdirectories frameworks use.
func searchDirs(dir string) []string {
	return []string{dir, filepath.Join(dir, "ios"), filepath.Join(dir, "iosApp"), filepath.Join(dir, "apps", "ios")}
}

// Detect prefers a workspace because a project inside one generally cannot be built on its own.
func (x *Xcode) Detect(dir string) (string, error) {
	for _, d := range searchDirs(dir) {
		if p := firstMatch(d, ".xcworkspace"); p != "" {
			return p, nil
		}
	}
	for _, d := range searchDirs(dir) {
		if p := firstMatch(d, ".xcodeproj"); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("no .xcworkspace or .xcodeproj found here")
}

func firstMatch(dir, ext string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ext) {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

func projectArgs(container string) []string {
	if strings.HasSuffix(container, ".xcworkspace") {
		return []string{"-workspace", container}
	}
	return []string{"-project", container}
}

// flutterConfigMissing is shared by the pre-check and two log phrases.
var flutterConfigMissing = SetupError{
	Detail:  "Flutter has not generated its build configuration for this project",
	Command: "flutter build ios --config-only",
	Hint:    "that xcconfig is generated, not committed, so a fresh clone never has it",
}

// xcodeDiagnoses are the xcodebuild failures with a known remedy.
var xcodeDiagnoses = []diagnosis{
	// Signing.
	{phrase: "requires a development team", signing: true,
		hint: "select a team in Xcode under Signing & Capabilities, or set DEVELOPMENT_TEAM in the target's build settings; cross-platform templates usually read it from an xcconfig instead (Kotlin Multiplatform: TEAM_ID in iosApp/Configuration/Config.xcconfig)"},
	{phrase: "No profiles for", signing: true,
		hint: "register the device and update the provisioning profile in Apple's developer portal"},
	{phrase: "Failed to register bundle identifier", signing: true,
		hint: "the bundle identifier is taken or cannot be registered: change it, or register it in Apple's developer portal"},
	{phrase: "Unable to process request - PLA Update available", signing: true,
		hint: "accept the updated Apple Developer Program License Agreement at developer.apple.com, then retry"},
	{phrase: "no valid signing identity", signing: true,
		hint: "this machine's keychain holds no usable certificate for the team: sign in to Xcode's Accounts pane, or install the certificate"},
	{phrase: "doesn't include signing certificate", signing: true,
		hint: "the profile does not list this machine's certificate: regenerate it in Apple's developer portal after adding the certificate"},
	{phrase: "Provisioning profile", signing: true,
		hint: "the provisioning profile does not fit this build: regenerate it in Apple's developer portal"},
	{phrase: "Code Signing Error", signing: true,
		hint: "read the signing settings on the target; the log names what was rejected"},

	// Setup. Without Generated.xcconfig, FLUTTER_ROOT is empty and Flutter's
	// failure names a rootless path. CocoaPods says it more plainly when it runs first.
	{phrase: "flutter_tools/bin/xcode_backend.sh: No such file or directory", setup: flutterConfigMissing},
	{phrase: "Flutter/Generated.xcconfig must exist", setup: flutterConfigMissing},
	{phrase: "The sandbox is not in sync with the Podfile.lock", setup: SetupError{
		Detail:  "the installed pods no longer match the Podfile",
		Command: "pod install"}},
	{phrase: "Unable to open base configuration reference file", setup: SetupError{
		Detail:  "a build configuration file the project references is missing",
		Command: "pod install",
		Hint:    "in a CocoaPods project that file is written by the install"}},
}

// Ambiguous is a choice discovery couldn't make, with the candidates and
// the flag that chooses.
type Ambiguous struct {
	Detail     string // what could not be chosen, as the message
	Flag       string // the flag that chooses: --scheme, --module or --flavor
	Candidates []string
}

func (e *Ambiguous) Error() string { return e.Detail }

// Schemes lists what xcodebuild reports, narrowed to schemes that actually archive an app.
func (x *Xcode) Schemes(container string) ([]string, error) {
	out, err := exec.Command("xcodebuild", append([]string{"-list", "-json"}, projectArgs(container)...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("could not list schemes for %s", filepath.Base(container))
	}
	var listing struct {
		Project   struct{ Schemes []string } `json:"project"`
		Workspace struct{ Schemes []string } `json:"workspace"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		return nil, fmt.Errorf("could not parse scheme list: %w", err)
	}
	all := listing.Project.Schemes
	if len(all) == 0 {
		all = listing.Workspace.Schemes
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no schemes in %s", filepath.Base(container))
	}

	// Narrow to schemes whose files archive an app. Package schemes have no
	// file, and if no file reads, the list stays as it is.
	if apps := appSchemes(container); len(apps) > 0 {
		var kept []string
		for _, name := range all {
			if apps[name] {
				kept = append(kept, name)
			}
		}
		if len(kept) > 0 {
			all = kept
		}
	}
	return all, nil
}

// ResolveScheme applies the selection rule: an explicit choice, else one named
// after the project, else a lone candidate, else refuse and list.
func (x *Xcode) ResolveScheme(container, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	names, err := x.Schemes(container)
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(container), filepath.Ext(container))
	for _, n := range names {
		if n == base {
			return n, nil
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	return "", &Ambiguous{
		Detail: fmt.Sprintf("several schemes and none named after %s", filepath.Base(container)),
		Flag:   "--scheme", Candidates: names,
	}
}

type schemeFile struct {
	Entries []struct {
		BuildForArchiving string `xml:"buildForArchiving,attr"`
		Reference         struct {
			BuildableName string `xml:"BuildableName,attr"`
		} `xml:"BuildableReference"`
	} `xml:"BuildAction>BuildActionEntries>BuildActionEntry"`
}

// appSchemes returns the schemes that archive a .app, which excludes a
// framework whose UI tests merely name a host app.
func appSchemes(container string) map[string]bool {
	found := map[string]bool{}
	roots := []string{container}
	if parent := filepath.Dir(container); parent != "" {
		if p := firstMatch(parent, ".xcodeproj"); p != "" && p != container {
			roots = append(roots, p)
		}
	}
	for _, root := range roots {
		for _, sub := range []string{"xcshareddata/xcschemes", "xcuserdata"} {
			_ = filepath.WalkDir(filepath.Join(root, sub), func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(p, ".xcscheme") {
					return nil
				}
				raw, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				var sf schemeFile
				if xml.Unmarshal(raw, &sf) != nil {
					return nil
				}
				for _, e := range sf.Entries {
					if strings.EqualFold(e.BuildForArchiving, "YES") &&
						strings.HasSuffix(e.Reference.BuildableName, ".app") {
						found[strings.TrimSuffix(filepath.Base(p), ".xcscheme")] = true
						break
					}
				}
				return nil
			})
		}
	}
	return found
}

// prerequisite returns a framework setup step that hasn't run, or nil. It
// checks the disk before a build that would take minutes to fail.
func prerequisite(container string) *SetupError {
	dir := filepath.Dir(container)
	root := filepath.Dir(dir)

	// Flutter first, since its Podfile reads Generated.xcconfig.
	// AppFrameworkInfo.plist is the committed file that marks a Flutter project.
	if exists(filepath.Join(dir, "Flutter", "AppFrameworkInfo.plist")) &&
		!exists(filepath.Join(dir, "Flutter", "Generated.xcconfig")) {
		missing := flutterConfigMissing
		missing.Dir = dir
		if exists(filepath.Join(root, "pubspec.yaml")) {
			missing.Dir = root
		}
		return &missing
	}

	// React Native ships a Podfile with nothing installed.
	if exists(filepath.Join(dir, "Podfile")) && !exists(filepath.Join(dir, "Pods", "Manifest.lock")) {
		return &SetupError{
			Detail:  "the project's pods are not installed",
			Command: "pod install",
			Dir:     dir,
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// job is a resolved Xcode build: the scheme, the configuration and an open log.
type job struct {
	container, scheme, config, logPath string
	log                                *os.File
}

// prepare resolves the job, refusing it if a prerequisite is missing.
func (x *Xcode) prepare(opts Options) (*job, error) {
	if opts.Config == "" {
		return nil, fmt.Errorf("no build configuration given")
	}
	if missing := prerequisite(opts.Container); missing != nil {
		return nil, missing
	}
	scheme, err := x.ResolveScheme(opts.Container, opts.Scheme)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Work, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(opts.Work, "xcodebuild.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	return &job{container: opts.Container, scheme: scheme, config: opts.Config, logPath: logPath, log: logFile}, nil
}

// args is the xcodebuild invocation both builders share.
func (j *job) args(action string, extra ...string) []string {
	args := append([]string{action}, projectArgs(j.container)...)
	args = append(args,
		"-scheme", j.scheme,
		"-configuration", j.config,
		"-destination", "generic/platform=iOS",
		"-allowProvisioningUpdates",
		"-skipMacroValidation",
		"ONLY_ACTIVE_ARCH=NO",
	)
	return append(args, extra...)
}

func (j *job) result(payload string) Result {
	return Result{PayloadPath: payload, Platform: artifact.IOS, Config: j.config, LogPath: j.logPath}
}

// failure checks Gradle's phrases too, since KMP runs Gradle inside xcodebuild.
func (j *job) failure(ctx context.Context, fallback string) error {
	return classifyBuildFailure(ctx, j.logPath, fallback, xcodeDiagnoses, gradleDiagnoses)
}

func (x *Xcode) Build(ctx context.Context, opts Options) (Result, error) {
	j, err := x.prepare(opts)
	if err != nil {
		return Result{}, err
	}
	defer j.log.Close()
	failed := Result{LogPath: j.logPath}

	archive := filepath.Join(opts.Work, j.scheme+".xcarchive")
	export := filepath.Join(opts.Work, "export")
	_ = os.RemoveAll(archive)
	_ = os.RemoveAll(export)

	opts.logf("archiving %s (%s)", j.scheme, j.config)
	if err := runLogged(ctx, j.log, "xcodebuild", j.args("archive", "-archivePath", archive)...); err != nil {
		return failed, j.failure(ctx, "archive failed")
	}

	team, err := archiveTeam(archive)
	if err != nil {
		return failed, err
	}
	optionsPath := filepath.Join(opts.Work, "ExportOptions.plist")
	if err := os.WriteFile(optionsPath, exportOptions(team), 0o644); err != nil {
		return failed, err
	}

	opts.logf("exporting (team %s)", team)
	if err := runLogged(ctx, j.log, "xcodebuild", "-exportArchive",
		"-archivePath", archive,
		"-exportOptionsPlist", optionsPath,
		"-exportPath", export,
		"-allowProvisioningUpdates",
	); err != nil {
		return failed, j.failure(ctx, "export failed")
	}

	ipa := firstMatch(export, ".ipa")
	if ipa == "" {
		return failed, fmt.Errorf("no .ipa produced in %s", export)
	}
	return j.result(ipa), nil
}

// archiveTeam reads the signing team out of the archive.
func archiveTeam(archive string) (string, error) {
	out, err := exec.Command("/usr/libexec/PlistBuddy",
		"-c", "Print :ApplicationProperties:Team", filepath.Join(archive, "Info.plist")).Output()
	team := strings.TrimSpace(string(out))
	if err != nil || team == "" {
		return "", fmt.Errorf("could not read the signing team from the archive")
	}
	return team, nil
}

// exportOptions uses "debugging", the Xcode 16+ name for what was
// "development". Ad-hoc would need an Apple Distribution certificate.
func exportOptions(team string) []byte {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(team))
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>method</key><string>debugging</string>
    <key>teamID</key><string>` + escaped.String() + `</string>
    <key>signingStyle</key><string>automatic</string>
    <key>stripSwiftSymbols</key><true/>
    <key>destination</key><string>export</string>
    <key>thinning</key><string>&lt;none&gt;</string>
</dict>
</plist>
`)
}
