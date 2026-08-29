package app

import (
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
)

// The transport is chosen once with 'transport use'.
func TestTransportRequiresExplicitSelection(t *testing.T) {
	a := &App{Config: config.Default()}
	if tr := a.selectTransport(); tr != nil {
		t.Fatalf("selected %q with nothing configured", tr.Name())
	}
	_, err := a.Transport()
	f := cli.AsFailure(err)
	if err == nil || f.Code != cli.CodeNoTransport {
		t.Fatalf("unconfigured transport: err=%v, code=%q", err, f.Code)
	}
	if !strings.Contains(f.Hint, "otata transport use") {
		t.Errorf("the hint does not say what to run: %q", f.Hint)
	}
}

// A hand-edited config with a transport outside the closed set is named,
// instead of being read as "nothing selected".
func TestUnknownTransportIsNamed(t *testing.T) {
	a := &App{Config: config.Config{Transport: "wireguard"}}
	_, err := a.Transport()
	f := cli.AsFailure(err)
	if err == nil || f.Code != cli.CodeNoTransport {
		t.Fatalf("unknown transport: err=%v, code=%q", err, f.Code)
	}
	if !strings.Contains(f.Message, "wireguard") {
		t.Errorf("the mistake is not named: %q", f.Message)
	}
}

// An explicit selection is honored without probing anything.
func TestExplicitSelectionIsHonored(t *testing.T) {
	ts := &App{Config: config.Config{Transport: "tailscale", ServePath: "/otata"}}
	if tr := ts.selectTransport(); tr == nil || tr.Name() != "tailscale" {
		t.Errorf("tailscale selected but not returned: %v", tr)
	}
	man := &App{Config: config.Config{Transport: "manual", Manual: &config.Manual{
		BaseURL: "https://box.example.com/otata", Visibility: "private",
	}}}
	if tr := man.selectTransport(); tr == nil || tr.Name() != "manual" {
		t.Errorf("manual selected but not returned: %v", tr)
	}
	// Manual selected with no base URL is still a refusal, with its own message.
	empty := &App{Config: config.Config{Transport: "manual"}}
	_, err := empty.Transport()
	if f := cli.AsFailure(err); err == nil || f.Code != cli.CodeNoTransport || !strings.Contains(f.Message, "base URL") {
		t.Errorf("manual without base URL: err=%v", err)
	}
}
