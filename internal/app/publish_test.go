package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/storage"
)

// The publish result speaks the same units and names as the record and the
// listings: bytes beside the megabytes older callers read, and the platform.
func TestPublishResultJSONMatchesTheRecord(t *testing.T) {
	out, err := json.Marshal(PublishResult{Platform: artifact.IOS, SizeBytes: 15 << 20, SizeMB: 15})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"platform":"ios"`, `"size_bytes":15728640`, `"size_mb":15`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("publish JSON lacks %s: %s", want, out)
		}
	}
}

// A zero CertExpires, the normal state on a node that only serves, must not
// reach an agent as the year 0001.
func TestZeroCertExpiryIsOmittedFromJSON(t *testing.T) {
	s := appmeta.Signing{ProfileExpires: time.Now(), Expires: time.Now(), Binder: appmeta.BinderProfile}
	out, err := json.Marshal(PublishResult{Signing: &s})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "cert_expires") {
		t.Errorf("zero cert_expires serialized: %s", out)
	}
	s.CertExpires = time.Now()
	out, _ = json.Marshal(PublishResult{Signing: &s})
	if !strings.Contains(string(out), "cert_expires") {
		t.Errorf("a real cert_expires was dropped: %s", out)
	}
}

// The title an --artifact publish prints comes out of the payload's own
// Info.plist. It must not be able to drive the terminal it is printed on.
func TestHumanOutputStripsHostileTitle(t *testing.T) {
	res := PublishResult{Title: "App\x1b]0;owned\x07\x1b[31m", Version: "1", Build: "1",
		InstallURL: "https://host/otata/app/", IndexURL: "https://host/otata/"}
	var out strings.Builder
	res.Human(&out)
	if strings.Contains(out.String(), "\x1b]0;") || strings.Contains(out.String(), "\x07") || strings.Contains(out.String(), "\x1b[31m") {
		t.Errorf("hostile title reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "https://host/otata/app/") {
		t.Errorf("the install URL went missing: %q", out.String())
	}
}

func TestPublishHumanShowsSigningOnlyWhenItMatters(t *testing.T) {
	base := PublishResult{
		Title: "My App", Version: "1.0", Build: "1", BuildConfig: "Debug",
		InstallURL: "https://host/otata/myapp/", IndexURL: "https://host/otata/",
	}

	// The ordinary publish: a deadline months out says nothing, so the URLs
	// stay the last thing printed and the output doesn't get noisy.
	var quiet bytes.Buffer
	base.Human(&quiet)
	if strings.Contains(quiet.String(), "Signing") {
		t.Errorf("published with no warning but printed one:\n%s", quiet.String())
	}

	warned := base
	warned.SigningWarning = "certificate expires 2026-11-11, in 12 days"
	var loud bytes.Buffer
	warned.Human(&loud)
	if !strings.Contains(loud.String(), "in 12 days") {
		t.Errorf("a warning did not reach the output:\n%s", loud.String())
	}
	// The install URL is what a publish is for; a warning must not displace it.
	if !strings.Contains(loud.String(), base.InstallURL) {
		t.Errorf("the install URL went missing:\n%s", loud.String())
	}
}

// The first signal cancels the build's context and is remembered, so Publish
// can report the stop with the shell's exit status for it. The process itself
// survives: the signal is delivered to this test binary, and Notify is what
// keeps it from being fatal. A second signal would exit at once, so only one
// is sent.
func TestFirstSignalCancelsTheBuildAndIsReported(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	ctx, interrupted, stop := a.onInterrupt("app", "https://host/otata")
	defer stop()

	if interrupted() != nil {
		t.Fatal("interrupted before any signal")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the build's context was not cancelled by the signal")
	}
	if got := interrupted(); got != syscall.SIGTERM {
		t.Errorf("interrupted() = %v, want SIGTERM", got)
	}
}

// The exit status follows the shell's convention for a signalled process,
// per signal, rather than 130 for everything.
func TestSignalExit(t *testing.T) {
	cases := map[os.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129}
	for sig, want := range cases {
		if got := signalExit(sig); got != want {
			t.Errorf("signalExit(%s) = %d, want %d", signalName(sig), got, want)
		}
	}
	if got := signalName(syscall.SIGTERM); got != "SIGTERM" {
		t.Errorf("signalName = %q", got)
	}
}

// What to build for is never discovered: a build without --platform is
// refused, an unknown platform is named, and a platform with no builder yet
// says so. All three are the caller's to fix, so they exit 2, and all three
// arrive before anything is claimed, wired or written.
func TestPublishRequiresAKnownPlatformToBuild(t *testing.T) {
	cases := []struct {
		name     string
		platform artifact.Platform
		want     string // in the message or the hint
	}{
		{"none", "", "--platform"},
		{"unknown", "windows", `"windows"`},
		{"no builder yet", artifact.Android, "not supported yet"},
	}
	for _, c := range cases {
		a := freshApp(t)
		_, err := a.Publish(PublishOptions{Dir: t.TempDir(), Platform: c.platform}, quiet)
		f := cli.AsFailure(err)
		if err == nil || f.Code != cli.CodeInvalidArgs {
			t.Errorf("%s: err=%v code=%q, want %q", c.name, err, f.Code, cli.CodeInvalidArgs)
			continue
		}
		if !strings.Contains(f.Message+f.Hint, c.want) {
			t.Errorf("%s: %q / %q does not say %q", c.name, f.Message, f.Hint, c.want)
		}
		if _, err := os.Stat(a.Store.IndexPath()); err == nil {
			t.Errorf("%s: a refused publish generated pages", c.name)
		}
		if markers, _ := a.Store.Building(); len(markers) != 0 {
			t.Errorf("%s: a refused publish left a build marker: %v", c.name, markers)
		}
	}
}

// A prebuilt payload says which platform it is, so --platform is not needed
// with --artifact, and one that disagrees with the file is refused. The
// platform step is passed when the failure that comes back is the
// transport's, which is the next thing publish asks for.
func TestArtifactPublishReadsThePlatformOffTheFile(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "Demo.ipa")
	apk := filepath.Join(dir, "Demo.apk")
	for _, p := range []string{ipa, apk} {
		if err := os.WriteFile(p, []byte("zip"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		artifact string
		platform artifact.Platform
		wantCode string
		want     string // in the message or the hint
	}{
		{"inferred", ipa, "", cli.CodeNoTransport, ""},
		{"agreeing", ipa, artifact.IOS, cli.CodeNoTransport, ""},
		{"disagreeing", ipa, artifact.Android, cli.CodeInvalidArgs, "ios payload"},
		{"unknown platform", ipa, "windows", cli.CodeInvalidArgs, `"windows"`},
		{"no reader yet", apk, "", cli.CodeInvalidArgs, "not served yet"},
		{"missing", filepath.Join(dir, "nope.ipa"), "", cli.CodeInvalidArgs, "no artifact"},
	}
	for _, c := range cases {
		a := freshApp(t)
		// Selected but incomplete, so the transport refuses without probing
		// for a tailscale CLI on the machine.
		a.Config.Transport = "manual"
		_, err := a.Publish(PublishOptions{Dir: dir, Artifact: c.artifact, Platform: c.platform}, quiet)
		f := cli.AsFailure(err)
		if err == nil || f.Code != c.wantCode {
			t.Errorf("%s: err=%v code=%q, want %q", c.name, err, f.Code, c.wantCode)
			continue
		}
		if !strings.Contains(f.Message+f.Hint, c.want) {
			t.Errorf("%s: %q / %q does not say %q", c.name, f.Message, f.Hint, c.want)
		}
	}
}
