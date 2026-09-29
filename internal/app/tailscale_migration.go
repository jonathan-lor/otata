package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
)

func tailscaleMigrationRequired() error {
	return cli.Fail(cli.CodeNeedsSetup, "this installation uses the removed host Tailscale transport").
		WithHint("run 'otata transport use tailscale' to remove the old Serve route, then 'otata autostart on' and 'otata transport login'; otata needs its own enrollment and will have a new URL; published builds are retained")
}

// The host CLI is used only to retire a released installation's Serve route.
// Never reset Serve or remove a handler that points at another application.
func cleanupLegacyTailscale(cfg config.Config) error {
	bin, _ := exec.LookPath("tailscale")
	if bin == "" {
		for _, p := range []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"} {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		return fmt.Errorf("host Tailscale CLI not found (needed once for migration)")
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("tailscale %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	out, err := run("serve", "status", "--json")
	if err != nil {
		return err
	}
	var status struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return fmt.Errorf("read old Serve configuration: %w", err)
	}
	path := strings.TrimSuffix(cfg.ServePath, "/")
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	found := false
	for host, site := range status.Web {
		if !strings.HasSuffix(host, ":443") {
			continue
		}
		if h, ok := site.Handlers[path]; ok {
			if h.Proxy != target {
				return fmt.Errorf("Serve handler %s%s no longer points to %s; inspect it with 'tailscale serve status' before migrating", host, path, target)
			}
			found = true
		}
	}
	if found {
		_, err = run("serve", "--https=443", "--set-path="+path, "off")
	}
	return err
}
