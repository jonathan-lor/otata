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

// Gradle builds an Android app with the project's own Gradle wrapper, which
// pins the Gradle version the project expects.
type Gradle struct{}

// gradleDirs is the repository root and android/, where frameworks keep theirs.
func gradleDirs(dir string) []string {
	return []string{dir, filepath.Join(dir, "android")}
}

// Detect returns the directory holding the settings file.
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

// initScript asks Gradle for the project's modules and variants.
//
//go:embed otata.init.gradle
var initScript []byte

// Each is shared by the pre-check and the log phrases that mean the same.
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

// prerequisite returns what the build is missing, or nil, checking in the
// order the wrapper and the plugin would fail.
func (g *Gradle) prerequisite(root string) *SetupError {
	// The wrapper: its script, its jar and the execute bit.
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
	// The APK reader finds the build-tools through ANDROID_HOME, so export an
	// SDK that only local.properties names.
	if os.Getenv("ANDROID_HOME") == "" && os.Getenv("ANDROID_SDK_ROOT") == "" {
		os.Setenv("ANDROID_HOME", sdk)
	}

	return frameworkPrerequisite(root)
}

// sdkLocation finds the SDK as the plugin does: sdk.dir, then ANDROID_HOME,
// then ANDROID_SDK_ROOT.
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

// localProperty reads a key from local.properties, undoing backslash escapes.
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

// frameworkPrerequisite reports a Flutter or React Native setup step that
// hasn't run.
func frameworkPrerequisite(root string) *SetupError {
	parent := filepath.Dir(root)
	// Flutter's settings script needs flutter.sdk in local.properties.
	if exists(filepath.Join(parent, "pubspec.yaml")) && localProperty(root, "flutter.sdk") == "" {
		missing := flutterSDKMissing
		missing.Dir = parent
		return &missing
	}
	// React Native's needs node_modules.
	if exists(filepath.Join(parent, "package.json")) && !exists(filepath.Join(parent, "node_modules")) {
		return &SetupError{
			Detail:  "the project's node modules are not installed",
			Command: "npm install",
			Dir:     parent,
		}
	}
	return nil
}

// listing is the init script's report: each application module's build
// directory, and every variant.
type listing struct {
	modules  map[string]string
	variants []variant
}

// variant is one build of a module, named like freeDebug.
type variant struct {
	module, name, flavor, buildType string
	// signed is "signed", "unsigned", or "" when the plugin can't tell.
	signed string
}

// parseListing picks the init script's lines out of Gradle's output.
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

// flavors lists the module's flavors, combined across dimensions as Gradle names them.
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

// buildDir is the module's reported build directory, or the conventional one.
func (l listing) buildDir(module, root string) string {
	if dir := l.modules[module]; dir != "" {
		return dir
	}
	rel := strings.ReplaceAll(strings.TrimPrefix(module, ":"), ":", string(filepath.Separator))
	return filepath.Join(root, rel, "build")
}

// list runs the init script while Gradle configures the help task.
// Configure-on-demand and the configuration cache are off, since either
// would skip the configuration that prints the listing.
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

// resolve picks the variant: the named module, else the lone one, else :app,
// and the named flavor, else the lone one, else none. config names the build
// type. Anything ambiguous is refused with the candidates.
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
		// Accept a module named without its leading colon.
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

// unsigned refuses a variant with no signing config before building it,
// since Android won't install the APK. A fresh project's release build type
// has none.
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

// gradleJob is a resolved build: the Gradle root, its wrapper and an open log.
type gradleJob struct {
	root, wrapper, work, logPath string
	log                          *os.File
}

// prepare resolves the job, refusing it if a prerequisite is missing.
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

	// assemble<Variant> builds the APK without installing it.
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

// locateAPK finds the variant's APK through the output-metadata.json the
// plugin writes, since a project can name and split its APKs.
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

// apkFromMetadata returns the APK output-metadata.json names for the variant.
// With ABI splits it takes the universal APK, since otata serves just one.
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
