// Package config holds the little that cannot be discovered.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jonathan-lor/otata/internal/atomicfile"
)

const (
	DefaultPort      = 8787
	DefaultServePath = "/otata"
)

type Manual struct {
	BaseURL string `json:"base_url"`
	// KeepPrefix says the proxy forwards the base URL's path unchanged instead
	// of stripping it, so the server has to strip it. False is the common
	// case for external proxies.
	KeepPrefix bool `json:"keep_prefix,omitempty"`
}

type Config struct {
	Port      int     `json:"port"`
	ServePath string  `json:"serve_path"`
	Transport string  `json:"transport,omitempty"`
	Manual    *Manual `json:"manual,omitempty"`
	TSNet     *TSNet  `json:"tsnet,omitempty"`
}

type TSNet struct {
	Hostname string `json:"hostname"`
}

func (c Config) TSNetHostname() string {
	if c.TSNet != nil && c.TSNet.Hostname != "" {
		return c.TSNet.Hostname
	}
	return "otata"
}

func Default() Config {
	return Config{Port: DefaultPort, ServePath: DefaultServePath, Transport: "tailscale", TSNet: &TSNet{Hostname: "otata"}}
}

// Released host-backed configurations have no embedded node settings.
func (c Config) NeedsTailscaleMigration() bool {
	return c.Transport == "tailscale" && c.TSNet == nil
}

func Path(root string) string { return filepath.Join(root, "config.json") }

// LoadFile reads only what is on disk. Writing config back uses this, so a
// one-off OTATA_PORT for a single command is never persisted.
func LoadFile(root string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(Path(root))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.Transport, c.TSNet = "", nil
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	// Preserve old explicit selections for migration; unconfigured stores and
	// the unreleased tsnet spelling use the embedded default.
	if c.Transport == "" || c.Transport == "tsnet" {
		c.Transport = "tailscale"
		if c.TSNet == nil {
			c.TSNet = Default().TSNet
		}
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ServePath == "" {
		c.ServePath = DefaultServePath
	}
	return c, nil
}

// Load is the on-disk config with environment overrides applied. These are
// documented, and an error hint tells the user to set OTATA_PORT, so they have
// to do something.
func Load(root string) (Config, error) {
	c, err := LoadFile(root)
	if err != nil {
		return c, err
	}
	if v := os.Getenv("OTATA_PORT"); v != "" {
		port, convErr := strconv.Atoi(v)
		if convErr != nil || port < 1 || port > 65535 {
			return c, fmt.Errorf("OTATA_PORT=%q is not a valid port", v)
		}
		c.Port = port
	}
	if v := os.Getenv("OTATA_PATH"); v != "" {
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		c.ServePath = strings.TrimSuffix(v, "/")
	}
	return c, nil
}

// Save writes atomically. A torn config makes every command fail, including
// doctor --fix, so a partial write here is unrecoverable without hand-editing.
func Save(root string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteData(root, Path(root), 0o600, data)
}
