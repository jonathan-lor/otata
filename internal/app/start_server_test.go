package app

import (
	"runtime"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/storage"
)

// A background server exists only under launchd. With no agent installed for
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
