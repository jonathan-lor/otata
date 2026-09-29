package tsnetnode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/types/empty"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeNode struct {
	startGate     chan struct{}
	startErr      error
	lastListener  net.Listener
	failListen    bool
	mu            sync.Mutex
	events        chan ipn.Notify
	watches       int
	snapshots     int
	certs         int
	listens       int
	failSnapshot  bool
	watchFailures int
	state         Status
	host          string
	certGate      chan struct{}
	certErr       error
	loginCalls    int
	closed        bool
	client        *http.Client
}

func (n *fakeNode) Start() error {
	if n.startGate != nil {
		<-n.startGate
	}
	return n.startErr
}
func (n *fakeNode) Snapshot(context.Context) (Status, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.snapshots++
	if n.failSnapshot {
		n.failSnapshot = false
		return Status{}, "", errors.New("injected snapshot failure")
	}
	return n.state, n.host, nil
}
func (n *fakeNode) Certificate(ctx context.Context, host string) error {
	n.mu.Lock()
	n.certs++
	gate, err := n.certGate, n.certErr
	n.mu.Unlock()
	if gate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
	}
	return err
}
func (n *fakeNode) Listen() (net.Listener, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.listens++
	if n.failListen {
		n.failListen = false
		return nil, errors.New("injected listener failure")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	n.lastListener = ln
	return ln, err
}
func (n *fakeNode) HTTPClient() *http.Client { return n.client }
func (n *fakeNode) Login(context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.loginCalls++
	n.state.AuthURL = "https://login.tailscale.com/test"
	return nil
}
func (n *fakeNode) Close() error { n.mu.Lock(); defer n.mu.Unlock(); n.closed = true; return nil }
func (n *fakeNode) change(s Status, host string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state, n.host = s, host
	if n.events != nil {
		state := ipn.Running
		select {
		case n.events <- ipn.Notify{State: &state}:
		default:
		}
	}
}

func setup(t *testing.T, n *fakeNode, prepare func(string) error) (Client, context.CancelFunc, <-chan error) {
	t.Helper()
	return setupWithRetry(t, n, prepare, time.Minute)
}
func setupWithRetry(t *testing.T, n *fakeNode, prepare func(string) error, retryDelay time.Duration) (Client, context.CancelFunc, <-chan error) {
	t.Helper()
	dir := t.TempDir()
	identity := Identity{Root: "test-root", Port: 18877, Prefix: "/otata", Hostname: "otata-test"}
	if n.client == nil {
		n.client = &http.Client{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runWithRetry(ctx, Options{Dir: dir, Identity: identity, Handler: http.NotFoundHandler(), Prepare: prepare}, n, retryDelay)
	}()
	t.Cleanup(cancel)
	c := Client{Dir: dir, Identity: identity}
	waitStatus(t, c, func(s Status) bool { return s.State != "starting" })
	return c, cancel, done
}

func waitStatus(t *testing.T, c Client, predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var s Status
	var err error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err = c.Status(ctx)
		cancel()
		if err == nil && predicate(s) {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status: %+v, %v", s, err)
	return s
}
func wake(t *testing.T, c Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Login(ctx); err != nil {
		t.Fatal(err)
	}
}
func stopped(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung")
	}
}

func TestEnrollmentCertificateReadinessAndShutdown(t *testing.T) {
	gate := make(chan struct{})
	n := &fakeNode{state: Status{State: "needs_login"}, certGate: gate}
	prepared := make(chan string, 1)
	c, cancel, done := setup(t, n, func(base string) error { prepared <- base; return nil })
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{c.Dir, 0700}, {ReceiptPath(c.Dir), 0600}} {
		fi, err := os.Stat(entry.path)
		if err != nil || fi.Mode().Perm() != entry.mode {
			t.Fatalf("permissions: %v %v", fi, err)
		}
	}
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return s.AuthURL != "" })
	n.change(Status{State: "needs_approval", Detail: "approval required"}, "")
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return s.State == "needs_approval" && !s.Ready })
	n.change(Status{State: "connected"}, "otata.tailnet.ts.net")
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return s.State == "certificate_pending" && !s.Ready && s.BaseURL == "" })
	select {
	case <-prepared:
		t.Fatal("pages prepared before certificate")
	default:
	}
	close(gate)
	s := waitStatus(t, c, func(s Status) bool { return s.Ready })
	if got := <-prepared; got != s.BaseURL {
		t.Fatalf("prepared %q, ready %q", got, s.BaseURL)
	}
	wake(t, c)
	n.mu.Lock()
	logins := n.loginCalls
	n.mu.Unlock()
	if logins != 1 {
		t.Fatalf("login on enrolled node rotated credentials: %d calls", logins)
	}
	other := &fakeNode{client: &http.Client{}}
	if err := run(context.Background(), Options{Dir: c.Dir, Identity: c.Identity}, other); err == nil || !strings.Contains(err.Error(), "owns") {
		t.Fatalf("duplicate state owner accepted: %v", err)
	}
	// Failed duplicate ownership must not remove the owner's receipt.
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	n.change(Status{State: "connecting"}, "")
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return !s.Ready && s.State == "connecting" })
	stopped(t, cancel, done)
	if _, err := os.Stat(ReceiptPath(c.Dir)); !os.IsNotExist(err) {
		t.Fatalf("control receipt survived shutdown: %v", err)
	}
	n.mu.Lock()
	closed := n.closed
	n.mu.Unlock()
	if !closed {
		t.Fatal("node not closed")
	}
	// The lock is reusable after orderly shutdown.
	ctx, end := context.WithCancel(context.Background())
	end()
	if err := run(ctx, Options{Dir: c.Dir, Identity: c.Identity}, other); err != nil {
		t.Fatal(err)
	}
}

func TestCertificateFailureRetryAndCancellation(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "otata.tailnet.ts.net", certErr: errors.New("certificate denied")}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.State == "error" && strings.Contains(s.Detail, "certificate denied") })
	n.mu.Lock()
	n.certErr = nil
	n.certGate = make(chan struct{})
	n.mu.Unlock()
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return s.State == "certificate_pending" })
	stopped(t, cancel, done)
}

func TestPreparationFailureDoesNotAdvertiseURL(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "otata.tailnet.ts.net"}
	c, cancel, done := setup(t, n, func(string) error { return errors.New("disk full") })
	waitStatus(t, c, func(s Status) bool {
		return s.State == "error" && !s.Ready && s.BaseURL == "" && strings.Contains(s.Detail, "disk full")
	})
	stopped(t, cancel, done)
}

func TestPrivateControlAndScopedProbes(t *testing.T) {
	requests := make(chan *http.Request, 10)
	n := &fakeNode{state: Status{State: "connected"}, host: "otata.tailnet.ts.net", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests <- r
		head := make(http.Header)
		head.Set("X-Otata-Root", "test-root")
		code := 200
		if strings.HasSuffix(r.URL.Path, "redirect") {
			code = 302
			head.Set("Location", "https://other.tailnet.ts.net/")
		}
		if strings.HasSuffix(r.URL.Path, "wrong-root") {
			head.Set("X-Otata-Root", "another-root")
		}
		return &http.Response{StatusCode: code, Header: head, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}
	c, cancel, done := setup(t, n, nil)
	s := waitStatus(t, c, func(s Status) bool { return s.Ready })
	ctx := context.Background()
	data, err := os.ReadFile(ReceiptPath(c.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var rec receipt
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(rec.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated status: %d", resp.StatusCode)
	}
	for _, mutate := range []func(*Identity){func(i *Identity) { i.Root = "other" }, func(i *Identity) { i.Port++ }, func(i *Identity) { i.Prefix = "/other" }, func(i *Identity) { i.Hostname = "other" }} {
		wrong := c
		mutate(&wrong.Identity)
		if _, err := wrong.Status(ctx); err == nil {
			t.Fatal("different config accepted")
		}
	}
	for _, target := range []string{"https://other.tailnet.ts.net/otata/app", "http://otata.tailnet.ts.net/otata/app", s.BaseURL + "-other/app", s.BaseURL + "/../private", s.BaseURL + "/%2e%2e/private", s.BaseURL + "/app?x=1"} {
		if _, err := c.Probe(ctx, target); err == nil {
			t.Fatalf("accepted out-of-scope probe %q", target)
		}
	}
	if len(requests) != 0 {
		t.Fatal("rejected probe reached network")
	}
	if code, err := c.Probe(ctx, s.BaseURL+"/app/payload.ipa"); err != nil || code != 200 {
		t.Fatalf("probe = %d,%v", code, err)
	}
	if req := <-requests; req.Method != "HEAD" {
		t.Fatalf("probe method %s", req.Method)
	}
	if code, err := c.Probe(ctx, s.BaseURL+"/redirect"); err != nil || code != 302 {
		t.Fatalf("redirect = %d,%v", code, err)
	}
	<-requests
	if len(requests) != 0 {
		t.Fatal("followed redirect")
	}
	if _, err := c.Probe(ctx, s.BaseURL+"/wrong-root"); err == nil {
		t.Fatal("accepted wrong root")
	}
	stopped(t, cancel, done)
}

type fakeWatcher struct {
	ctx    context.Context
	cancel context.CancelFunc
	events <-chan ipn.Notify
}

func (w *fakeWatcher) Next() (ipn.Notify, error) {
	select {
	case <-w.ctx.Done():
		return ipn.Notify{}, w.ctx.Err()
	case n, ok := <-w.events:
		if !ok {
			return ipn.Notify{}, io.EOF
		}
		return n, nil
	}
}
func (w *fakeWatcher) Close() error { w.cancel(); return nil }
func (n *fakeNode) Watch(ctx context.Context) (notificationWatcher, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.watches++
	if n.watchFailures > 0 {
		n.watchFailures--
		return nil, errors.New("injected subscribe failure")
	}
	n.events = make(chan ipn.Notify, 1024)
	state := ipn.Running
	n.events <- ipn.Notify{State: &state}
	ctx, cancel := context.WithCancel(ctx)
	return &fakeWatcher{ctx, cancel, n.events}, nil
}
func waitCondition(t *testing.T, f func() bool) {
	t.Helper()
	until := time.Now().Add(4 * time.Second)
	for !f() {
		if time.Now().After(until) {
			t.Fatal("condition timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestNotificationsPromptAndIdle(t *testing.T) {
	n := &fakeNode{state: Status{State: "connecting"}}
	c, cancel, done := setup(t, n, nil)
	waitCondition(t, func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.watches > 0 })
	start := time.Now()
	n.change(Status{State: "connected"}, "first.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	elapsed := time.Since(start)
	t.Logf("transition to ready: %s", elapsed)
	if elapsed > time.Second {
		t.Fatal("slow transition")
	}
	time.Sleep(50 * time.Millisecond)
	n.mu.Lock()
	before := n.snapshots
	n.mu.Unlock()
	time.Sleep(2200 * time.Millisecond)
	n.mu.Lock()
	after := n.snapshots
	n.mu.Unlock()
	if before != after {
		t.Fatalf("idle queried status %d -> %d", before, after)
	}
	n.change(Status{State: "connected", Detail: "health warning"}, "first.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready && s.Detail == "health warning" })
	n.mu.Lock()
	listens := n.listens
	n.mu.Unlock()
	if listens != 1 {
		t.Fatal("health change recreated listener")
	}
	n.change(Status{State: "dns_disabled"}, "")
	waitStatus(t, c, func(s Status) bool { return !s.Ready && s.State == "dns_disabled" })
	n.change(Status{State: "connected"}, "second.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready && strings.Contains(s.BaseURL, "second") })
	stopped(t, cancel, done)
}
func TestNotificationsReconnectAndSnapshotRetry(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "first.ts.net"}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	waitCondition(t, func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.events != nil })
	n.mu.Lock()
	msg := "IPN bus consumer fell behind; closing watch"
	n.events <- ipn.Notify{ErrMessage: &msg}
	close(n.events)
	n.events = nil
	n.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	n.mu.Lock()
	n.state = Status{State: "connected"}
	n.host = "missed.ts.net"
	n.failSnapshot = true
	n.mu.Unlock()
	waitStatus(t, c, func(s Status) bool { return s.Ready && strings.Contains(s.BaseURL, "missed") })
	n.mu.Lock()
	w := n.watches
	n.mu.Unlock()
	if w < 2 {
		t.Fatal("not resubscribed")
	}
	stopped(t, cancel, done)
}
func TestNotificationsEventsDuringCertificate(t *testing.T) {
	gate := make(chan struct{})
	n := &fakeNode{state: Status{State: "connected"}, host: "first.ts.net", certGate: gate}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.State == "certificate_pending" })
	waitCondition(t, func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.events != nil })
	for i := 0; i < 500; i++ {
		n.change(Status{State: "connected"}, "final.ts.net")
	}
	close(gate)
	waitStatus(t, c, func(s Status) bool { return s.Ready && strings.Contains(s.BaseURL, "final") })
	n.mu.Lock()
	w := n.watches
	n.mu.Unlock()
	if w != 1 {
		t.Fatalf("reader fell behind: %d watches", w)
	}
	stopped(t, cancel, done)
}
func TestNotificationsCertificateRetryBackoff(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "first.ts.net", certErr: errors.New("temporary cert failure")}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.State == "error" })
	for i := 0; i < 30; i++ {
		n.change(Status{State: "connected", Detail: "unrelated health event"}, "first.ts.net")
	}
	time.Sleep(100 * time.Millisecond)
	n.mu.Lock()
	calls := n.certs
	n.certErr = nil
	n.mu.Unlock()
	if calls != 1 {
		t.Fatalf("events bypass retry backoff: %d", calls)
	}
	wake(t, c)
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	stopped(t, cancel, done)
}

func TestNotificationsAutomaticRetry(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "retry.ts.net", certErr: errors.New("temporary failure")}
	c, cancel, done := setupWithRetry(t, n, nil, 120*time.Millisecond)
	waitStatus(t, c, func(s Status) bool { return s.State == "error" })
	n.mu.Lock()
	n.certErr = nil
	n.mu.Unlock()
	// No backend event or manual wake: the failure timer must retry by itself.
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	n.mu.Lock()
	calls := n.certs
	n.mu.Unlock()
	if calls != 2 {
		t.Fatalf("cert attempts=%d", calls)
	}
	stopped(t, cancel, done)
}

func TestNotificationsListenerLifecycle(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "first.ts.net"}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	n.mu.Lock()
	first := n.lastListener.Addr().String()
	n.mu.Unlock()
	checkClosed := func(addr string) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("listener still open: %s", addr)
		}
	}
	resp, err := http.Get("http://" + first)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	n.change(Status{State: "connected"}, "renamed.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready && strings.Contains(s.BaseURL, "renamed") })
	checkClosed(first)
	n.mu.Lock()
	second := n.lastListener.Addr().String()
	n.mu.Unlock()
	n.change(Status{State: "https_disabled"}, "")
	waitStatus(t, c, func(s Status) bool { return s.State == "https_disabled" })
	checkClosed(second)
	stopped(t, cancel, done)
}
func TestNotificationsListenerRetryAndCancelReconnect(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "retry.ts.net", failListen: true}
	c, cancel, done := setupWithRetry(t, n, nil, 120*time.Millisecond)
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	n.mu.Lock()
	calls := n.listens
	msg := "forced disconnect"
	n.events <- ipn.Notify{ErrMessage: &msg}
	close(n.events)
	n.events = nil
	n.mu.Unlock()
	if calls != 2 {
		t.Fatalf("listen attempts=%d", calls)
	}
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	stopped(t, cancel, done)
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("shutdown waited for reconnect delay")
	}
}

// The callback is the boundary at which app starts serving local identity.
// A caller observing that identity must already be able to query startup,
// even when tsnet.Start is slow or eventually fails.
func TestControlReadyBeforeNodeInitialization(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			gate := make(chan struct{})
			n := &fakeNode{startGate: gate, client: &http.Client{}, state: Status{State: "connected"}, host: "startup.ts.net"}
			if fail {
				n.startErr = errors.New("injected initialization failure")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			identity := Identity{Root: "startup", Port: 18877, Prefix: "/otata", Hostname: "startup"}
			c := Client{Dir: t.TempDir(), Identity: identity}
			ready := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- run(ctx, Options{Dir: c.Dir, Identity: identity, Handler: http.NotFoundHandler(), ControlReady: func() { close(ready) }}, n)
			}()
			t.Cleanup(func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
			})
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("control not published before Start")
			}
			s, err := c.Status(ctx)
			if err != nil || s.State != "starting" {
				t.Fatalf("status during Start: %+v %v", s, err)
			}
			s, err = c.Login(ctx)
			if err != nil || s.State != "starting" {
				t.Fatalf("login during Start: %+v %v", s, err)
			}
			n.mu.Lock()
			snapshots, logins := n.snapshots, n.loginCalls
			n.mu.Unlock()
			if snapshots != 0 || logins != 0 {
				t.Fatal("control accessed node before initialization finished")
			}
			close(gate)
			if fail {
				select {
				case err := <-done:
					if err == nil || !strings.Contains(err.Error(), "initialization failure") {
						t.Fatalf("start error: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("failed startup hung")
				}
				if _, err := os.Stat(ReceiptPath(c.Dir)); !os.IsNotExist(err) {
					t.Fatalf("failed startup left receipt: %v", err)
				}
			} else {
				waitStatus(t, c, func(s Status) bool { return s.Ready })
				stopped(t, cancel, done)
			}
		})
	}
}

func TestNotificationsAvailabilityDuringBackoff(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "retry.ts.net", certErr: errors.New("certificate unavailable")}
	c, cancel, done := setup(t, n, nil)
	waitStatus(t, c, func(s Status) bool { return s.State == "error" })
	n.change(Status{State: "dns_disabled"}, "")
	waitStatus(t, c, func(s Status) bool { return s.State == "dns_disabled" })
	n.mu.Lock()
	n.certErr = nil
	n.mu.Unlock()
	// Re-enabling DNS must clear the old preparation backoff, even if the
	// hostname is unchanged, instead of retaining a stale dns_disabled status.
	n.change(Status{State: "connected"}, "retry.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	stopped(t, cancel, done)
}

func TestNotificationsSubscriptionFailure(t *testing.T) {
	n := &fakeNode{state: Status{State: "connecting"}, watchFailures: 1}
	c, cancel, done := setup(t, n, nil)
	waitCondition(t, func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.events != nil })
	n.change(Status{State: "connected"}, "recovered.ts.net")
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	n.mu.Lock()
	attempts := n.watches
	n.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("subscription attempts=%d", attempts)
	}
	stopped(t, cancel, done)
}

// Each input can change independently of the backend's Running state. In
// particular, DNS/certificate settings and health must not rely on State events.
func TestNotificationsWithoutStateTransitions(t *testing.T) {
	n := &fakeNode{state: Status{State: "connected"}, host: "events.ts.net"}
	c, cancel, done := setup(t, n, nil)
	defer stopped(t, cancel, done)
	waitStatus(t, c, func(s Status) bool { return s.Ready })
	waitCondition(t, func() bool { n.mu.Lock(); defer n.mu.Unlock(); return n.events != nil })
	message := "notification"
	for _, tc := range []struct {
		name string
		ev   ipn.Notify
	}{
		{"self", ipn.Notify{SelfChange: &tailcfg.Node{}}},
		{"health", ipn.Notify{Health: &health.State{}}},
		{"auth URL", ipn.Notify{BrowseToURL: &message}},
		{"login finished", ipn.Notify{LoginFinished: &empty.Message{}}},
		{"error", ipn.Notify{ErrMessage: &message}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.mu.Lock()
			n.state.Detail = tc.name
			n.events <- tc.ev
			n.mu.Unlock()
			waitStatus(t, c, func(s Status) bool { return s.Ready && s.Detail == tc.name })
		})
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.listens != 1 || n.certs != 1 {
		t.Fatalf("notifications repeated HTTPS preparation: listens=%d certs=%d", n.listens, n.certs)
	}
}
