package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/transport"
)

func TestTSNetSelectionWithoutHostInstallation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("OTATA_PATH", "/ignored-service-path")
	t.Setenv("OTATA_PORT", "1")
	a := freshApp(t)
	// Selecting the embedded node needs no host CLI or path configuration.
	if err := os.WriteFile(config.Path(a.Root), []byte(`{"transport":"tailscale","serve_path":"/old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	a.Config, err = config.Load(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UseTransport(TransportSelection{Name: "tailscale", Hostname: "dev-node"}, quiet); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(a.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != "tailscale" || cfg.TSNetHostname() != "dev-node" || cfg.Port != config.DefaultPort {
		t.Fatalf("persisted config: %+v", cfg)
	}
	tr, err := a.Transport()
	if err != nil {
		t.Fatal(err)
	}
	if tr.IncomingPrefix() != "" {
		t.Fatal("Tailscale retained an obsolete path prefix")
	}
	if s := tr.Status(a.Config.Port); s.Ready || s.State != "stopped" {
		t.Fatalf("unstarted selection claimed ready: %+v", s)
	}
	if _, err := tr.Ensure(a.Config.Port); err == nil {
		t.Fatal("ensure accepted missing daemon")
	}
	if _, err := os.Stat(a.tsnetDir()); !os.IsNotExist(err) {
		t.Fatalf("selection or status created node state: %v", err)
	}
	before, err := os.ReadFile(config.Path(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UseTransport(TransportSelection{Name: "tailscale", Hostname: "bad/name"}, quiet); err == nil {
		t.Fatal("invalid hostname accepted")
	}
	after, err := os.ReadFile(config.Path(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("invalid selection changed config")
	}
}

type switchingSupervisor struct {
	*fakeSupervisor
	start, stop func() error
}

func (s *switchingSupervisor) Load() error {
	if err := s.fakeSupervisor.Load(); err != nil {
		return err
	}
	return s.start()
}
func (s *switchingSupervisor) Unload() error {
	if err := s.fakeSupervisor.Unload(); err != nil {
		return err
	}
	return s.stop()
}

// Changing between manual and tsnet must restart the process that owns the
// node, even when the manual proxy also serves at the root.
func TestTSNetSwitchRestartsManagedServer(t *testing.T) {
	for _, from := range []string{"tailscale", "manual"} {
		t.Run(from, func(t *testing.T) {
			a := freshApp(t)
			a.Config.Transport = from
			a.Config.Manual = &config.Manual{BaseURL: "https://old.example.com", KeepPrefix: true}
			if err := config.Save(a.Root, a.Config); err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			a.Config.Port = ln.Addr().(*net.TCPAddr).Port
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Otata", "1")
				w.Header().Set("X-Otata-Root", a.RootDigest())
				w.WriteHeader(200)
			})
			var current *http.Server
			serve := func(l net.Listener) { current = &http.Server{Handler: handler}; go current.Serve(l) }
			serve(ln)
			defer func() { current.Close() }()
			sup := &switchingSupervisor{fakeSupervisor: &fakeSupervisor{available: true, loaded: true, installed: &agentSpec{Program: "/fake/otata", Root: a.Root, Port: a.Config.Port}}}
			sup.stop = func() error { return current.Close() }
			sup.start = func() error {
				l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", a.Config.Port))
				if err != nil {
					return err
				}
				serve(l)
				return nil
			}
			a.sup = sup
			sel := TransportSelection{Name: "tailscale", Hostname: "dev-node"}
			if from == "tailscale" {
				sel = TransportSelection{Name: "manual", BaseURL: "https://new.example.com", KeepPrefix: true}
			}
			if err := a.UseTransport(sel, quiet); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sup.calls, []string{"unload", "load"}) {
				t.Fatalf("switch failed to restart: %v", sup.calls)
			}
			if !a.ServerRunning() {
				t.Fatal("managed server did not return")
			}
		})
	}
}

type embeddedProbeTransport struct{ targets []string }

func (*embeddedProbeTransport) Name() string                     { return "tailscale" }
func (*embeddedProbeTransport) Visibility() transport.Visibility { return transport.Private }
func (*embeddedProbeTransport) IncomingPrefix() string           { return "" }
func (*embeddedProbeTransport) Ensure(int) (string, error) {
	return "https://unresolvable.invalid", nil
}
func (*embeddedProbeTransport) Status(int) transport.Status {
	return transport.Status{Name: "tailscale", Ready: true, BaseURL: "https://unresolvable.invalid", Visibility: transport.Private}
}
func (t *embeddedProbeTransport) Probe(ctx context.Context, target string) (int, error) {
	t.targets = append(t.targets, target)
	return http.StatusOK, nil
}

func TestDoctorUsesEmbeddedNetwork(t *testing.T) {
	a := freshApp(t)
	tr := &embeddedProbeTransport{}
	a.setTransport(tr)
	if err := a.Reindex("https://unresolvable.invalid"); err != nil {
		t.Fatal(err)
	}
	serveThisRoot(t, a)
	r, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Healthy || len(tr.targets) != 1 || !strings.HasPrefix(tr.targets[0], "https://unresolvable.invalid/") {
		t.Fatalf("doctor did not use embedded path: %+v %v", r, tr.targets)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "state", "tsnet")); !os.IsNotExist(err) {
		t.Fatal("doctor created a second node")
	}
}
