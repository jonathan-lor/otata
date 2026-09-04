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
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Xcode builds a native iOS app. iOS payloads require macOS and Xcode
// permanently. That is Apple's constraint, and every cross-platform framework
// routes through xcodebuild too.
type Xcode struct{}

func (x *Xcode) Name() string { return "xcode" }

// searchDirs covers the repository root plus the subdirectories cross-platform
// frameworks use instead of assuming the project sits at top level. React
// Native has no workspace at all until pods are installed.
func searchDirs(dir string) []string {
	return []string{dir, filepath.Join(dir, "ios"), filepath.Join(dir, "iosApp"), filepath.Join(dir, "apps", "ios")}
}

// Detect prefers a workspace because a project inside one generally cannot be built on its own.
func (x *Xcode) Detect(dir string) (bool, string) {
	for _, d := range searchDirs(dir) {
		if p := firstMatch(d, ".xcworkspace"); p != "" {
			return true, p
		}
	}
	for _, d := range searchDirs(dir) {
		if p := firstMatch(d, ".xcodeproj"); p != "" {
			return true, p
		}
	}
	return false, ""
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

// SigningError distinguishes a failure a human must fix in Apple's portal from
// one the code can fix. an agent escalates the first and retries the second.
//
// Hint is the remedy for the phrase that matched, not for signing in general.
// One hint for all of them sent a project with no team selected to the portal,
// where there is nothing to do.
type SigningError struct {
	Detail string
	Hint   string
}

func (e *SigningError) Error() string { return "code signing failed: " + e.Detail }

// SetupError reports a step the project's own toolchain must run before
// xcodebuild can archive: pods installed, an xcconfig generated, a JDK for a
// Gradle phase. Not a build failure: the code is fine and the log holds nothing
// to act on. Not "no project" either. One command fixes it, so the command is
// carried instead of described.
type SetupError struct {
	Detail  string // what is missing
	Hint    string // what to do about it, when no single command says it
	Command string // the command that fixes it, when one does
	Dir     string // where to run it, empty when the log cannot say
}

func (e *SetupError) Error() string { return e.Detail }

// diagnosis pairs a phrase a failing build prints with the remedy for it.
type diagnosis struct {
	phrase string
	// signing marks what only a human with Xcode or portal access can resolve.
	// The rest are steps a caller can run and then retry, which is the whole
	// reason the two are different codes.
	signing bool
	// detail states the problem for a setup failure; a signing failure states
	// itself, so the matched phrase is its detail.
	detail string
	hint   string
	// command is the setup step to run, for the ones that have one.
	command string
}

/*
diagnoses is scanned by position in the log, not in the order written here.
Whichever phrase appears FIRST in the build output wins.

That matters for cross-platform projects. A fresh checkout fails for two
reasons at once (the framework's setup step has never run, and the template
ships no development team), so a table-ordered scan reported signing and sent
the caller to Apple's portal for something 'pod install' fixes.
*/
var diagnoses = []diagnosis{
	// Signing. Each of these has a different remedy, which is why each carries its own hint
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

	// Setup. Each is phrased as the toolchain phrases it. With no
	// Generated.xcconfig there is no FLUTTER_ROOT either, so Flutter's build
	// phase resolves to an absolute path with an empty prefix, which is what
	// the failure actually looks like. The tidier message below appears only
	// when CocoaPods reads the Podfile first.
	{phrase: "flutter_tools/bin/xcode_backend.sh: No such file or directory",
		detail:  "Flutter has not generated its build configuration for this project",
		command: "flutter build ios --config-only",
		hint:    "that xcconfig is generated, not committed, so a fresh clone never has it"},
	{phrase: "Flutter/Generated.xcconfig must exist",
		detail:  "Flutter has not generated its build configuration for this project",
		command: "flutter build ios --config-only",
		hint:    "that xcconfig is generated, not committed"},
	{phrase: "The sandbox is not in sync with the Podfile.lock",
		detail:  "the installed pods no longer match the Podfile",
		command: "pod install"},
	{phrase: "Unable to open base configuration reference file",
		detail:  "a build configuration file the project references is missing",
		command: "pod install",
		hint:    "in a CocoaPods project that file is written by the install"},
	{phrase: "Unable to locate a Java Runtime",
		detail: "this build runs Gradle, and no JDK is installed",
		hint:   "install a JDK 17 or newer"},
	{phrase: "JAVA_HOME is not set",
		detail: "this build runs Gradle, and no JDK is on PATH",
		hint:   "install a JDK 17 or newer, or set JAVA_HOME"},
	{phrase: "SDK location not found",
		detail: "the Gradle build has an Android target and no Android SDK is configured",
		hint:   "install the Android SDK and set ANDROID_HOME, or write sdk.dir into local.properties"},
}

// classifyBuildFailure reads the tail of the build log and names what failed.
// A build that was cancelled failed because it was told to, and its log says
// nothing about the code, so the cancellation is reported instead.
func classifyBuildFailure(ctx context.Context, logPath, fallback string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("%s", fallback)
	}
	text := string(raw)
	if len(text) > 200_000 {
		text = text[len(text)-200_000:] // the failure is at the end
	}
	return diagnose(text, fallback)
}

// diagnose is the log-reading half, split out so it can be tested against real
// build output without a build.
func diagnose(text, fallback string) error {
	best := -1
	var match diagnosis
	for _, d := range diagnoses {
		i := strings.Index(text, d.phrase)
		if i < 0 || (best >= 0 && i >= best) {
			continue
		}
		best, match = i, d
	}
	if best < 0 {
		return fmt.Errorf("%s", fallback)
	}
	if match.signing {
		return &SigningError{Detail: match.phrase, Hint: match.hint}
	}
	return &SetupError{Detail: match.detail, Hint: match.hint, Command: match.command}
}

// AmbiguousScheme carries the candidates so a caller can choose without re-running discovery.
type AmbiguousScheme struct {
	Project    string
	Candidates []string
}

func (e *AmbiguousScheme) Error() string {
	return fmt.Sprintf("several schemes and none named after %s", filepath.Base(e.Project))
}

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

	// Narrow to app-producing schemes when the scheme files are readable. Package
	// schemes are auto-generated with no file on disk, which is what we want to
	// drop. Failing open matters because unreadable must not narrow to an empty set.
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
	return "", &AmbiguousScheme{Project: container, Candidates: names}
}

type schemeFile struct {
	Entries []struct {
		BuildForArchiving string `xml:"buildForArchiving,attr"`
		Reference         struct {
			BuildableName string `xml:"BuildableName,attr"`
		} `xml:"BuildableReference"`
	} `xml:"BuildAction>BuildActionEntries>BuildActionEntry"`
}

// appSchemes returns scheme names whose build action archives a .app. Matching
// on ".app" alone would catch a framework scheme whose UI tests merely name a
// host app, so the entry must archive as well.
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

// prerequisite reports a step the project's own toolchain must run before
// xcodebuild can archive it, or nil when nothing is missing.
//
// Asked before the archive rather than read out of the log afterwards: the
// answer is already on disk, and the archive it replaces takes minutes to fail.
// It also survives what the log cannot say: a fresh clone fails for two
// reasons, and the loudest is rarely the one to fix first.
func prerequisite(container string) *SetupError {
	dir := filepath.Dir(container)
	root := filepath.Dir(dir)

	// Flutter before CocoaPods because its Podfile reads Generated.xcconfig, so a
	// missing xcconfig is also why the pods are absent, and 'pod install' alone
	// fixes neither. AppFrameworkInfo.plist is the committed half of the same
	// directory, so its presence is what makes this a Flutter project.
	if exists(filepath.Join(dir, "Flutter", "AppFrameworkInfo.plist")) &&
		!exists(filepath.Join(dir, "Flutter", "Generated.xcconfig")) {
		where := dir
		if exists(filepath.Join(root, "pubspec.yaml")) {
			where = root
		}
		return &SetupError{
			Detail:  "Flutter has not generated its build configuration for this project",
			Command: "flutter build ios --config-only",
			Dir:     where,
			Hint:    "that xcconfig is generated, not committed, so a fresh clone never has it",
		}
	}

	// A Podfile with nothing installed is React Native's default state.
	// The workspace CocoaPods writes does not exist yet either, so detection
	// falls back to the .xcodeproj and archives something that cannot link.
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

func (x *Xcode) Build(ctx context.Context, opts Options) (Result, error) {
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
	archive := filepath.Join(opts.Work, scheme+".xcarchive")
	export := filepath.Join(opts.Work, "export")
	logPath := filepath.Join(opts.Work, "xcodebuild.log")
	_ = os.RemoveAll(archive)
	_ = os.RemoveAll(export)

	logFile, err := os.Create(logPath)
	if err != nil {
		return Result{}, err
	}
	defer logFile.Close()

	opts.logf("archiving %s (%s)", scheme, config)
	args := append([]string{"archive"}, projectArgs(container)...)
	args = append(args,
		"-scheme", scheme,
		"-configuration", config,
		"-destination", "generic/platform=iOS",
		"-archivePath", archive,
		"-allowProvisioningUpdates",
		"-skipMacroValidation",
		"ONLY_ACTIVE_ARCH=NO",
	)
	if err := runLogged(ctx, logFile, "xcodebuild", args...); err != nil {
		return Result{LogPath: logPath}, classifyBuildFailure(ctx, logPath, "archive failed")
	}

	team, err := archiveTeam(archive)
	if err != nil {
		return Result{LogPath: logPath}, err
	}
	optionsPath := filepath.Join(opts.Work, "ExportOptions.plist")
	if err := os.WriteFile(optionsPath, exportOptions(team), 0o644); err != nil {
		return Result{LogPath: logPath}, err
	}

	opts.logf("exporting (team %s)", team)
	if err := runLogged(ctx, logFile, "xcodebuild", "-exportArchive",
		"-archivePath", archive,
		"-exportOptionsPlist", optionsPath,
		"-exportPath", export,
		"-allowProvisioningUpdates",
	); err != nil {
		return Result{LogPath: logPath}, classifyBuildFailure(ctx, logPath, "export failed")
	}

	ipa := firstMatch(export, ".ipa")
	if ipa == "" {
		return Result{LogPath: logPath}, fmt.Errorf("no .ipa produced in %s", export)
	}
	return Result{PayloadPath: ipa, Platform: artifact.IOS, Config: config, LogPath: logPath}, nil
}

// runLogged runs a toolchain command with its output in log. The command gets
// a process group of its own, and cancelling ctx signals that whole group:
// xcodebuild fans out into clang, swift-frontend and script phases, and
// killing only the parent left those writing into the work directory after
// the publish that started them was gone. Cancellation is reported as
// ctx.Err(), whatever the killed process's exit looked like.
func runLogged(ctx context.Context, log *os.File, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = log
	cmd.Stderr = log
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

// archiveTeam reads the signing team out of the archive, so nothing is
// hardcoded and any team's project builds.
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
	// The team is read out of an archive, so it is data, not a constant.
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
