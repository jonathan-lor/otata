package app

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/appmeta"
	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/storage"
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

// serveThisRoot runs a real server for a on a loopback port and points a at
// it, so doctor gets past its server checks to the ones under test.
func serveThisRoot(t *testing.T, a *App) {
	t.Helper()
	if err := a.Store.WriteFile(a.Store.IndexPath(), []byte("<html>")); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(a.Store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(ln, srv)
	t.Cleanup(func() { ln.Close(); srv.Close() })
	a.Config.Port = ln.Addr().(*net.TCPAddr).Port
}

// A transport that is unready for a reason --fix cannot mend must be reported
// as that reason. It used to say "not wired; 'otata doctor --fix' wires it"
// for every unready transport, and --fix then failed with the real one. The
// obstacle here is a base URL the config validation refuses, which a
// hand-edited config can hold and which needs no Tailscale to reproduce.
func TestDoctorReportsTheTransportObstacleNotWiring(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{
		ServePath: "/otata",
		Transport: "manual",
		Manual:    &config.Manual{BaseURL: "http://127.0.0.1:1/otata", Visibility: "private"},
	}}
	serveThisRoot(t, a)

	res, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Healthy {
		t.Error("an unusable transport reported healthy")
	}
	found := false
	for _, c := range res.Checks {
		if c.Name != "transport" {
			continue
		}
		found = true
		if c.OK {
			t.Error("the transport check passed")
		}
		if !strings.Contains(c.Detail, "https") {
			t.Errorf("the obstacle is not named: %q", c.Detail)
		}
		if strings.Contains(c.Detail, "wired") || strings.Contains(c.Detail, "--fix") {
			t.Errorf("a repair was promised that --fix cannot make: %q", c.Detail)
		}
	}
	if !found {
		t.Errorf("no transport check at all: %+v", res.Checks)
	}
}

// The URL probes fly concurrently but the report must read as it always has:
// the index, then each app's manifest, payload and signing, newest app first.
func TestDoctorReportsChecksInOrderHoweverProbesLand(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{
		ServePath: "/otata",
		Transport: "manual",
		Manual: &config.Manual{
			// Port 1 answers nothing, so every URL probe fails fast without
			// leaving the machine; the order they report in is what is under test.
			BaseURL:    "https://127.0.0.1:1/otata",
			KeepPrefix: true,
			Visibility: "private",
		},
	}}

	// Two published records. Their payload files are absent on purpose.
	// The signing check has nothing to read and stays silent.
	now := time.Now()
	for slug, built := range map[string]time.Time{"older": now.Add(-time.Hour), "newer": now} {
		if err := store.PutRecord(artifact.Record{Slug: slug, PayloadName: "App.ipa", BuiltAt: built}); err != nil {
			t.Fatal(err)
		}
	}
	serveThisRoot(t, a)

	res, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Checks {
		names = append(names, c.Name)
	}
	want := []string{"index", "newer manifest", "newer payload", "older manifest", "older payload"}
	if !slices.Equal(names, want) {
		t.Errorf("checks = %v, want %v", names, want)
	}
	if res.Healthy {
		t.Error("probes against a dead port reported healthy")
	}
}

// An Android app has no manifest to answer, so doctor probes its payload
// alone, and the report still reads app by app whatever each one has.
func TestDoctorProbesNoManifestForAndroid(t *testing.T) {
	a := freshApp(t)
	a.Config.ServePath, a.Config.Transport = "/otata", "manual"
	// Port 1 answers nothing, so the URL probes fail fast without leaving the machine.
	a.Config.Manual = &config.Manual{BaseURL: "https://127.0.0.1:1/otata", KeepPrefix: true, Visibility: "private"}
	now := time.Now()
	for _, r := range []artifact.Record{
		{Slug: "droid", Platform: artifact.Android, PayloadName: "App.apk", BuiltAt: now},
		{Slug: "iosapp", Platform: artifact.IOS, PayloadName: "App.ipa", BuiltAt: now.Add(-time.Hour)},
	} {
		if err := a.Store.PutRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	serveThisRoot(t, a)

	res, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Checks {
		names = append(names, c.Name)
	}
	want := []string{"index", "droid payload", "iosapp manifest", "iosapp payload"}
	if !slices.Equal(names, want) {
		t.Errorf("checks = %v, want %v", names, want)
	}
}

// Under a keep-prefix manual transport every request reaching the server
// carries the base URL's path, and the bare root is refused by design. Doctor's
// "can the server serve the index" probe must therefore ask with the prefix.
// probing "/" declared every healthy keep-prefix server broken, and doctor
// --fix restarted one on every run, forever.
func TestDoctorProbesIndexThroughIncomingPrefix(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{
		ServePath: "/otata",
		Transport: "manual",
		Manual: &config.Manual{
			// Port 1 answers nothing, so the URL probes that follow the check
			// under test fail fast and without leaving the machine.
			BaseURL:    "https://127.0.0.1:1/otata",
			KeepPrefix: true,
			Visibility: "private",
		},
	}}
	if got := a.IncomingPrefix(); got != "/otata" {
		t.Fatalf("IncomingPrefix() = %q, want /otata", got)
	}
	// The real server, stripping the real prefix, on a loopback port.
	serveThisRoot(t, a)

	res, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	sawIndexProbe := false
	for _, c := range res.Checks {
		if c.Name == "server" {
			t.Errorf("a healthy keep-prefix server was diagnosed broken: %s", c.Detail)
		}
		if c.Name == "index" {
			sawIndexProbe = true
		}
	}
	// The URL probes come after the check under test, so reaching them proves
	// doctor did not bail out early on the false diagnosis.
	if !sawIndexProbe {
		t.Errorf("doctor never reached the URL probes: %+v", res.Checks)
	}
}

// Doctor cannot start a server that autostart does not manage,
// so with no agent the server check fails outright, and the
// detail carries the setup step instead of promising a repair.
func TestDoctorNamesAutostartWhenItCannotStartTheServer(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{
		Port:      1,
		ServePath: "/otata",
		Transport: "manual",
		Manual:    &config.Manual{BaseURL: "https://127.0.0.1:1/otata", KeepPrefix: true, Visibility: "private"},
	}}

	for _, fix := range []bool{false, true} {
		res, err := a.Doctor(fix)
		if err != nil {
			t.Fatal(err)
		}
		if res.Healthy {
			t.Errorf("fix=%v: no server and no way to start one, yet healthy", fix)
		}
		found := false
		for _, c := range res.Checks {
			if c.Name != "server" {
				continue
			}
			found = true
			if c.OK {
				t.Errorf("fix=%v: the server check passed", fix)
			}
			if !strings.Contains(c.Detail, "autostart") {
				t.Errorf("fix=%v: the detail does not name the setup step: %q", fix, c.Detail)
			}
		}
		if !found {
			t.Errorf("fix=%v: no server check at all: %+v", fix, res.Checks)
		}
		if len(res.Repaired) != 0 {
			t.Errorf("fix=%v: claimed a repair it cannot make: %v", fix, res.Repaired)
		}
	}
}
