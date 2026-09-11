package builder

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Gradle builds a native Android app with the project's own Gradle wrapper.
// The wrapper rather than a Gradle on PATH: it pins the Gradle version the
// project was written for, every Android project ships one, and a project
// without one is a project that needs one, which is what it is told.
type Gradle struct{}

// gradleDirs covers the repository root plus the subdirectory the
// cross-platform frameworks keep their Android project in.
func gradleDirs(dir string) []string {
	return []string{dir, filepath.Join(dir, "android")}
}

// Detect reports the Gradle root: the directory holding the settings file,
// which is what defines a Gradle build. The wrapper beside it is a
// prerequisite checked before the build, not a condition of detection.
func (g *Gradle) Detect(dir string) (string, error) {
	for _, d := range gradleDirs(dir) {
		for _, name := range []string{"settings.gradle.kts", "settings.gradle"} {
			if exists(filepath.Join(d, name)) {
				return d, nil
			}
		}
	}
	return "", fmt.Errorf("no settings.gradle or settings.gradle.kts found here")
}

// initScript is the listing Gradle is asked for; see otata.init.gradle.
//
//go:embed otata.init.gradle
var initScript []byte

// ---------- prerequisites ----------

// jdkMissing, sdkMissing and flutterSDKMissing are each stated once, for
// the pre-check and for the log phrases that mean the same thing.
var (
	jdkMissing = SetupError{
		Detail: "the build runs Gradle, and no JDK is installed or on PATH",
		Hint:   "install a JDK 17 or newer, or set JAVA_HOME",
	}
	sdkMissing = SetupError{
		Detail: "the build has an Android target, and no Android SDK is configured",
		Hint:   "install the Android SDK and set ANDROID_HOME, or write sdk.dir into local.properties",
	}
	flutterSDKMissing = SetupError{
		Detail:  "Flutter has not written its SDK location for this project",
		Command: "flutter pub get",
		Hint:    "android/local.properties is generated, not committed, so a fresh clone never has it",
	}
)

// prerequisite reports what an Android build needs before Gradle can run,
// or nil. Asked up front for the same reason as Xcode's: the answer is on
// disk, and the run it replaces takes a minute to fail, on the loudest
// reason rather than the first. Each check mirrors what the wrapper script
// and the plugin themselves look for, in their order.
func (g *Gradle) prerequisite(root string) *SetupError {
	// The wrapper: its script, its jar, and the script being runnable, which
	// a checkout through a filesystem with no execute bit loses.
	info, err := os.Stat(filepath.Join(root, "gradlew"))
	switch {
	case err != nil || !exists(filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.jar")):
		return &SetupError{
			Detail:  "the project has no Gradle wrapper",
			Command: "gradle wrapper",
			Dir:     root,
			Hint:    "otata builds with the project's own wrapper so the Gradle version is the project's; writing one needs a Gradle on PATH once",
		}
	case info.Mode()&0o111 == 0:
		return &SetupError{
			Detail:  "the Gradle wrapper is not executable",
			Command: "chmod +x gradlew",
			Dir:     root,
		}
	}

	// The JDK, as the wrapper resolves it: JAVA_HOME first, then PATH.
	if home := os.Getenv("JAVA_HOME"); home != "" {
		if !exists(filepath.Join(home, "bin", "java")) {
			return &SetupError{
				Detail: fmt.Sprintf("JAVA_HOME is %s, which holds no JDK", home),
				Hint:   "point JAVA_HOME at a JDK 17 or newer, or unset it to use the java on PATH",
			}
		}
	} else if _, err := exec.LookPath("java"); err != nil {
		missing := jdkMissing
		return &missing
	}

	// The SDK, as the plugin resolves it.
	sdk := sdkLocation(root)
	if sdk == "" {
		missing := sdkMissing
		return &missing
	}
	if !exists(sdk) {
		return &SetupError{
			Detail: fmt.Sprintf("the Android SDK is configured at %s, which does not exist", sdk),
			Hint:   "install the SDK there, or point ANDROID_HOME or local.properties' sdk.dir at where it is",
		}
	}
	// The APK is read after the build with the SDK's own build-tools, which
	// the reader finds through ANDROID_HOME. When only local.properties names
	// the SDK, the reader would fail after a successful build, so the location
	// is exported for the rest of this process.
	if os.Getenv("ANDROID_HOME") == "" && os.Getenv("ANDROID_SDK_ROOT") == "" {
		os.Setenv("ANDROID_HOME", sdk)
	}

	return frameworkPrerequisite(root)
}

// sdkLocation is where the plugin will look for the SDK: sdk.dir in the
// project's local.properties first, as the plugin itself prefers, then
// ANDROID_HOME, then ANDROID_SDK_ROOT for an older setup.
func sdkLocation(root string) string {
	if dir := localProperty(root, "sdk.dir"); dir != "" {
		return dir
	}
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

// localProperty reads one key out of local.properties, a Java properties
// file: a backslash escapes the character after it, which is how Android
// Studio writes a Windows path and its colon.
func localProperty(root, key string) string {
	raw, err := os.ReadFile(filepath.Join(root, "local.properties"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		var value strings.Builder
		v = strings.TrimSpace(v)
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			value.WriteByte(v[i])
		}
		return value.String()
	}
	return ""
}

// frameworkPrerequisite reports the step a framework wrapping the Android
// project has not run. Both keep the project in android/ under their own
// root, and both generate what its settings script includes rather than
// commit it.
func frameworkPrerequisite(root string) *SetupError {
	parent := filepath.Dir(root)
	// Flutter's settings script loads its Gradle plugin from the SDK path
	// that every flutter command writes into local.properties.
	if exists(filepath.Join(parent, "pubspec.yaml")) && localProperty(root, "flutter.sdk") == "" {
		missing := flutterSDKMissing
		missing.Dir = parent
		return &missing
	}
	// React Native's loads its from node_modules, which an install writes.
	if exists(filepath.Join(parent, "package.json")) && !exists(filepath.Join(parent, "node_modules")) {
		return &SetupError{
			Detail:  "the project's node modules are not installed",
			Command: "npm install",
			Dir:     parent,
		}
	}
	return nil
}

// ---------- the listing ----------

// listing is what the init script reports: every application module with
// its build directory, and every variant of each.
type listing struct {
	modules  map[string]string
	variants []variant
}

// variant is one build of one application module, as Gradle names it:
// the flavor, or none, followed by the build type, as freeDebug.
type variant struct {
	module, name, flavor, buildType string
	// signed is "signed" or "unsigned" as the listing could tell, or ""
	// where it could not.
	signed string
}

// parseListing reads the init script's lines out of everything else Gradle
// printed. Split from the running so the parse is testable.
func parseListing(out []byte) listing {
	l := listing{modules: map[string]string{}}
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		switch {
		case fields[0] == "otata-module" && len(fields) == 3:
			l.modules[fields[1]] = fields[2]
		case fields[0] == "otata-variant" && len(fields) == 6:
			l.variants = append(l.variants, variant{
				module: fields[1], name: fields[2], flavor: fields[3], buildType: fields[4], signed: fields[5],
			})
		}
	}
	return l
}

func (l listing) modulePaths() []string {
	paths := make([]string, 0, len(l.modules))
	for p := range l.modules {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// flavors is the module's product flavors, as its variants name them: a
// project with several flavor dimensions names each combination.
func (l listing) flavors(module string) []string {
	return l.distinct(module, func(v variant) string { return v.flavor })
}

func (l listing) buildTypes(module string) []string {
	return l.distinct(module, func(v variant) string { return v.buildType })
}

func (l listing) distinct(module string, field func(variant) string) []string {
	var out []string
	for _, v := range l.variants {
		if s := field(v); v.module == module && s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// buildDir is where the module builds, as it reported, or the conventional
// place for a module the script listed variants for but no directory.
func (l listing) buildDir(module, root string) string {
	if dir := l.modules[module]; dir != "" {
		return dir
	}
	rel := strings.ReplaceAll(strings.TrimPrefix(module, ":"), ":", string(filepath.Separator))
	return filepath.Join(root, rel, "build")
}

// list asks Gradle which application modules the project has and which
// variants each builds. The init script prints them while Gradle configures
// the project, and the help task is what makes it configure without
// building. Configuration on demand and the configuration cache are both
// off for the run: the first would configure the root project alone, and
// the second would replay a cached configuration and print nothing.
func (g *Gradle) list(ctx context.Context, j *gradleJob) (listing, error) {
	script := filepath.Join(j.work, "otata.init.gradle")
	if err := os.WriteFile(script, initScript, 0o644); err != nil {
		return listing{}, err
	}
	var out bytes.Buffer
	err := run(ctx, io.MultiWriter(&out, j.log), j.log, j.root, j.wrapper,
		"--init-script", script, "--console=plain", "--quiet",
		"--no-configure-on-demand", "--no-configuration-cache", "help")
	if err != nil {
		return listing{}, j.failure(ctx, "Gradle could not configure the project")
	}
	l := parseListing(out.Bytes())
	if len(l.modules) == 0 {
		return listing{}, fmt.Errorf("no application module in %s: nothing there applies com.android.application", filepath.Base(j.root))
	}
	return l, nil
}

// ---------- selection ----------

// resolve applies the selection rule to the listing. The module: the one
// named, else the lone application module, else one named :app, else refuse
// and list. The flavor within it: the one named, else the lone flavor, else
// none where the module defines none, else refuse and list. The build type
// is the configuration as Gradle spells it, and a module that does not
// build it says which it does.
func resolve(l listing, module, flavor, config string) (variant, error) {
	module, err := resolveModule(l, module)
	if err != nil {
		return variant{}, err
	}
	flavor, err = resolveFlavor(l, module, flavor)
	if err != nil {
		return variant{}, err
	}
	for _, v := range l.variants {
		if v.module == module && v.flavor == flavor && (v.buildType == config || v.buildType == lowerFirst(config)) {
			return v, nil
		}
	}
	return variant{}, fmt.Errorf("%s has no %s build type; --config takes one of %s",
		module, lowerFirst(config), strings.Join(l.buildTypes(module), ", "))
}

func resolveModule(l listing, module string) (string, error) {
	paths := l.modulePaths()
	if module != "" {
		if _, ok := l.modules[module]; ok {
			return module, nil
		}
		// Gradle spells a project path with a leading colon; the same
		// module named without one is the same module.
		if _, ok := l.modules[":"+module]; ok && !strings.HasPrefix(module, ":") {
			return ":" + module, nil
		}
		return "", fmt.Errorf("no application module %s; the project has %s", module, strings.Join(paths, ", "))
	}
	if len(paths) == 1 {
		return paths[0], nil
	}
	if _, ok := l.modules[":app"]; ok {
		return ":app", nil
	}
	return "", &Ambiguous{
		Detail: "several application modules and none named :app", Flag: "--module", Candidates: paths,
	}
}

func resolveFlavor(l listing, module, flavor string) (string, error) {
	flavors := l.flavors(module)
	if flavor != "" {
		if slices.Contains(flavors, flavor) {
			return flavor, nil
		}
		if len(flavors) == 0 {
			return "", fmt.Errorf("%s has no product flavors, so --flavor %s selects nothing", module, flavor)
		}
		return "", fmt.Errorf("%s has no product flavor %s; it has %s", module, flavor, strings.Join(flavors, ", "))
	}
	switch len(flavors) {
	case 0:
		return "", nil
	case 1:
		return flavors[0], nil
	}
	return "", &Ambiguous{
		Detail: module + " has several product flavors", Flag: "--flavor", Candidates: flavors,
	}
}

// unsigned refuses a variant the listing reports as having no signing
// config, before the build. Android will not install an unsigned APK, and a
// release build type ships with no signingConfig, so this is every fresh
// project's first Release publish: refused once, with both remedies, rather
// than after a release build that took minutes. Where the listing could
// not tell, the APK is verified after the build instead.
func unsigned(v variant) error {
	if v.signed != "unsigned" {
		return nil
	}
	return &SetupError{
		Detail: fmt.Sprintf("%s has no signing config for its %s build type, and Android refuses to install an unsigned APK", v.module, v.buildType),
		Hint:   fmt.Sprintf("build with --config Debug, which signs with the debug keystore, or add a signingConfig for %s to the module's build script", v.buildType),
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---------- the build ----------

// gradleJob is what Build resolves before running anything: the Gradle
// root, its wrapper, and an open log in the work directory.
type gradleJob struct {
	root, wrapper, work, logPath string
	log                          *os.File
}

// prepare resolves the job, refusing before Gradle runs when something it
// needs is not there.
func (g *Gradle) prepare(opts Options) (*gradleJob, error) {
	if opts.Config == "" {
		return nil, fmt.Errorf("no build configuration given")
	}
	if missing := g.prerequisite(opts.Container); missing != nil {
		return nil, missing
	}
	if err := os.MkdirAll(opts.Work, 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(opts.Work, "gradle.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	return &gradleJob{
		root: opts.Container, wrapper: filepath.Join(opts.Container, "gradlew"),
		work: opts.Work, logPath: logPath, log: logFile,
	}, nil
}

// failure names what went wrong from the log.
func (j *gradleJob) failure(ctx context.Context, fallback string) error {
	return classifyBuildFailure(ctx, j.logPath, fallback, gradleDiagnoses)
}

func (g *Gradle) Build(ctx context.Context, opts Options) (Result, error) {
	j, err := g.prepare(opts)
	if err != nil {
		return Result{}, err
	}
	defer j.log.Close()
	failed := Result{LogPath: j.logPath}

	opts.logf("asking Gradle for %s's application modules", filepath.Base(j.root))
	l, err := g.list(ctx, j)
	if err != nil {
		return failed, err
	}
	v, err := resolve(l, opts.Module, opts.Flavor, opts.Config)
	if err != nil {
		return failed, err
	}
	if err := unsigned(v); err != nil {
		return failed, err
	}

	// assemble<Variant> on the module: the APK, signed as the variant's
	// config says, with nothing installed or bundled.
	opts.logf("building %s in %s (%s)", v.name, v.module, opts.Config)
	task := v.module + ":assemble" + upperFirst(v.name)
	if err := run(ctx, j.log, j.log, j.root, j.wrapper, "--console=plain", task); err != nil {
		return failed, j.failure(ctx, "build failed")
	}

	apk, err := locateAPK(l.buildDir(v.module, j.root), v.name)
	if err != nil {
		return failed, err
	}
	return Result{PayloadPath: apk, Platform: artifact.Android, Config: opts.Config, LogPath: j.logPath}, nil
}

// locateAPK finds the variant's APK through the metadata the plugin writes
// beside it, rather than a guessed filename: outputs/apk/<flavor>/<type>/
// is the convention, but the file's name is the project's to set, and a
// project that splits its APKs has several.
func locateAPK(buildDir, variantName string) (string, error) {
	root := filepath.Join(buildDir, "outputs", "apk")
	var found string
	var problem error
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "output-metadata.json" {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		file, matched, err := apkFromMetadata(raw, variantName)
		if !matched {
			return nil
		}
		if err != nil {
			problem = err
		} else {
			found = filepath.Join(filepath.Dir(p), file)
		}
		return filepath.SkipAll
	})
	switch {
	case problem != nil:
		return "", problem
	case found == "":
		return "", fmt.Errorf("no APK for %s under %s: the build wrote no output-metadata.json naming it", variantName, root)
	case !exists(found):
		return "", fmt.Errorf("the build's metadata names %s, which does not exist", found)
	}
	return found, nil
}

// apkFromMetadata reads the plugin's output-metadata.json: the variant it
// describes and the files that variant produced. matched reports whether it
// is the variant asked for. A variant split by ABI produces one APK per
// ABI, and otata serves one, so the universal APK is the one taken, and a
// project that builds none is told which setting adds it.
func apkFromMetadata(raw []byte, variantName string) (file string, matched bool, err error) {
	var meta struct {
		VariantName string `json:"variantName"`
		Elements    []struct {
			Filters    []json.RawMessage `json:"filters"`
			OutputFile string            `json:"outputFile"`
		} `json:"elements"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.VariantName != variantName {
		return "", false, nil
	}
	for _, e := range meta.Elements {
		if len(e.Filters) == 0 && e.OutputFile != "" {
			return e.OutputFile, true, nil
		}
	}
	if len(meta.Elements) == 0 {
		return "", true, fmt.Errorf("the build's metadata for %s names no APK", variantName)
	}
	return "", true, fmt.Errorf("%s builds %d APKs split by ABI and no universal one; otata serves one APK, which splits.abi.universalApk = true adds", variantName, len(meta.Elements))
}
