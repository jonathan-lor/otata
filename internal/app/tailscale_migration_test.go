package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
)

func migrationCLI(t *testing.T, status string, failRemoval bool) string {
	t.Helper()
	dir := t.TempDir()
	response, calls := filepath.Join(dir, "status.json"), filepath.Join(dir, "calls")
	if err := os.WriteFile(response, []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	exit := 0
	if failRemoval {
		exit = 1
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
if [ "$*" = "serve status --json" ]; then
  while IFS= read -r line || [ -n "$line" ]; do printf '%%s\n' "$line"; done < %q
  exit 0
fi
exit %d
`, calls, response, exit)
	if err := os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return calls
}

func legacyApp(t *testing.T) *App {
	t.Helper()
	a := freshApp(t)
	a.Config.TSNet = nil
	a.sup = &fakeSupervisor{}
	if err := config.Save(a.Root, a.Config); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLegacyCommandsRequireExplicitMigration(t *testing.T) {
	log := migrationCLI(t, `{}`, false)
	a := legacyApp(t)
	before, _ := os.ReadFile(config.Path(a.Root))
	for name, action := range map[string]func() error{
		"serve": a.Serve, "start": a.StartServer, "autostart": a.EnableAutostart,
		"login": a.LoginTransport,
	} {
		if err := action(); err == nil || cli.AsFailure(err).Code != cli.CodeNeedsSetup || !strings.Contains(cli.AsFailure(err).Hint, "otata transport use tailscale") {
			t.Fatalf("%s did not request migration: %v", name, err)
		}
	}
	s, err := a.Status()
	if err != nil || s.Transport.State != "migration_required" || s.Transport.Ready {
		t.Fatalf("status = %+v, %v", s, err)
	}
	d, err := a.Doctor(true)
	if err != nil || d.Healthy {
		t.Fatalf("doctor = %+v, %v", d, err)
	}
	after, _ := os.ReadFile(config.Path(a.Root))
	if string(before) != string(after) {
		t.Fatal("ordinary command changed legacy config")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("ordinary command invoked host CLI")
	}
	if len(a.sup.(*fakeSupervisor).calls) != 0 {
		t.Fatal("ordinary command changed service")
	}
}

func TestTailscaleMigrationScopesCleanupAndPreservesBuilds(t *testing.T) {
	for _, prefix := range []string{"/otata", "/"} {
		t.Run(prefix, func(t *testing.T) {
			a := legacyApp(t)
			a.Config.ServePath = prefix
			if err := config.Save(a.Root, a.Config); err != nil {
				t.Fatal(err)
			}
			log := migrationCLI(t, fmt.Sprintf(`{"Web":{"host.ts.net:443":{"Handlers":{%q:{"Proxy":"http://127.0.0.1:1"},"/other":{"Proxy":"http://127.0.0.1:9999"}}},"host.ts.net:8443":{"Handlers":{%q:{"Proxy":"http://127.0.0.1:2"}}}}}`, prefix, prefix), false)
			payload := filepath.Join(a.Store.Public(), "retained.bin")
			if err := os.WriteFile(payload, []byte("build"), 0600); err != nil {
				t.Fatal(err)
			}
			var notices []string
			if err := a.UseTransport(TransportSelection{Name: "tailscale", Hostname: "builds"}, func(s string) { notices = append(notices, s) }); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadFile(a.Root)
			if err != nil || cfg.NeedsTailscaleMigration() || cfg.TSNetHostname() != "builds" || cfg.Port != 1 || cfg.ServePath != prefix {
				t.Fatalf("migrated config = %+v, %v", cfg, err)
			}
			data, _ := os.ReadFile(payload)
			if string(data) != "build" {
				t.Fatal("migration changed payload")
			}
			if !strings.Contains(strings.Join(notices, " "), "new URL") {
				t.Fatal("missing URL change notice")
			}
			if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(log)
			want := "serve status --json\nserve --https=443 --set-path=" + prefix + " off\n"
			if string(calls) != want {
				t.Fatalf("host calls = %q, want %q", calls, want)
			}
		})
	}
}

func TestMigrationRefusalsLeaveConfigurationIntact(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		failRemoval  bool
	}{
		{"foreign handler", `{"Web":{"host.ts.net:443":{"Handlers":{"/otata":{"Proxy":"http://127.0.0.1:11"}}}}}`, false},
		{"unreadable status", `bad json`, false},
		{"failed removal", `{"Web":{"host.ts.net:443":{"Handlers":{"/otata":{"Proxy":"http://127.0.0.1:1"}}}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := migrationCLI(t, tc.status, tc.failRemoval)
			a := legacyApp(t)
			before, _ := os.ReadFile(config.Path(a.Root))
			if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err == nil {
				t.Fatal("migration should have failed")
			}
			after, _ := os.ReadFile(config.Path(a.Root))
			if string(before) != string(after) || !a.Config.NeedsTailscaleMigration() {
				t.Fatal("failed migration changed configuration")
			}
			calls, _ := os.ReadFile(log)
			if !tc.failRemoval && strings.Contains(string(calls), " off") {
				t.Fatal("removed an unverified route")
			}
		})
	}
}

func TestMigrationWithNoOldRoute(t *testing.T) {
	log := migrationCLI(t, `{"Web":{"host.ts.net:443":{"Handlers":{"/other":{"Proxy":"http://127.0.0.1:1"}}}}}`, false)
	a := legacyApp(t)
	if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "serve status --json\n" {
		t.Fatalf("unrelated route changed: %s", calls)
	}
}

func TestMigrationRefusesAnotherRootsServer(t *testing.T) {
	log := migrationCLI(t, `{}`, false)
	other := freshApp(t)
	serveThisRoot(t, other)
	a := legacyApp(t)
	a.Config.Port = other.Config.Port
	if err := config.Save(a.Root, a.Config); err != nil {
		t.Fatal(err)
	}
	if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err == nil || cli.AsFailure(err).Code != cli.CodeServerDown {
		t.Fatalf("foreign root accepted: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("migration touched host routing for another root")
	}
	if !other.ServerRunning() {
		t.Fatal("migration stopped another root")
	}
}

func TestMigrationUsesRuntimeRouteWithoutPersistingOverrides(t *testing.T) {
	log := migrationCLI(t, `{"Web":{"host.ts.net:443":{"Handlers":{"/builds":{"Proxy":"http://127.0.0.1:2"}}}}}`, false)
	a := legacyApp(t)
	a.Config.Port, a.Config.ServePath = 2, "/builds"
	if err := a.UseTransport(TransportSelection{Name: "tailscale"}, quiet); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(a.Root)
	if err != nil || cfg.Port != 1 || cfg.ServePath != "/otata" || cfg.NeedsTailscaleMigration() {
		t.Fatalf("persisted overrides: %+v, %v", cfg, err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "serve status --json\nserve --https=443 --set-path=/builds off\n" {
		t.Fatalf("wrong route cleaned up: %s", calls)
	}
}
