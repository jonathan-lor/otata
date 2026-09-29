// Package tsnetnode owns the embedded network in the serving process.
// CLI commands use a private loopback API and never open the node's state.
package tsnetnode

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type Identity struct {
	Root     string `json:"root"`
	Port     int    `json:"port"`
	Prefix   string `json:"prefix"`
	Hostname string `json:"hostname"`
}

type Status struct {
	State   string `json:"state"`
	Ready   bool   `json:"ready"`
	BaseURL string `json:"base_url,omitempty"`
	AuthURL string `json:"auth_url,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type receipt struct {
	Version  int      `json:"version"`
	Identity Identity `json:"identity"`
	URL      string   `json:"url"`
	Token    string   `json:"token"`
}

func ReceiptPath(dir string) string { return filepath.Join(dir, "control.json") }

type Client struct {
	Dir      string
	Identity Identity
}

func (c Client) call(ctx context.Context, method, path string, input, output any) error {
	data, err := os.ReadFile(ReceiptPath(c.Dir))
	if err != nil {
		return fmt.Errorf("tsnet server unavailable; start 'otata serve' or 'otata start': %w", err)
	}
	var rec receipt
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("invalid tsnet control receipt: %w", err)
	}
	if rec.Version != 1 || rec.Identity != c.Identity {
		return fmt.Errorf("tsnet server configuration differs; restart 'otata serve' or run 'otata restart'")
	}
	u, err := url.Parse(rec.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || rec.Token == "" {
		return fmt.Errorf("invalid tsnet control address")
	}
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, rec.URL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+rec.Token)
	req.Header.Set("Content-Type", "application/json")
	// Do not send the control token to an environment-configured HTTP proxy.
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("tsnet server unavailable; restart it: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("tsnet control: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(output)
}

func (c Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.call(ctx, http.MethodGet, "/status", nil, &s)
	return s, err
}

func (c Client) Login(ctx context.Context) (Status, error) {
	var s Status
	err := c.call(ctx, http.MethodPost, "/login", nil, &s)
	return s, err
}

type probeRequest struct {
	URL string `json:"url"`
}
type probeResult struct {
	Code int    `json:"code"`
	Root string `json:"root"`
}

func (c Client) Probe(ctx context.Context, target string) (int, error) {
	var result probeResult
	if err := c.call(ctx, http.MethodPost, "/probe", probeRequest{target}, &result); err != nil {
		return 0, err
	}
	if result.Root != c.Identity.Root {
		return 0, fmt.Errorf("HTTPS endpoint served a different otata root")
	}
	return result.Code, nil
}

// A probe can only HEAD this node's own served path. It cannot act as a
// general-purpose tailnet proxy, and redirects are never followed.
func validProbe(base, target string) bool {
	b, e1 := url.Parse(base)
	u, e2 := url.Parse(target)
	return e1 == nil && e2 == nil && b.Scheme == "https" && u.Scheme == b.Scheme && u.Host == b.Host &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(path.Clean(u.Path)+"/", strings.TrimSuffix(b.Path, "/")+"/")
}

func (m *manager) control(token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(m.status()) })
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		// Start may still be initializing the LocalAPI client. Do not call
		// into tsnet concurrently with Start or its OmitAuth configuration.
		select {
		case <-m.started:
		default:
			json.NewEncoder(w).Encode(m.status())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		// Read the backend, not the supervisor cache: a completed browser login
		// may have enrolled the node since the last status update.
		s, _, err := m.node.Snapshot(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// Re-running login on an enrolled node must not rotate its identity.
		if s.State == "needs_login" {
			if err := m.node.Login(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		select {
		case m.wake <- struct{}{}:
		default:
		}
		json.NewEncoder(w).Encode(m.status())
	})
	mux.HandleFunc("POST /probe", func(w http.ResponseWriter, r *http.Request) {
		var input probeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
			http.Error(w, "invalid probe", 400)
			return
		}
		s := m.status()
		if !s.Ready {
			http.Error(w, s.Detail, 503)
			return
		}
		if !validProbe(s.BaseURL, input.URL) {
			http.Error(w, "probe must target this node's served path", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, input.URL, nil)
		if err != nil {
			http.Error(w, "invalid probe URL", 400)
			return
		}
		resp, err := m.client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		json.NewEncoder(w).Encode(probeResult{resp.StatusCode, resp.Header.Get("X-Otata-Root")})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
	})
}
