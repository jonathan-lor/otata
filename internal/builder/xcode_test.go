package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both lines are copied from a real archive of a freshly generated React
// Native project. The pods have never been installed, and the template ships
// no development team, so the log carries both failures in that order.
const freshReactNativeLog = `
ios/OtataRN.xcodeproj:1:1: error: Unable to open base configuration reference file '/Users/x/otata-rn/ios/Pods/Target Support Files/Pods-OtataRN/Pods-OtataRN.release.xcconfig'. (in target 'OtataRN' from project 'OtataRN')
ios/OtataRN.xcodeproj: error: Signing for "OtataRN" requires a development team. Select a development team in the Signing & Capabilities editor. (in target 'OtataRN' from project 'OtataRN')
`

// The failure to report is the one that happened first, not the one listed
// first in our table. Scanning the table in order reported signing here and
// sent the caller to Apple's developer portal, which fixes nothing. The build
// is one 'pod install' away from working.
func TestDiagnosisPrefersTheEarlierFailure(t *testing.T) {
	err := diagnose(freshReactNativeLog, "archive failed")
	setup, ok := errors.AsType[*SetupError](err)
	if !ok {
		t.Fatalf("got %T (%v), want *SetupError", err, err)
	}
	if setup.Command != "pod install" {
		t.Errorf("command = %q, want 'pod install'", setup.Command)
	}
}

// The same log without its first line must still report signing; otherwise
// the test above would pass just as well with signing detection deleted.
func TestSigningIsStillReportedWhenItIsFirst(t *testing.T) {
	_, after, _ := strings.Cut(freshReactNativeLog, "\n\n")
	if after == "" {
		after = freshReactNativeLog[strings.Index(freshReactNativeLog, "ios/OtataRN.xcodeproj: error: Signing"):]
	}
	err := diagnose(after, "archive failed")
	if _, ok := errors.AsType[*SigningError](err); !ok {
		t.Fatalf("got %T (%v), want *SigningError", err, err)
	}
}

// One hint for every signing failure is wrong. A missing team is a build
// setting, and only the profile and device cases are portal work.
func TestSigningHintsDifferByCause(t *testing.T) {
	noTeam := hintFor(t, `error: Signing for "iosApp" requires a development team.`)
	if strings.Contains(noTeam, "portal") {
		t.Errorf("a missing team sends the caller to Apple's portal: %q", noTeam)
	}
	if !strings.Contains(noTeam, "DEVELOPMENT_TEAM") {
		t.Errorf("hint does not name the setting to change: %q", noTeam)
	}
	noProfile := hintFor(t, `error: No profiles for 'com.example.app' were found`)
	if !strings.Contains(noProfile, "portal") {
		t.Errorf("a missing profile is portal work: %q", noProfile)
	}
}

func hintFor(t *testing.T, log string) string {
	t.Helper()
	signing, ok := errors.AsType[*SigningError](diagnose(log, "archive failed"))
	if !ok {
		t.Fatalf("%q was not classified as signing", log)
	}
	return signing.Hint
}

// An unrecognized failure must stay unrecognized.
func TestUnknownFailureKeepsTheFallback(t *testing.T) {
	err := diagnose("error: use of undeclared identifier 'foo'", "archive failed")
	_, isSigning := errors.AsType[*SigningError](err)
	_, isSetup := errors.AsType[*SetupError](err)
	if isSigning || isSetup {
		t.Fatalf("classified an ordinary compile error as %T", err)
	}
	if err.Error() != "archive failed" {
		t.Errorf("message = %q, want the fallback", err.Error())
	}
}

// touch creates the file and the directories above it.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Flutter checkout is missing Generated.xcconfig because it is gitignored,
// so this is the state of every clone instead of a weird one.
func TestFlutterCheckoutNeedsItsConfigGenerated(t *testing.T) {
	root := t.TempDir()
	ios := filepath.Join(root, "ios")
	touch(t, filepath.Join(root, "pubspec.yaml"))
	touch(t, filepath.Join(ios, "Flutter", "AppFrameworkInfo.plist"))
	container := filepath.Join(ios, "Runner.xcworkspace")

	missing := prerequisite(container)
	if missing == nil {
		t.Fatal("no prerequisite reported")
	}
	if missing.Command != "flutter build ios --config-only" {
		t.Errorf("command = %q", missing.Command)
	}
	// The command has to run where pubspec.yaml is, not in ios/.
	if missing.Dir != root {
		t.Errorf("dir = %q, want the repository root %q", missing.Dir, root)
	}

	touch(t, filepath.Join(ios, "Flutter", "Generated.xcconfig"))
	if missing := prerequisite(container); missing != nil {
		t.Errorf("still reported %v once the config exists", missing)
	}
}

// React Native's default state after a clone: a Podfile, no Pods, and no
// workspace either, so detection archives the .xcodeproj and it cannot link.
func TestPodfileWithoutPodsNeedsInstall(t *testing.T) {
	ios := filepath.Join(t.TempDir(), "ios")
	touch(t, filepath.Join(ios, "Podfile"))
	container := filepath.Join(ios, "OtataRN.xcodeproj")

	missing := prerequisite(container)
	if missing == nil {
		t.Fatal("no prerequisite reported")
	}
	if missing.Command != "pod install" || missing.Dir != ios {
		t.Errorf("got %q in %q, want 'pod install' in %q", missing.Command, missing.Dir, ios)
	}

	touch(t, filepath.Join(ios, "Pods", "Manifest.lock"))
	if missing := prerequisite(container); missing != nil {
		t.Errorf("still reported %v once the pods are installed", missing)
	}
}

// A Flutter app with plugins is missing both at once. 'pod install' cannot run
// first: Flutter's Podfile reads the xcconfig that is not there yet.
func TestFlutterWinsOverPodsWhenBothAreMissing(t *testing.T) {
	root := t.TempDir()
	ios := filepath.Join(root, "ios")
	touch(t, filepath.Join(root, "pubspec.yaml"))
	touch(t, filepath.Join(ios, "Flutter", "AppFrameworkInfo.plist"))
	touch(t, filepath.Join(ios, "Podfile"))

	missing := prerequisite(filepath.Join(ios, "Runner.xcworkspace"))
	if missing == nil || missing.Command != "flutter build ios --config-only" {
		t.Fatalf("got %v, want the Flutter step first", missing)
	}
}

// The three places that name Flutter's setup step say the same thing, and the
// pre-check adds only where to run it.
func TestFlutterSetupIsStatedOnce(t *testing.T) {
	for _, phrase := range []string{
		"flutter_tools/bin/xcode_backend.sh: No such file or directory",
		"Flutter/Generated.xcconfig must exist",
	} {
		setup, ok := errors.AsType[*SetupError](diagnose(phrase, "archive failed"))
		if !ok {
			t.Fatalf("%q was not classified as setup", phrase)
		}
		if *setup != flutterConfigMissing {
			t.Errorf("%q: %+v, want %+v", phrase, *setup, flutterConfigMissing)
		}
	}
	root := t.TempDir()
	touch(t, filepath.Join(root, "ios", "Flutter", "AppFrameworkInfo.plist"))
	want := flutterConfigMissing
	want.Dir = filepath.Join(root, "ios")
	if got := prerequisite(filepath.Join(root, "ios", "Runner.xcworkspace")); got == nil || *got != want {
		t.Errorf("prerequisite = %+v, want %+v", got, want)
	}
}

// The configuration is the caller's to choose and announce; a build with none
// is refused before any toolchain runs, rather than defaulted here as well.
func TestPrepareRequiresAConfiguration(t *testing.T) {
	_, err := (&Xcode{}).prepare(Options{Container: filepath.Join(t.TempDir(), "App.xcodeproj")})
	if err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Errorf("got %v, want a refusal naming the configuration", err)
	}
}

// A native project with neither must be left alone. Reporting a prerequisite
// here would refuse to build a project that builds.
func TestNativeProjectHasNoPrerequisite(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "MyApp.xcodeproj", "project.pbxproj"))
	if missing := prerequisite(filepath.Join(dir, "MyApp.xcodeproj")); missing != nil {
		t.Errorf("reported %v for a plain Xcode project", missing)
	}
}

// Copied from a real Kotlin Multiplatform archive on a Mac with no JDK: the
// Gradle phase runs macOS's java stub, which is not a JDK and says so. Without
// this the whole failure is "archive failed, see the log", and the log is
// tens of thousands of lines of exported build settings.
const noJDKLog = `    export variant\=normal
    /bin/sh -c /Users/x/Library/Developer/Xcode/DerivedData/iosApp-abc/Build/Intermediates.noindex/ArchiveIntermediates/iosApp/Script-F36B.sh
The operation couldn’t be completed. Unable to locate a Java Runtime.
Please visit http://www.java.com for information on installing Java.
** ARCHIVE FAILED **`

func TestGradleWithoutAJDKIsSetupNotBuildFailure(t *testing.T) {
	setup, ok := errors.AsType[*SetupError](diagnose(noJDKLog, "archive failed"))
	if !ok {
		t.Fatal("a missing JDK was not reported as setup")
	}
	if !strings.Contains(setup.Hint, "JDK") {
		t.Errorf("hint = %q, does not name a JDK", setup.Hint)
	}
}
