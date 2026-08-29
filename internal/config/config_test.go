package config

import (
	"os"
	"path/filepath"
	"testing"
)

// These are documented in the README and in an error hint that tells the user to set OTATA_PORT.
func TestEnvironmentOverridesFile(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, Config{Port: 8787, ServePath: "/otata", Transport: "tailscale"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTATA_PORT", "9123")
	t.Setenv("OTATA_PATH", "builds")

	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 9123 {
		t.Errorf("port = %d, want 9123", c.Port)
	}
	if c.ServePath != "/builds" {
		t.Errorf("serve path = %q, want /builds (leading slash added)", c.ServePath)
	}
	// The file must be untouched, so a one-off override is not persisted.
	onDisk, err := LoadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Port != 8787 || onDisk.ServePath != "/otata" {
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

// A torn config makes every command fail, including doctor --fix.
func TestSaveIsAtomic(t *testing.T) {
	root := t.TempDir()
	if err := Save(root, Config{Port: 1, ServePath: "/a"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, Config{Port: 2, ServePath: "/bbbbbbbbbbbbbbbb"}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(root)
	if err != nil {
		t.Fatalf("config unreadable after rewrite: %v", err)
	}
	if c.Port != 2 {
		t.Errorf("port = %d, want 2", c.Port)
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Errorf("left a stray file behind: %s", e.Name())
		}
	}
	info, _ := os.Stat(filepath.Join(root, "config.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestMissingConfigIsNotAnError(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("a fresh install should not need a config file: %v", err)
	}
	if c.Port != DefaultPort || c.ServePath != DefaultServePath {
		t.Errorf("defaults not applied: %+v", c)
	}
}
