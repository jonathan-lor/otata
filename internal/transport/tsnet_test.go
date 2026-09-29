package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jonathan-lor/otata/internal/tsnetnode"
)

func tsnetControl(t *testing.T, status func() tsnetnode.Status) *TSNet {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/status" || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unexpected control request", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(status())
	}))
	t.Cleanup(server.Close)
	tr := NewTSNet(t.TempDir(), tsnetnode.Identity{Root: "test", Port: 18877, Prefix: "/otata", Hostname: "test"})
	data, err := json.Marshal(map[string]any{
		"version": 1, "identity": tr.Client.Identity, "url": server.URL, "token": "test-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tsnetnode.ReceiptPath(tr.Client.Dir), data, 0600); err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestTSNetEnsureWaitsForStartup(t *testing.T) {
	states := []string{"starting", "connecting", "certificate_pending", "ready"}
	var calls atomic.Int32
	tr := tsnetControl(t, func() tsnetnode.Status {
		i := min(int(calls.Add(1))-1, len(states)-1)
		s := tsnetnode.Status{State: states[i]}
		if s.State == "ready" {
			s.Ready, s.BaseURL = true, "https://test.ts.net/otata"
		}
		return s
	})
	base, err := tr.Ensure(tr.Client.Identity.Port)
	if err != nil || base != "https://test.ts.net/otata" || calls.Load() != 4 {
		t.Fatalf("startup: base=%q err=%v calls=%d", base, err, calls.Load())
	}
}

func TestTSNetEnsureReturnsActionableFailures(t *testing.T) {
	for _, state := range []string{"needs_login", "needs_approval", "dns_disabled", "https_disabled", "error"} {
		t.Run(state, func(t *testing.T) {
			var calls atomic.Int32
			tr := tsnetControl(t, func() tsnetnode.Status {
				calls.Add(1)
				return tsnetnode.Status{State: state, Detail: "action required", AuthURL: "https://login.tailscale.com/test"}
			})
			base, err := tr.Ensure(tr.Client.Identity.Port)
			var unavailable *ErrUnavailable
			if base != "" || !errors.As(err, &unavailable) || unavailable.Reason != "action required; login: https://login.tailscale.com/test" || calls.Load() != 1 {
				t.Fatalf("failure: base=%q err=%v calls=%d", base, err, calls.Load())
			}
		})
	}
}
