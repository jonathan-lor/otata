package builder

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SigningError distinguishes a failure a human must fix in Apple's portal from
// one the code can fix. An agent should escalate the first and retry the second.
//
// Hint is the remedy for the phrase that matched, not for signing in general.
type SigningError struct {
	Detail string
	Hint   string
}

func (e *SigningError) Error() string { return "code signing failed: " + e.Detail }

// SetupError reports a step the project's own toolchain must run before the
// build can start: pods installed, an xcconfig generated, a JDK for a Gradle
// phase. Not a build failure: the code is fine and the log holds nothing to
// act on. Not "no project" either. One command fixes it, so the command is
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
	// signing marks what only a human with Xcode or portal access can resolve,
	// and hint is its remedy. A signing failure states itself, so the matched
	// phrase is its detail.
	signing bool
	hint    string
	// setup is the step a caller can run and then retry, for the rest, which
	// is the whole reason the two are different codes.
	setup SetupError
}

// gradleDiagnoses is what a Gradle build prints for a prerequisite it lacks,
// each phrased as the toolchain phrases it. Both builders scan for these: a
// Gradle build is what an Android publish runs, and what a Kotlin
// Multiplatform archive runs inside xcodebuild.
var gradleDiagnoses = []diagnosis{
	{phrase: "Unable to locate a Java Runtime", setup: SetupError{
		Detail: "this build runs Gradle, and no JDK is installed",
		Hint:   "install a JDK 17 or newer"}},
	{phrase: "JAVA_HOME is not set", setup: SetupError{
		Detail: "this build runs Gradle, and no JDK is on PATH",
		Hint:   "install a JDK 17 or newer, or set JAVA_HOME"}},
	{phrase: "SDK location not found", setup: SetupError{
		Detail: "the Gradle build has an Android target and no Android SDK is configured",
		Hint:   "install the Android SDK and set ANDROID_HOME, or write sdk.dir into local.properties"}},
}

// classifyBuildFailure reads the tail of the build log and names what failed.
// A build that was cancelled failed because it was told to, and its log says
// nothing about the code, so the cancellation is reported instead.
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

/*
diagnose is the log-reading half, split out so it can be tested against real
build output without a build. The tables are scanned by position in the log,
not in the order written: whichever phrase appears FIRST in the build output
wins.

That matters for cross-platform projects. A fresh checkout fails for two
reasons at once (the framework's setup step has never run, and the template
ships no development team), so a table-ordered scan reported signing and sent
the caller to Apple's portal for something 'pod install' fixes.
*/
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
