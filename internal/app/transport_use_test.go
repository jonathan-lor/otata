package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/storage"
	"github.com/jonathan-lor/otata/internal/transport/transporttest"
)

// freshApp is an App on a scratch root with nothing configured. Port 1 so a
// probe, should one happen, finds nothing rather than a real server.
func freshApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Port = 1
	return &App{Root: root, Store: store, Config: cfg}
}

func quiet(string) {}

// Selecting the manual transport persists exactly that selection and
// regenerates the pages against its base URL.
func TestUseTransportPersistsAndReindexes(t *testing.T) {
	a := freshApp(t)
	sel := TransportSelection{Name: "manual", BaseURL: "https://box.example.com/otata", Visibility: "private"}
	if err := a.UseTransport(sel, quiet); err != nil {
		t.Fatal(err)
	}
	onDisk, err := config.LoadFile(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Transport != "manual" || onDisk.Manual == nil || onDisk.Manual.BaseURL != sel.BaseURL {
		t.Errorf("config on disk = %+v, want the manual selection", onDisk)
	}
	// The one-off port override must not have been persisted with it.
	if onDisk.Port != config.DefaultPort {
		t.Errorf("port %d was persisted; the file should keep its own", onDisk.Port)
	}
	index, err := os.ReadFile(filepath.Join(a.Store.Public(), "index.html"))
	if err != nil {
		t.Fatalf("no index was generated: %v", err)
	}
	if !strings.Contains(string(index), "box.example.com") {
		t.Error("the index was not regenerated against the new base URL")
	}
}

// A selection that is refused changes nothing: no config is written and no
// page generated. The usage errors are the caller's, so they exit 2; a route
// declared public is refused by the guard every later command applies, and
// used to be saved first and refused by all of them after.
func TestUseTransportRefusesBadSelectionsBeforeChangingAnything(t *testing.T) {
	cases := []struct {
		name string
		sel  TransportSelection
		want string
	}{
		{"unknown transport", TransportSelection{Name: "wireguard"}, cli.CodeInvalidArgs},
		{"manual without a base URL", TransportSelection{Name: "manual", Visibility: "private"}, cli.CodeInvalidArgs},
		{"manual over http", TransportSelection{Name: "manual", BaseURL: "http://x/otata", Visibility: "private"}, cli.CodeInvalidArgs},
		{"manual with a made-up visibility", TransportSelection{Name: "manual", BaseURL: "https://x/otata", Visibility: "internal"}, cli.CodeInvalidArgs},
		{"manual declared public", TransportSelection{Name: "manual", BaseURL: "https://x/otata", Visibility: "public"}, cli.CodeTransportDown},
	}
	for _, c := range cases {
		a := freshApp(t)
		err := a.UseTransport(c.sel, quiet)
		if f := cli.AsFailure(err); err == nil || f.Code != c.want {
			t.Errorf("%s: err=%v code=%q, want %q", c.name, err, f.Code, c.want)
		}
		if _, err := os.Stat(config.Path(a.Root)); err == nil {
			t.Errorf("%s: a refused selection wrote the config", c.name)
		}
		if _, err := os.Stat(filepath.Join(a.Store.Public(), "index.html")); err == nil {
			t.Errorf("%s: a refused selection generated pages", c.name)
		}
	}
}

// useStubTailscale puts a fake tailscale CLI first on PATH for the test, so
// the transport resolves it instead of a real one. Nothing else this
// command spawns is on that PATH either, which is the point: no real
// launchd, keychain or tailnet is consulted.
func useStubTailscale(t *testing.T, serveOut string) (callLog string) {
	t.Helper()
	bin, callLog := transporttest.Stub(t, serveOut, transporttest.StatusReady)
	t.Setenv("PATH", filepath.Dir(bin))
	return callLog
}

// The headline case: Tailscale itself is fine, but Funnel is on for the
// listener otata mounts on, so every handler there is public. Selection must
// refuse with the config it found left exactly as it was. It used to tear
// down the previous transport, save, and report success, after which every
// command refused the transport it had just been told to use.
func TestUseTransportRefusesAFunnelledTailnetBeforeChangingAnything(t *testing.T) {
	useStubTailscale(t, transporttest.ServeFunnelled)
	a := freshApp(t)
	previous := config.Config{Port: config.DefaultPort, ServePath: "/otata", Transport: "manual",
		Manual: &config.Manual{BaseURL: "https://old.example.com/otata", Visibility: "private"}}
	if err := config.Save(a.Root, previous); err != nil {
		t.Fatal(err)
	}
	a.Config.Transport, a.Config.Manual = previous.Transport, previous.Manual
	before, err := os.ReadFile(config.Path(a.Root))
	if err != nil {
		t.Fatal(err)
	}

	err = a.UseTransport(TransportSelection{Name: "tailscale"}, quiet)
	f := cli.AsFailure(err)
	if err == nil || f.Code != cli.CodeTransportDown {
		t.Fatalf("funnelled tailnet: err=%v code=%q, want %q", err, f.Code, cli.CodeTransportDown)
	}
	if !strings.Contains(f.Message, "public") || !strings.Contains(f.Hint, "funnel") {
		t.Errorf("the refusal does not name Funnel: %q / %q", f.Message, f.Hint)
	}
	after, err := os.ReadFile(config.Path(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the config changed under a refused selection:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(a.Store.Public(), "index.html")); err == nil {
		t.Error("a refused selection generated pages")
	}
}

// The same tailnet without Funnel is selected: verified, wired, persisted,
// and the pages regenerated against its MagicDNS name.
func TestUseTransportSelectsATailnetThatCanServe(t *testing.T) {
	calls := useStubTailscale(t, transporttest.ServeUnwired)
	a := freshApp(t)
	if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err != nil {
		t.Fatal(err)
	}
	onDisk, err := config.LoadFile(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Transport != "tailscale" {
		t.Errorf("config on disk selects %q, want tailscale", onDisk.Transport)
	}
	if n := transporttest.Calls(t, calls, "serve --bg --https=443 --set-path=/otata http://127.0.0.1:1"); n != 1 {
		t.Errorf("the serve path was wired %d times, want once", n)
	}
	index, err := os.ReadFile(filepath.Join(a.Store.Public(), "index.html"))
	if err != nil {
		t.Fatalf("no index was generated: %v", err)
	}
	if !strings.Contains(string(index), "host.tailnet.ts.net") {
		t.Error("the index was not regenerated against the tailnet name")
	}
}
