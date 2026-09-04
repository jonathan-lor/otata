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
