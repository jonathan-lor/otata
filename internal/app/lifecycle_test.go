package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/storage"
)

// A background server exists only under launchd or systemd. With no agent installed for
// this root, StartServer refuses and names the setup command.
// Port 1 is used because nothing can be listening there.
func TestStartServerRefusesWithoutAutostart(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{Port: 1}}

	err = a.StartServer()
	if err == nil {
		t.Fatal("StartServer succeeded with no agent and no server; something was spawned")
	}
	f := cli.AsFailure(err)
	if f.Code != cli.CodeServerDown {
		t.Errorf("code = %q, want %q (%s)", f.Code, cli.CodeServerDown, f.Message)
	}
	if !strings.Contains(f.Hint, "otata serve") {
		t.Errorf("the hint does not name the foreground escape hatch: %q", f.Hint)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(f.Hint, "autostart on") {
		t.Errorf("the hint does not name the setup command: %q", f.Hint)
	}
}

func TestStopServerWaitsForEmbeddedNodeShutdown(t *testing.T) {
	a := freshApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Otata", "1")
		w.Header().Set("X-Otata-Root", a.RootDigest())
		// No PID: if StopServer stops waiting too soon, it must fail instead
		// of signaling the test process while the service is shutting down.
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	a.Config.Port = server.Listener.Addr().(*net.TCPAddr).Port
	sup := &switchingSupervisor{fakeSupervisor: &fakeSupervisor{
		available: true, loaded: true,
		installed: &agentSpec{Root: a.Root, Port: a.Config.Port},
	}}
	sup.stop = func() error {
		// Longer than the old three-second stop budget, within tsnet's
		// five-second log-flush timeout. Unload can return before exit.
		timer := time.AfterFunc(3500*time.Millisecond, server.Close)
		t.Cleanup(func() { timer.Stop() })
		return nil
	}
	a.sup = sup
	if err := a.StopServer(); err != nil {
		t.Fatalf("normal embedded-node shutdown on port %d: %v", a.Config.Port, err)
	}
	if a.ServerRunning() {
		t.Fatal("stop returned before local identity disappeared")
	}
}
