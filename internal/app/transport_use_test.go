package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/storage"
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
// page generated. The usage errors here are the caller's, so they exit 2.
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
