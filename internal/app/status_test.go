package app

import (
	"strings"
	"testing"
)

// Autostart answers "does it return at login"; loaded answers "is launchd
// managing it now". After `otata stop` they differ, and status must say so
// rather than report autostart off about a server that is back next login.
func TestStatusHumanDistinguishesInstalledFromLoaded(t *testing.T) {
	var out strings.Builder
	StatusResult{Autostart: true, AutostartLoaded: false, Port: 8787}.Human(&out)
	if !strings.Contains(out.String(), "autostart on") || !strings.Contains(out.String(), "not loaded") {
		t.Errorf("installed-but-unloaded agent not reported:\n%s", out.String())
	}
	out.Reset()
	StatusResult{Autostart: true, AutostartLoaded: true, Port: 8787}.Human(&out)
	if strings.Contains(out.String(), "not loaded") {
		t.Errorf("a loaded agent reported as unloaded:\n%s", out.String())
	}
}
