package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
)

func at(now time.Time, d time.Duration) appmeta.Signing {
	s := appmeta.Signing{ProfileExpires: now.Add(d), Expires: now.Add(d), Binder: appmeta.BinderProfile}
	return s
}

func TestSigningCheckSeverities(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		in       time.Duration
		wantOK   bool
		wantWarn bool
	}{
		{"months out is silent", 83 * 24 * time.Hour, true, false},
		{"just outside the window is silent", signingWindow + time.Hour, true, false},
		{"inside the window warns", 10 * 24 * time.Hour, true, true},
		{"expired fails", -time.Hour, false, false},
	}
	for _, c := range cases {
		got := signingCheck("app signing", at(now, c.in), now)
		if got.OK != c.wantOK || got.Warn != c.wantWarn {
			t.Errorf("%s: ok=%v warn=%v, want ok=%v warn=%v", c.name, got.OK, got.Warn, c.wantOK, c.wantWarn)
		}
		if got.Detail == "" {
			t.Errorf("%s: no detail; an agent reads the date from here even when nothing is wrong", c.name)
		}
	}
}

// The invariant the third state exists for: a warning must not fail the command
// an agent uses as a health gate, and an expiry must. record is the one door
// every check enters the result through, so the rule is tested at that door.
func TestOnlyAFailingCheckClearsHealthy(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name        string
		in          time.Duration
		wantHealthy bool
	}{
		{"warning", 10 * 24 * time.Hour, true},
		{"expiry", -time.Hour, false},
	} {
		res := &DoctorResult{Healthy: true}
		res.record(signingCheck("app signing", at(now, c.in), now))
		if res.Healthy != c.wantHealthy {
			t.Errorf("%s: healthy=%v, want %v", c.name, res.Healthy, c.wantHealthy)
		}
		if len(res.Checks) != 1 {
			t.Errorf("%s: %d checks recorded, want 1", c.name, len(res.Checks))
		}
	}
}

func TestDoctorHumanDistinguishesAllThreeStates(t *testing.T) {
	res := DoctorResult{Checks: []Check{
		{Name: "fine", OK: true},
		{Name: "soon", OK: true, Warn: true, Detail: "profile expires 2026-09-01, in 12 days"},
		{Name: "gone", OK: false, Detail: "profile expired 2026-08-01, signing no longer works"},
	}}
	var buf bytes.Buffer
	res.Human(&buf)
	out := buf.String()

	for _, want := range []string{"OK", "WARN", "FAIL"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s line in:\n%s", want, out)
		}
	}
	// A warning renders its reason. Folded into a bare OK it would be
	// indistinguishable from a passing check, which is why Warn is a field.
	if !strings.Contains(out, "in 12 days") {
		t.Errorf("a warning dropped its detail:\n%s", out)
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

// A free profile fails the check outright. iOS refuses to install it OTA regardless of the dates.
func TestFreeProfileFailsTheCheck(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	sig := at(now, 7*24*time.Hour)
	sig.Free = true

	got := signingCheck("app signing", sig, now)
	if got.OK {
		t.Error("a build that cannot be installed passed the check")
	}
	if !strings.Contains(got.Detail, "free provisioning profile") {
		t.Errorf("detail does not say why: %q", got.Detail)
	}
}

// The team belongs in the listing because automatic signing chooses it and a
// machine with several Apple accounts gives no other sign of which. A payload
// with no readable profile yields none, and blank would read as "signed by
// nobody" rather than "not recorded".
func TestListNamesTheSigningTeam(t *testing.T) {
	var buf bytes.Buffer
	ListResult{Apps: []artifact.Record{
		{Slug: "signed", Title: "Signed", Version: "1.0", Build: "1", Config: "Release", Team: "WDT3B55TUP"},
		{Slug: "older", Title: "Older", Version: "1.0", Build: "1", Config: "Release"},
	}}.Human(&buf)
	out := buf.String()
	if !strings.Contains(out, "WDT3B55TUP") {
		t.Errorf("the team is not in the listing:\n%s", out)
	}
	if !strings.Contains(out, " - ") {
		t.Errorf("a record with no team recorded shows blank rather than absent:\n%s", out)
	}
}

// The header labels the columns, so it belongs with them: an empty listing has
// no columns to label, only a sentence saying so.
func TestListHeaderAppearsOnlyWithRows(t *testing.T) {
	var withRows, empty bytes.Buffer
	ListResult{Apps: []artifact.Record{{Slug: "app", Title: "App", Config: "Release"}}}.Human(&withRows)
	ListResult{}.Human(&empty)

	if !strings.Contains(withRows.String(), "SLUG") || !strings.Contains(withRows.String(), "TEAM") {
		t.Errorf("no header above the rows:\n%s", withRows.String())
	}
	if strings.Contains(empty.String(), "SLUG") {
		t.Errorf("labeled columns that are not there:\n%s", empty.String())
	}
	if !strings.Contains(empty.String(), "nothing published yet") {
		t.Errorf("an empty listing says nothing at all:\n%s", empty.String())
	}
}
