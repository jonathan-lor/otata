package transport

import (
	"os"
	"testing"
)

// Reads the real tailscale CLI on this machine when OTATA_TEST_TAILSCALE=1.
// the only proof that the status and serve-status parsers agree with what
// tailscale actually prints, which the canned JSON in transporttest cannot
// give once a release moves a field. Skipped when unset, so the suite stays
// hermetic on a machine with no tailnet.
//
// Every call here is read-only. Ensure and Teardown are left out on purpose,
// since a dev machine may be serving a real otata on this very path.
// Therefore, a renamed `serve` flag is the one drift this test won't catch.
func TestReadsRealTailscale(t *testing.T) {
	if os.Getenv("OTATA_TEST_TAILSCALE") == "" {
		t.Skip("OTATA_TEST_TAILSCALE not set")
	}
	ts := NewTailscale("/otata")
	if ts.bin == "" {
		t.Fatal("no tailscale CLI was resolved; is Tailscale installed?")
	}
	t.Logf("cli=%s", ts.bin)

	// Verify is what refuses a node that is logged out, has no MagicDNS name
	// or has certificates off, so on a working tailnet it proves all three
	// fields still parse from where the struct expects them.
	if err := ts.Verify(); err != nil {
		t.Fatalf("a working tailnet did not verify: %v", err)
	}
	st, err := ts.statusJSON()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dns=%q certs=%v hostname=%q", st.Self.DNSName, st.CertDomains, ts.hostname())

	// The serve config's shape.
	cfg, ok := ts.serveStatus()
	if !ok {
		t.Fatal("serve status --json did not parse")
	}
	for site, web := range cfg.Web {
		for path, h := range web.Handlers {
			t.Logf("serve %s%s -> %s", site, path, h.Proxy)
		}
	}
	t.Logf("visibility=%s wired(8787)=%v", ts.Visibility(), ts.wired(8787))
}
