package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Copied from a fresh React Native project's archive, which fails on the
// missing pods and then on signing.
const freshReactNativeLog = `
ios/OtataRN.xcodeproj:1:1: error: Unable to open base configuration reference file '/Users/x/otata-rn/ios/Pods/Target Support Files/Pods-OtataRN/Pods-OtataRN.release.xcconfig'. (in target 'OtataRN' from project 'OtataRN')
ios/OtataRN.xcodeproj: error: Signing for "OtataRN" requires a development team. Select a development team in the Signing & Capabilities editor. (in target 'OtataRN' from project 'OtataRN')
`

func TestDiagnosisPrefersTheEarlierFailure(t *testing.T) {
	err := xcodeDiagnose(freshReactNativeLog)
	setup, ok := errors.AsType[*SetupError](err)
	if !ok {
		t.Fatalf("got %T (%v), want *SetupError", err, err)
	}
	if setup.Command != "pod install" {
		t.Errorf("command = %q, want 'pod install'", setup.Command)
	}
}

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

// xcodeDiagnose scans a log as the Xcode builder does.
func xcodeDiagnose(text string) error {
	return diagnose(text, "archive failed", xcodeDiagnoses, gradleDiagnoses)
}

func hintFor(t *testing.T, log string) string {
	t.Helper()
	signing, ok := errors.AsType[*SigningError](xcodeDiagnose(log))
	if !ok {
		t.Fatalf("%q was not classified as signing", log)
	}
	return signing.Hint
}

func TestUnknownFailureKeepsTheFallback(t *testing.T) {
	err := xcodeDiagnose("error: use of undeclared identifier 'foo'")
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

func TestFlutterSetupIsStatedOnce(t *testing.T) {
	for _, phrase := range []string{
		"flutter_tools/bin/xcode_backend.sh: No such file or directory",
		"Flutter/Generated.xcconfig must exist",
	} {
		setup, ok := errors.AsType[*SetupError](xcodeDiagnose(phrase))
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

func TestPrepareRequiresAConfiguration(t *testing.T) {
	_, err := (&Xcode{}).prepare(Options{Container: filepath.Join(t.TempDir(), "App.xcodeproj")})
	if err == nil || !strings.Contains(err.Error(), "configuration") {
		t.Errorf("got %v, want a refusal naming the configuration", err)
	}
}

func TestNativeProjectHasNoPrerequisite(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "MyApp.xcodeproj", "project.pbxproj"))
	if missing := prerequisite(filepath.Join(dir, "MyApp.xcodeproj")); missing != nil {
		t.Errorf("reported %v for a plain Xcode project", missing)
	}
}

// Copied from a real KMP archive on a Mac with no JDK, where macOS's java
// stub runs in place of one.
const noJDKLog = `    export variant\=normal
    /bin/sh -c /Users/x/Library/Developer/Xcode/DerivedData/iosApp-abc/Build/Intermediates.noindex/ArchiveIntermediates/iosApp/Script-F36B.sh
The operation couldn’t be completed. Unable to locate a Java Runtime.
Please visit http://www.java.com for information on installing Java.
** ARCHIVE FAILED **`

func TestGradleWithoutAJDKIsSetupNotBuildFailure(t *testing.T) {
	setup, ok := errors.AsType[*SetupError](xcodeDiagnose(noJDKLog))
	if !ok {
		t.Fatal("a missing JDK was not reported as setup")
	}
	if !strings.Contains(setup.Hint, "JDK") {
		t.Errorf("hint = %q, does not name a JDK", setup.Hint)
	}
}
