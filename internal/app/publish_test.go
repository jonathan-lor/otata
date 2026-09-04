package app

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/storage"
)

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
