package app

import (
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/storage"
)

// serveThisRoot runs a real server for a on a loopback port and points a at
// it, so doctor gets past its server checks to the ones under test.
func serveThisRoot(t *testing.T, a *App) {
	t.Helper()
	if err := a.Store.WriteFile(filepath.Join(a.Store.Public(), "index.html"), []byte("<html>")); err != nil {
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
