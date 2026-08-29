package app

import (
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/storage"
)

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

	// The index the server should be able to serve.
	if err := store.WriteFile(filepath.Join(store.Public(), "index.html"), []byte("<html>")); err != nil {
		t.Fatal(err)
	}

	// The real server, stripping the real prefix, on a loopback port.
	srv, err := server.New(store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)
	a.Config.Port = ln.Addr().(*net.TCPAddr).Port

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
