package app

import (
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/transport"
)

func TestDefaultTransportNeedsNoHostInstallation(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	a := &App{Root: t.TempDir(), Config: config.Default()}
	tr, err := a.Transport()
	if _, ok := tr.(*transport.TSNet); err != nil || !ok || tr.Name() != "tailscale" {
		t.Fatalf("default transport = %v, %v", tr, err)
	}
	if tr != a.selectTransport() {
		t.Fatal("transport was not cached")
	}
}

func TestInvalidTransportConfiguration(t *testing.T) {
	for _, tc := range []struct {
		cfg     config.Config
		message string
	}{
		{config.Config{Transport: "wireguard"}, "wireguard"},
		{config.Config{Transport: "manual"}, "base URL"},
	} {
		a := &App{Config: tc.cfg}
		_, err := a.Transport()
		if err == nil || cli.AsFailure(err).Code != cli.CodeNoTransport || !strings.Contains(err.Error(), tc.message) {
			t.Fatalf("invalid configuration: %v", err)
		}
	}
}
