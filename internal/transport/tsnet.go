package transport

import (
	"context"
	"time"

	"github.com/jonathan-lor/otata/internal/tsnetnode"
)

// TSNet is only a client of the node owned by otata serve.
type TSNet struct{ Client tsnetnode.Client }

func NewTSNet(dir string, identity tsnetnode.Identity) *TSNet {
	return &TSNet{Client: tsnetnode.Client{Dir: dir, Identity: identity}}
}
func (*TSNet) Name() string           { return "tailscale" }
func (*TSNet) Visibility() Visibility { return Private }
func (*TSNet) IncomingPrefix() string { return "" }

func (t *TSNet) Status(port int) Status {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := t.Client
	c.Identity.Port = port
	s, err := c.Status(ctx)
	if err != nil {
		return Status{Name: "tailscale", Visibility: Private, State: "stopped", Detail: err.Error()}
	}
	return Status{Name: "tailscale", Visibility: Private, Ready: s.Ready, BaseURL: s.BaseURL, State: s.State, AuthURL: s.AuthURL, Detail: s.Detail}
}

func (t *TSNet) Ensure(port int) (string, error) {
	// Enrollment needs human input. Never hang publish on it. Ordinary node
	// startup and certificate issuance get a bounded opportunity to finish.
	deadline := time.Now().Add(2 * time.Minute)
	for {
		s := t.Status(port)
		if s.Ready {
			return s.BaseURL, nil
		}
		if (s.State != "starting" && s.State != "connecting" && s.State != "certificate_pending") || time.Now().After(deadline) {
			detail := s.Detail
			if s.AuthURL != "" {
				detail += "; login: " + s.AuthURL
			}
			return "", &ErrUnavailable{Name: "tailscale", Reason: detail}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Probe uses the running node's network, so doctor works without host Tailscale.
func (t *TSNet) Probe(ctx context.Context, target string) (int, error) {
	return t.Client.Probe(ctx, target)
}

func (t *TSNet) Login(ctx context.Context) error {
	_, err := t.Client.Login(ctx)
	return err
}

var _ Transport = (*TSNet)(nil)
