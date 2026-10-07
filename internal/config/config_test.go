package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// These are documented in docs/cli-reference.md and in an error hint that tells the user to set OTATA_PORT.
func TestEnvironmentOverridesFile(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, Config{Port: 8787, Transport: "tailscale"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTATA_PORT", "9123")

	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9123 {
		t.Errorf("port = %d, want 9123", c.Port)
	}
	// The file must be untouched, so a one-off override is not persisted.
	onDisk, err := LoadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Port != 8787 {
		t.Errorf("LoadFile saw the override: %+v", onDisk)
	}
}

func TestInvalidPortIsReported(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{"nope", "0", "70000", "-1"} {
		t.Setenv("OTATA_PORT", bad)
		if _, err := Load(root); err == nil {
			t.Errorf("OTATA_PORT=%q was accepted", bad)
		}
	}
}

// The config is the user's alone. The staging and the rename are
// atomicfile's, tested there; the mode is this package's to choose.
func TestSaveIsPrivate(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, Config{Port: 1}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a fresh install should not need a config file: %v", err)
	}
	if c.Port != DefaultPort || c.Transport != "tailscale" || c.TSNetHostname() != "otata" {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestTransportDefaultsAndRemovedPaths(t *testing.T) {
	t.Setenv("OTATA_PATH", "/obsolete-service-path")
	for _, tc := range []struct {
		body, transport, hostname string
	}{
		{`{}`, "tailscale", "otata"},
		{`{"transport":""}`, "tailscale", "otata"},
		{`{"transport":"tailscale","serve_path":"/otata"}`, "tailscale", "otata"},
		{`{"transport":"tailscale","serve_path":"/custom","tsnet":{"hostname":"builds"}}`, "tailscale", "builds"},
		{`{"transport":"tsnet","tsnet":{"hostname":"builds"}}`, "tailscale", "builds"},
		{`{"transport":"tailscale","tsnet":null}`, "tailscale", "otata"},
		{`{"transport":"manual","serve_path":"/ignored","manual":{"base_url":"https://example.com/custom","keep_prefix":true}}`, "manual", "otata"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(Path(root), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadFile(root)
			if err != nil || c.Transport != tc.transport || c.TSNetHostname() != tc.hostname {
				t.Fatalf("load = %+v, %v", c, err)
			}
			runtime, err := Load(root)
			if err != nil || !reflect.DeepEqual(runtime, c) {
				t.Fatalf("obsolete OTATA_PATH changed config: %+v, %v", runtime, err)
			}
			data, _ := os.ReadFile(Path(root))
			if string(data) != tc.body {
				t.Fatal("reading config changed it")
			}
			if err := Save(root, c); err != nil {
				t.Fatal(err)
			}
			reloaded, err := LoadFile(root)
			if err != nil || !reflect.DeepEqual(reloaded, c) {
				t.Fatalf("save/load changed configuration: %+v, %v", reloaded, err)
			}
			data, _ = os.ReadFile(Path(root))
			if strings.Contains(string(data), "serve_path") {
				t.Fatal("saved configuration retained the removed path setting")
			}
		})
	}
}
