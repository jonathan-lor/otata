package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatusJSONOmitsUnsetAutostartFlags(t *testing.T) {
	out, err := json.Marshal(StatusResult{})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"autostart_stale", "autostart_disabled", "autostart_other_root", "server_other_root"} {
		if strings.Contains(string(out), absent) {
			t.Errorf("an unset %s was emitted: %s", absent, out)
		}
	}
	out, _ = json.Marshal(StatusResult{AutostartStale: true})
	if !strings.Contains(string(out), `"autostart_stale":true`) {
		t.Errorf("a set autostart_stale was dropped: %s", out)
	}
}

// After `otata stop`, the unit is installed but not loaded.
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
