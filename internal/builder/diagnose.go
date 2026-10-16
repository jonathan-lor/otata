package builder

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SigningError is a signing failure that needs a human with Xcode or portal
// access. Hint is the remedy for the phrase that matched.
type SigningError struct {
	Detail string
	Hint   string
}

func (e *SigningError) Error() string { return "code signing failed: " + e.Detail }

// SetupError is a prerequisite the project's toolchain is missing, like pods
// or a JDK, usually with the command that fixes it.
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
	// signing marks a SigningError, with hint as its remedy.
	signing bool
	hint    string
	// setup is the SetupError for everything else.
	setup SetupError
}

// gradleDiagnoses are Gradle's messages for missing prerequisites. Both
// builders scan for them, since a KMP iOS build runs Gradle.
var gradleDiagnoses = []diagnosis{
	// The JDK.
	{phrase: "Unable to locate a Java Runtime", setup: jdkMissing},
	{phrase: "JAVA_HOME is not set", setup: jdkMissing},
	{phrase: "JAVA_HOME is set to an invalid directory", setup: SetupError{
		Detail: "JAVA_HOME points at a directory with no JDK",
		Hint:   "point JAVA_HOME at a JDK 17 or newer, or unset it to use the java on PATH"}},
	{phrase: "Android Gradle plugin requires Java", setup: SetupError{
		Detail: "the Android Gradle plugin needs a newer JDK than the one running Gradle",
		Hint:   "install a JDK 17 or newer and point JAVA_HOME at it"}},
	{phrase: "Unsupported class file major version", setup: SetupError{
		Detail: "the project's Gradle version does not run on the installed JDK, which is newer than it knows",
		Hint:   "point JAVA_HOME at the JDK the project expects, or upgrade the wrapper in gradle/wrapper/gradle-wrapper.properties"}},

	// The SDK.
	{phrase: "SDK location not found", setup: sdkMissing},
	{phrase: "as some licences have not been accepted", setup: SetupError{
		Detail:  "the build needs Android SDK packages whose licenses have not been accepted",
		Command: "sdkmanager --licenses",
		Hint:    "the tool is under the SDK's cmdline-tools/latest/bin; Android Studio's SDK Manager does the same"}},

	// The plugin, whose variant API the listing needs.
	{phrase: "Could not get unknown property 'androidComponents'", setup: SetupError{
		Detail: "the project's Android Gradle plugin is older than 7.0, whose variant API is how otata lists a project's modules",
		Hint:   "upgrade the plugin to com.android.tools.build:gradle 7.0 or newer"}},

	// Signing, which is a setup step on Android.
	{phrase: "not found for signing config", setup: SetupError{
		Detail: "the signing config names a keystore that is not on this machine",
		Hint:   "put the keystore where the module's build script expects it, or build with --config Debug"}},
	{phrase: "is missing required property", setup: SetupError{
		Detail: "the signing config is incomplete",
		Hint:   "the module's build script names the missing property; set it, or build with --config Debug"}},

	// Flutter's two templates, without the SDK path in local.properties.
	{phrase: "flutter.sdk not set in local.properties", setup: flutterSDKMissing},
	{phrase: "Define location with flutter.sdk in the local.properties file", setup: flutterSDKMissing},
}

// classifyBuildFailure names what failed from the tail of the log. A
// cancelled build reports the cancellation instead.
func classifyBuildFailure(ctx context.Context, logPath, fallback string, tables ...[]diagnosis) error {
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
	return diagnose(text, fallback, tables...)
}

// diagnose returns the remedy for whichever phrase appears first in the log,
// not first in the tables. A fresh cross-platform clone fails on setup and
// signing at once, and setup comes first.
func diagnose(text, fallback string, tables ...[]diagnosis) error {
	best := -1
	var match diagnosis
	for _, table := range tables {
		for _, d := range table {
			i := strings.Index(text, d.phrase)
			if i < 0 || (best >= 0 && i >= best) {
				continue
			}
			best, match = i, d
		}
	}
	if best < 0 {
		return fmt.Errorf("%s", fallback)
	}
	if match.signing {
		return &SigningError{Detail: match.phrase, Hint: match.hint}
	}
	setup := match.setup
	return &setup
}
