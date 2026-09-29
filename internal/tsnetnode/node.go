package tsnetnode

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonathan-lor/otata/internal/atomicfile"
	"tailscale.com/tsnet"
)

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func ValidateHostname(name string) error {
	if !hostnamePattern.MatchString(name) {
		return fmt.Errorf("tsnet hostname must be a lowercase DNS label (1–63 letters, digits or internal hyphens)")
	}
	return nil
}

// node permits lifecycle tests without registering devices or issuing certs.
type node interface {
	Start() error
	Watch(context.Context) (notificationWatcher, error)
	Snapshot(context.Context) (Status, string, error)
	Certificate(context.Context, string) error
	Listen() (net.Listener, error)
	HTTPClient() *http.Client
	Login(context.Context) error
	Close() error
}

type embedded struct{ tsnet.Server }

// Start makes the LocalAPI client skip the host daemon's auth token. tsnet's
// LocalAPI is in-process and ignores it, and on macOS each lookup runs lsof.
func (n *embedded) Start() error {
	if err := n.Server.Start(); err != nil {
		return err
	}
	lc, err := n.LocalClient()
	if err != nil {
		return err
	}
	lc.OmitAuth = true
	return nil
}

func (n *embedded) Snapshot(ctx context.Context) (Status, string, error) {
	lc, err := n.LocalClient()
	if err != nil {
		return Status{}, "", err
	}
	s, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		return Status{}, "", err
	}
	st := Status{AuthURL: s.AuthURL}
	switch s.BackendState {
	case "NeedsLogin":
		st.State, st.Detail = "needs_login", "enroll this node with 'otata transport login'"
	case "NeedsMachineAuth":
		st.State, st.Detail = "needs_approval", "approve this node in the Tailscale admin console"
	case "Running":
		if s.CurrentTailnet == nil || !s.CurrentTailnet.MagicDNSEnabled {
			st.State, st.Detail = "dns_disabled", "enable MagicDNS in the tailnet DNS settings"
			break
		}
		if s.Self == nil || s.Self.DNSName == "" {
			st.State, st.Detail = "connecting", "waiting for this node's DNS name"
			break
		}
		if len(s.CertDomains) == 0 {
			st.State, st.Detail = "https_disabled", "enable HTTPS certificates in the tailnet DNS settings"
			break
		}
		st.State, st.Detail = "connected", strings.Join(s.Health, "; ")
		return st, strings.TrimSuffix(s.Self.DNSName, "."), nil
	default:
		st.State, st.Detail = "connecting", "Tailscale state: "+s.BackendState
	}
	return st, "", nil
}

func (n *embedded) Certificate(ctx context.Context, host string) error {
	lc, err := n.LocalClient()
	if err != nil {
		return err
	}
	_, _, err = lc.CertPair(ctx, host)
	return err
}

func (n *embedded) Listen() (net.Listener, error) {
	lc, err := n.LocalClient()
	if err != nil {
		return nil, err
	}
	ln, err := n.Server.Listen("tcp", ":443")
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: lc.GetCertificate}), nil
}

func (n *embedded) Login(ctx context.Context) error {
	lc, err := n.LocalClient()
	if err != nil {
		return err
	}
	return lc.StartLoginInteractive(ctx)
}

type Options struct {
	Dir      string
	Identity Identity
	Handler  http.Handler
	// Prepare rewrites install pages before the new HTTPS URL is exposed.
	Prepare func(string) error
	// ControlReady runs once the control API can report startup status.
	ControlReady func()
	Logf         func(string, ...any)
}

type manager struct {
	node    node
	client  *http.Client
	mu      sync.Mutex
	current Status
	wake    chan struct{}
	started chan struct{}
}

func (m *manager) status() Status { m.mu.Lock(); defer m.mu.Unlock(); return m.current }
func (m *manager) set(s Status)   { m.mu.Lock(); m.current = s; m.mu.Unlock() }

// Run holds the state lock until both the node and its control API are closed.
// Credentials survive shutdown; the ephemeral control token does not.
func Run(ctx context.Context, opts Options) error {
	if err := ValidateHostname(opts.Identity.Hostname); err != nil {
		return err
	}
	n := &embedded{tsnet.Server{Dir: opts.Dir, Hostname: opts.Identity.Hostname, UserLogf: opts.Logf}}
	return run(ctx, opts, n)
}

func run(ctx context.Context, opts Options, n node) error {
	return runWithRetry(ctx, opts, n, time.Minute)
}

func runWithRetry(ctx context.Context, opts Options, n node, retryDelay time.Duration) error {
	if err := os.MkdirAll(opts.Dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(opts.Dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(opts.Dir, "node.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another otata server owns this tsnet state: %w", err)
	}
	if err := os.Remove(ReceiptPath(opts.Dir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	defer os.Remove(ReceiptPath(opts.Dir))
	nodeStarted := false
	defer func() {
		if nodeStarted {
			n.Close()
		}
	}()
	m := &manager{node: n, current: Status{State: "starting", Detail: "connecting embedded Tailscale node"}, wake: make(chan struct{}, 1), started: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	token := rand.Text()
	control := &http.Server{Handler: m.control(token), ReadHeaderTimeout: 5 * time.Second}
	defer control.Close()
	errCh := make(chan error, 2)
	go func() { errCh <- control.Serve(ln) }()
	data, _ := json.Marshal(receipt{1, opts.Identity, "http://" + ln.Addr().String(), token})
	if err := atomicfile.WriteData(opts.Dir, ReceiptPath(opts.Dir), 0600, data); err != nil {
		return err
	}
	if opts.ControlReady != nil {
		opts.ControlReady()
	}
	// Publish the control receipt before starting the network. Local identity
	// serving begins at ControlReady, so a successful process probe always has
	// a corresponding control endpoint, even during slow initialization.
	nodeStarted = true
	if err := n.Start(); err != nil {
		return fmt.Errorf("start tsnet: %w", err)
	}
	client := n.HTTPClient()
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	m.client = client
	close(m.started)

	changed := make(chan struct{}, 1)
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go watchNode(watchCtx, n, changed, watchDone, opts.Logf)
	defer func() { stopWatch(); <-watchDone }()

	var tail *http.Server
	defer func() {
		if tail != nil {
			tail.Close()
		}
	}()
	var activeURL string
	var retryBase string
	var retryAt time.Time
	var retryStatus Status
	delay := time.Duration(0)
	timer := time.NewTimer(0)
	timer.Stop()
	defer timer.Stop()
	for {
		var tick <-chan time.Time
		if delay >= 0 {
			timer.Reset(delay)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case err := <-errCh:
			timer.Stop()
			return fmt.Errorf("tsnet listener stopped: %w", err)
		case <-m.wake:
			timer.Stop()
			retryAt = time.Time{}
		case <-changed:
			timer.Stop()
		case <-tick:
		}
		delay = -1 // Healthy supervision waits for notifications, not a timer.
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		st, host, err := n.Snapshot(queryCtx)
		cancel()
		if err != nil {
			m.set(Status{State: "connecting", Detail: err.Error()})
			delay = time.Second
			continue
		}
		if host == "" {
			retryBase, retryAt = "", time.Time{}
			if tail != nil {
				tail.Close()
				tail = nil
			}
			m.set(st)
			continue
		}
		base := "https://" + host + opts.Identity.Prefix
		// Health events must not bypass preparation backoff. A new hostname,
		// restored availability, or explicit login/retry can start fresh.
		if base == retryBase && time.Now().Before(retryAt) {
			m.set(retryStatus)
			delay = time.Until(retryAt)
			continue
		}
		if tail != nil && activeURL == base {
			st.State, st.Ready, st.BaseURL = "ready", true, base
			m.set(st)
			continue
		}
		if tail != nil {
			tail.Close()
			tail = nil
		}
		m.set(Status{State: "certificate_pending", Detail: "preparing the HTTPS certificate; first issuance can take a minute"})
		certCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = n.Certificate(certCtx, host)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err == nil && opts.Prepare != nil {
			err = opts.Prepare(base)
		}
		if err != nil {
			retryStatus = Status{State: "error", Detail: fmt.Sprintf("prepare HTTPS: %v; retrying automatically, or run 'otata transport login' to retry now", err)}
			m.set(retryStatus)
			delay = retryDelay
			retryBase, retryAt = base, time.Now().Add(delay)
			continue
		}
		listener, err := n.Listen()
		if err != nil {
			retryStatus = Status{State: "error", Detail: err.Error()}
			m.set(retryStatus)
			delay = retryDelay
			retryBase, retryAt = base, time.Now().Add(delay)
			continue
		}
		tail = &http.Server{Handler: opts.Handler, ReadHeaderTimeout: 10 * time.Second}
		activeURL = base
		retryBase, retryAt = "", time.Time{}
		// Closing an old listener during a DNS rename is expected.
		go func(s *http.Server) {
			if err := s.Serve(listener); err != http.ErrServerClosed {
				errCh <- err
			}
		}(tail)
		st.State, st.Ready, st.BaseURL = "ready", true, base
		m.set(st)
		if opts.Logf != nil {
			opts.Logf("tsnet ready: %s/", base)
		}
	}
}
