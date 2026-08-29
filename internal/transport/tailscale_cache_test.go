package transport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const statusReadyJSON = `{"Self": {"DNSName": "host.tailnet.ts.net."}, "CertDomains": ["host.tailnet.ts.net"]}`

// stubTailscale writes a fake tailscale CLI that answers from canned JSON and
// logs every invocation, so a test can count how often the real one would have
// been spawned.
func stubTailscale(t *testing.T, serveOut, statusOut string) (bin, callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.log")
	serveFile := filepath.Join(dir, "serve.json")
	statusFile := filepath.Join(dir, "status.json")
	for path, body := range map[string]string{serveFile: serveOut, statusFile: statusOut} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin = filepath.Join(dir, "tailscale")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$*" in
  "serve status --json") cat %q ;;
  "status --json") cat %q ;;
esac
exit 0
`, callLog, serveFile, statusFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, callLog
}

func countCalls(t *testing.T, callLog, args string) int {
	t.Helper()
	raw, err := os.ReadFile(callLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line == args {
			n++
		}
	}
	return n
}

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
