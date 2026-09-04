package transport

import (
	"errors"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/transport/transporttest"
)

// The fake CLI lives in transporttest so the app package can drive the
// transport through it too.
var (
	stubTailscale   = transporttest.Stub
	countCalls      = transporttest.Calls
	statusReadyJSON = transporttest.StatusReady
)

// One command asks the same transport several questions about one moment:
// doctor wants visibility, the hostname and the wiring, and used to shell out
// to the tailscale CLI for each. The value lives for one invocation, so each
// read happens once.
func TestCLIReadsAreMemoizedPerInstance(t *testing.T) {
	bin, calls := stubTailscale(t, serveJSON, statusReadyJSON)
	ts := &Tailscale{bin: bin, servePath: "/otata"}

	if ts.Visibility() != Private {
		t.Fatal("stub config is not funnelled; visibility should be private")
	}
	_ = ts.Visibility()
	if !ts.wired(8787) {
		t.Fatal("stub config wires /otata to 8787")
	}
	if got := countCalls(t, calls, "serve status --json"); got != 1 {
		t.Errorf("serve status fetched %d times in one command, want 1", got)
	}

	for range 2 {
		if ts.hostname() == "" {
			t.Fatal("the stub's MagicDNS name did not resolve")
		}
	}
	if got := countCalls(t, calls, "status --json"); got != 1 {
		t.Errorf("status fetched %d times in one command, want 1", got)
	}
}

// Ensure is the mutation inside the memo's window. After it wires the serve
// path, an answer from before the wiring is stale, so the memo must be
// dropped. Kept, doctor --fix would report the transport it had just wired as
// still unwired.
func TestEnsureDropsTheServeMemo(t *testing.T) {
	bin, calls := stubTailscale(t, `{"Web": {}}`, statusReadyJSON)
	ts := &Tailscale{bin: bin, servePath: "/otata"}

	if ts.wired(8787) {
		t.Fatal("stub config starts unwired")
	}
	if _, err := ts.Ensure(8787); err != nil {
		t.Fatal(err)
	}
	if countCalls(t, calls, "serve --bg --https=443 --set-path=/otata http://127.0.0.1:8787") != 1 {
		t.Fatal("Ensure did not issue the wiring command")
	}
	_ = ts.wired(8787)
	if got := countCalls(t, calls, "serve status --json"); got != 2 {
		t.Errorf("after wiring, wired() made %d fetches, want 2: a stale memo answered", got)
	}
}

// Status has to say whether Ensure would mend an unready transport, because
// doctor prescribes --fix on that answer. Verified but unwired is the one
// repairable state; a node that fails Verify is an obstacle on the machine.
func TestStatusSaysWhetherEnsureWouldRepair(t *testing.T) {
	bin, _ := stubTailscale(t, `{"Web": {}}`, statusReadyJSON)
	unwired := (&Tailscale{bin: bin, servePath: "/otata"}).Status(8787)
	if unwired.Ready || !unwired.Repairable {
		t.Errorf("verified but unwired: ready=%v repairable=%v, want unready and repairable", unwired.Ready, unwired.Repairable)
	}

	bin, _ = stubTailscale(t, serveJSON, `{"Self": {"DNSName": ""}, "CertDomains": []}`)
	noDNS := (&Tailscale{bin: bin, servePath: "/otata"}).Status(8787)
	if noDNS.Ready || noDNS.Repairable {
		t.Errorf("no MagicDNS: ready=%v repairable=%v, want neither", noDNS.Ready, noDNS.Repairable)
	}
	if !strings.Contains(noDNS.Detail, "MagicDNS") {
		t.Errorf("the obstacle is not named: %q", noDNS.Detail)
	}
}

// A failed mutation is explained by what the CLI printed, and by the exec
// error only when it printed nothing. "exit status 1" told the user nothing
// about why a teardown failed.
func TestCLIErrorPrefersTheCLIsOwnWords(t *testing.T) {
	err := cliError("tailscale serve failed", []byte("  error: not logged in\n"), errors.New("exit status 1"))
	if got := err.Error(); got != "tailscale serve failed: error: not logged in" {
		t.Errorf("got %q", got)
	}
	err = cliError("tailscale serve failed", nil, errors.New("signal: killed"))
	if got := err.Error(); got != "tailscale serve failed: signal: killed" {
		t.Errorf("silent failure: got %q", got)
	}
}

// Teardown also changes the serve config, whoever reads next.
func TestTeardownDropsTheServeMemo(t *testing.T) {
	bin, calls := stubTailscale(t, serveJSON, statusReadyJSON)
	ts := &Tailscale{bin: bin, servePath: "/otata"}

	_ = ts.wired(8787)
	if err := ts.Teardown(); err != nil {
		t.Fatal(err)
	}
	_ = ts.wired(8787)
	if got := countCalls(t, calls, "serve status --json"); got != 2 {
		t.Errorf("after teardown, %d fetches, want 2: a stale memo answered", got)
	}
}
