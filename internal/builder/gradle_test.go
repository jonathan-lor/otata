package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Detect answers with the Gradle root, which is where the settings file is:
// the repository root, or android/ under a framework's.
func TestGradleDetectFindsTheSettingsFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := (&Gradle{}).Detect(dir); err == nil || !strings.Contains(err.Error(), "settings.gradle") {
		t.Errorf("an empty directory: %v, want a refusal naming the settings file", err)
	}
	touch(t, filepath.Join(dir, "android", "settings.gradle"))
	if root, err := (&Gradle{}).Detect(dir); err != nil || root != filepath.Join(dir, "android") {
		t.Errorf("Detect = %q, %v; want android/", root, err)
	}
	touch(t, filepath.Join(dir, "settings.gradle.kts"))
	if root, err := (&Gradle{}).Detect(dir); err != nil || root != dir {
		t.Errorf("Detect = %q, %v; want the root, which wins over android/", root, err)
	}
}

// What the init script printed for a two-flavor module, with the rest of
// what Gradle says around it, copied from a run against a real project.
const scratchListing = "Fetching distribution.\n" +
	"otata-variant\t:app\tfreeDebug\tfree\tdebug\tsigned\n" +
	"otata-variant\t:app\tpaidDebug\tpaid\tdebug\tsigned\n" +
	"otata-variant\t:app\tfreeRelease\tfree\trelease\tunsigned\n" +
	"otata-variant\t:app\tpaidRelease\tpaid\trelease\tunsigned\n" +
	"otata-module\t:app\t/home/x/flavored/app/build\n" +
	"\nWelcome to Gradle 9.7.1.\n\nTo run a build, run gradlew <task> ...\n"

func TestParseListingReadsModulesAndVariants(t *testing.T) {
	l := parseListing([]byte(scratchListing))
	if len(l.modules) != 1 || l.modules[":app"] != "/home/x/flavored/app/build" {
		t.Errorf("modules = %v", l.modules)
	}
	if len(l.variants) != 4 {
		t.Fatalf("variants = %v", l.variants)
	}
	want := variant{module: ":app", name: "freeRelease", flavor: "free", buildType: "release", signed: "unsigned"}
	if l.variants[2] != want {
		t.Errorf("variant = %+v, want %+v", l.variants[2], want)
	}
	if got := l.flavors(":app"); strings.Join(got, ",") != "free,paid" {
		t.Errorf("flavors = %v", got)
	}
	if got := l.buildTypes(":app"); strings.Join(got, ",") != "debug,release" {
		t.Errorf("build types = %v", got)
	}
	if !strings.Contains(string(initScript), "otata-variant") || !strings.Contains(string(initScript), "otata-module") {
		t.Error("the embedded init script does not print the lines the parser reads")
	}
}

// A listing with two application modules, one flavored and one not, and a
// second with two neither of which is :app.
func twoModules() listing {
	return listing{
		modules: map[string]string{":app": "/x/app/build", ":wear": "/x/wear/build"},
		variants: []variant{
			{module: ":app", name: "freeDebug", flavor: "free", buildType: "debug", signed: "signed"},
			{module: ":app", name: "paidDebug", flavor: "paid", buildType: "debug", signed: "signed"},
			{module: ":app", name: "freeRelease", flavor: "free", buildType: "release", signed: "unsigned"},
			{module: ":app", name: "paidRelease", flavor: "paid", buildType: "release", signed: "unsigned"},
			{module: ":wear", name: "debug", buildType: "debug", signed: "signed"},
			{module: ":wear", name: "release", buildType: "release", signed: "signed"},
		},
	}
}

// The selection rule, case by case: what is named wins, a lone candidate is
// taken, :app is the module to prefer, and anything else is refused with the
// candidates and the flag that chooses, so a caller can choose without
// re-running discovery.
func TestResolveAppliesTheSelectionRule(t *testing.T) {
	cases := []struct {
		name                   string
		module, flavor, config string
		want                   string // the variant, as module/name
		wantErr                string // in the error
		wantFlag               string // an Ambiguous naming this flag
	}{
		{name: "app with two flavors asks", config: "Release", wantErr: "several product flavors", wantFlag: "--flavor"},
		{name: "flavor named", flavor: "free", config: "Release", want: ":app/freeRelease"},
		{name: "module named, no flavors", module: ":wear", config: "Debug", want: ":wear/debug"},
		{name: "module without its colon", module: "wear", config: "Debug", want: ":wear/debug"},
		{name: "build type as Gradle spells it", module: ":wear", config: "release", want: ":wear/release"},
		{name: "unknown module lists them", module: ":nope", config: "Release", wantErr: ":app, :wear"},
		{name: "flavor on a module with none", module: ":wear", flavor: "free", config: "Release", wantErr: "no product flavors"},
		{name: "unknown flavor lists them", flavor: "nope", config: "Release", wantErr: "free, paid"},
		{name: "unknown build type lists them", module: ":wear", config: "Staging", wantErr: "debug, release"},
	}
	for _, c := range cases {
		v, err := resolve(twoModules(), c.module, c.flavor, c.config)
		if c.wantErr == "" {
			if err != nil || v.module+"/"+v.name != c.want {
				t.Errorf("%s: %s/%s, %v; want %s", c.name, v.module, v.name, err, c.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: %v, want an error saying %q", c.name, err, c.wantErr)
			continue
		}
		amb, ok := errors.AsType[*Ambiguous](err)
		if (c.wantFlag != "") != ok || (ok && amb.Flag != c.wantFlag) {
			t.Errorf("%s: ambiguous=%v flag=%q, want flag %q", c.name, ok, c.wantFlag, c.wantFlag)
		}
	}

	// Two modules and neither is :app: refused, both listed.
	l := twoModules()
	l.modules = map[string]string{":mobile": "", ":tv": ""}
	_, err := resolve(l, "", "", "Release")
	amb, ok := errors.AsType[*Ambiguous](err)
	if !ok || amb.Flag != "--module" || strings.Join(amb.Candidates, ",") != ":mobile,:tv" {
		t.Errorf("got %v, want --module with both candidates", err)
	}
	// One module, whatever its name, is the one.
	l.modules = map[string]string{":wear": ""}
	if v, err := resolve(l, "", "", "Debug"); err != nil || v.name != "debug" {
		t.Errorf("lone module: %+v, %v", v, err)
	}
}

// A variant with no signing config is refused before a build runs, as the
// step it is: both remedies are in the hint. One the listing could not
// judge is left to the verification after the build.
func TestUnsignedVariantIsRefusedBeforeBuilding(t *testing.T) {
	err := unsigned(variant{module: ":app", buildType: "release", signed: "unsigned"})
	setup, ok := errors.AsType[*SetupError](err)
	if !ok {
		t.Fatalf("got %T (%v), want *SetupError", err, err)
	}
	if !strings.Contains(setup.Hint, "--config Debug") || !strings.Contains(setup.Hint, "signingConfig") {
		t.Errorf("hint = %q, does not carry both remedies", setup.Hint)
	}
	for _, signed := range []string{"signed", ""} {
		if err := unsigned(variant{signed: signed}); err != nil {
			t.Errorf("%q was refused: %v", signed, err)
		}
	}
}

// Copied from a real build: the plugin's metadata names the variant and
// the file, relative to itself.
const freeDebugMetadata = `{
  "version": 3,
  "artifactType": {"type": "APK", "kind": "Directory"},
  "applicationId": "com.example.flavored.free",
  "variantName": "freeDebug",
  "elements": [
    {"type": "SINGLE", "filters": [], "attributes": [], "versionCode": 3, "versionName": "1.2", "outputFile": "app-free-debug.apk"}
  ],
  "elementType": "File",
  "minSdkVersionForDexing": 24
}`

const splitMetadata = `{
  "version": 3,
  "variantName": "release",
  "elements": [
    {"type": "ONE_OF_MANY", "filters": [{"filterType": "ABI", "value": "arm64-v8a"}], "outputFile": "app-arm64-v8a-release.apk"},
    {"type": "ONE_OF_MANY", "filters": [{"filterType": "ABI", "value": "x86_64"}], "outputFile": "app-x86_64-release.apk"}
  ]
}`

func TestLocateAPKReadsThePluginsMetadata(t *testing.T) {
	build := t.TempDir()
	dir := filepath.Join(build, "outputs", "apk", "free", "debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output-metadata.json"), []byte(freeDebugMetadata), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := locateAPK(build, "freeDebug"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("metadata naming a missing file: %v", err)
	}
	touch(t, filepath.Join(dir, "app-free-debug.apk"))
	if apk, err := locateAPK(build, "freeDebug"); err != nil || apk != filepath.Join(dir, "app-free-debug.apk") {
		t.Errorf("locateAPK = %q, %v", apk, err)
	}
	if _, err := locateAPK(build, "paidDebug"); err == nil || !strings.Contains(err.Error(), "paidDebug") {
		t.Errorf("another variant's metadata was taken: %v", err)
	}

	// Splits: several APKs and no universal one is refused with the setting
	// that adds one; a universal element is the one served.
	_, matched, err := apkFromMetadata([]byte(splitMetadata), "release")
	if !matched || err == nil || !strings.Contains(err.Error(), "universalApk") {
		t.Errorf("split metadata: matched=%v err=%v", matched, err)
	}
	universal := strings.Replace(splitMetadata, `"elements": [`, `"elements": [
    {"type": "UNIVERSAL", "filters": [], "outputFile": "app-universal-release.apk"},`, 1)
	if file, matched, err := apkFromMetadata([]byte(universal), "release"); !matched || err != nil || file != "app-universal-release.apk" {
		t.Errorf("universal: %q %v %v", file, matched, err)
	}
	if _, matched, _ := apkFromMetadata([]byte("not json"), "release"); matched {
		t.Error("garbage matched a variant")
	}
}

// The pre-checks, in the order the toolchain would hit them, each cleared
// by what fixes it. JAVA_HOME points at a stand-in throughout so the test
// does not depend on a java on PATH.
func TestGradlePrerequisiteChecksInOrder(t *testing.T) {
	root := t.TempDir()
	jdk := t.TempDir()
	touch(t, filepath.Join(jdk, "bin", "java"))
	t.Setenv("JAVA_HOME", jdk)
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	g := &Gradle{}

	check := func(what string, want func(*SetupError) bool) {
		t.Helper()
		got := g.prerequisite(root)
		if got == nil || !want(got) {
			t.Errorf("%s: got %+v", what, got)
		}
	}
	check("no wrapper", func(e *SetupError) bool { return e.Command == "gradle wrapper" && e.Dir == root })
	touch(t, filepath.Join(root, "gradlew"))
	check("no wrapper jar", func(e *SetupError) bool { return e.Command == "gradle wrapper" })
	touch(t, filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.jar"))
	check("wrapper not executable", func(e *SetupError) bool { return e.Command == "chmod +x gradlew" && e.Dir == root })
	if err := os.Chmod(filepath.Join(root, "gradlew"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("JAVA_HOME", filepath.Join(jdk, "nope"))
	check("JAVA_HOME with no java", func(e *SetupError) bool { return strings.Contains(e.Detail, "JAVA_HOME") })
	t.Setenv("JAVA_HOME", jdk)

	check("no SDK", func(e *SetupError) bool { return *e == sdkMissing })
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte("# written by studio\nsdk.dir=/nonexistent/sdk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("SDK path that does not exist", func(e *SetupError) bool { return strings.Contains(e.Detail, "/nonexistent/sdk") })
	sdk := t.TempDir()
	// Escaped the way Android Studio writes a path, to prove the unescaping.
	escaped := strings.ReplaceAll(strings.ReplaceAll(sdk, `\`, `\\`), ":", `\:`)
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte("sdk.dir="+escaped+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := g.prerequisite(root); got != nil {
		t.Fatalf("a complete setup was refused: %+v", got)
	}
	// The SDK only local.properties named is exported for the APK reader.
	if got := os.Getenv("ANDROID_HOME"); got != sdk {
		t.Errorf("ANDROID_HOME = %q after the check, want %q", got, sdk)
	}

	// A framework's step, when the root is its android/ directory.
	parent := t.TempDir()
	root = filepath.Join(parent, "android")
	touch(t, filepath.Join(root, "gradlew"))
	if err := os.Chmod(filepath.Join(root, "gradlew"), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.jar"))
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte("sdk.dir="+escaped+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(parent, "pubspec.yaml"))
	check("flutter without its SDK location", func(e *SetupError) bool { return e.Command == "flutter pub get" && e.Dir == parent })
	if err := os.WriteFile(filepath.Join(root, "local.properties"), []byte("sdk.dir="+escaped+"\nflutter.sdk=/opt/flutter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := g.prerequisite(root); got != nil {
		t.Errorf("flutter with its SDK location was refused: %+v", got)
	}
	touch(t, filepath.Join(parent, "package.json"))
	check("react native without node_modules", func(e *SetupError) bool { return e.Command == "npm install" && e.Dir == parent })
	touch(t, filepath.Join(parent, "node_modules", ".package-lock.json"))
	if got := g.prerequisite(root); got != nil {
		t.Errorf("react native with node_modules was refused: %+v", got)
	}
}

// Each phrase as Gradle, the wrapper script or the plugin prints it,
// captured from real runs where noted, and the remedy each maps to.
func TestGradleDiagnosesNameTheRemedy(t *testing.T) {
	cases := []struct {
		log     string
		command string // expected Command, or ""
		hint    string // in the hint or the detail
	}{
		// The wrapper script, JAVA_HOME pointing nowhere (captured).
		{"ERROR: JAVA_HOME is set to an invalid directory: /nonexistent", "", "JAVA_HOME"},
		// The wrapper script, no java anywhere (captured).
		{"ERROR: JAVA_HOME is not set and no 'java' command could be found in your PATH.", "", "JDK"},
		// The plugin, on a build with no SDK (captured).
		{"> SDK location not found. Define a valid SDK location with an ANDROID_HOME environment variable", "", "ANDROID_HOME"},
		{"Failed to install the following Android SDK packages as some licences have not been accepted.", "sdkmanager --licenses", ""},
		{"Android Gradle plugin requires Java 17 to run. You are currently using Java 11.", "", "JDK 17"},
		{"Unsupported class file major version 67", "", "JAVA_HOME"},
		{"Could not get unknown property 'androidComponents' for project ':app'", "", "7.0"},
		{`Keystore file '/x/release.jks' not found for signing config 'release'.`, "", "keystore"},
		{`SigningConfig "release" is missing required property "storeFile".`, "", "--config Debug"},
		{"flutter.sdk not set in local.properties", "flutter pub get", ""},
		{"Flutter SDK not found. Define location with flutter.sdk in the local.properties file.", "flutter pub get", ""},
	}
	for _, c := range cases {
		setup, ok := errors.AsType[*SetupError](diagnose(c.log, "build failed", gradleDiagnoses))
		if !ok {
			t.Errorf("%q was not classified as setup", c.log)
			continue
		}
		if setup.Command != c.command {
			t.Errorf("%q: command = %q, want %q", c.log, setup.Command, c.command)
		}
		if c.hint != "" && !strings.Contains(setup.Hint+setup.Detail, c.hint) {
			t.Errorf("%q: %q / %q does not say %q", c.log, setup.Detail, setup.Hint, c.hint)
		}
	}
	if err := diagnose("error: cannot find symbol", "build failed", gradleDiagnoses); err.Error() != "build failed" {
		t.Errorf("a compile error was classified: %v", err)
	}
}
