// Package transporttest fakes the tailscale CLI, so a test in any package
// that drives the tailscale transport can dictate what `tailscale` answers
// and count how often the real one would have been spawned.
package transporttest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shapes taken from real CLI output.
const (
	// StatusReady is a logged-in node with MagicDNS and HTTPS certificates.
	StatusReady = `{"Self": {"DNSName": "host.tailnet.ts.net."}, "CertDomains": ["host.tailnet.ts.net"]}`
	// ServeUnwired is a serve config with nothing mounted.
	ServeUnwired = `{"Web": {}}`
	// ServeFunnelled is a :443 listener that Funnel exposes to the internet,
	// with someone else's handler on it and none of otata's yet.
	ServeFunnelled = `{
  "TCP": {"443": {"HTTPS": true}},
  "Web": {"host.tailnet.ts.net:443": {"Handlers": {"/blog": {"Proxy": "http://127.0.0.1:3000"}}}},
  "AllowFunnel": {"host.tailnet.ts.net:443": true}
}`
)

// Stub writes a fake tailscale CLI that answers `serve status --json` with
// serveOut and `status --json` with statusOut, exits 0 for anything else, and
// logs every invocation. It returns the binary's path and the log's. Putting
// the binary's directory on PATH is what makes the transport find it, and the
// script uses shell builtins only, so that directory can be the whole PATH.
func Stub(t *testing.T, serveOut, statusOut string) (bin, callLog string) {
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
emit() { while IFS= read -r line || [ -n "$line" ]; do printf '%%s\n' "$line"; done < "$1"; }
case "$*" in
  "serve status --json") emit %q ;;
  "status --json") emit %q ;;
esac
exit 0
`, callLog, serveFile, statusFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, callLog
}

// Calls counts how often the stub was invoked with exactly args.
func Calls(t *testing.T, callLog, args string) int {
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
