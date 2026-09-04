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
	// case and what Tailscale does.
	KeepPrefix bool `json:"keep_prefix,omitempty"`
}

type Config struct {
	Port      int     `json:"port"`
	ServePath string  `json:"serve_path"`
	Transport string  `json:"transport,omitempty"` // empty means auto-detect
	Manual    *Manual `json:"manual,omitempty"`
}

func Default() Config {
	return Config{Port: DefaultPort, ServePath: DefaultServePath}
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
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
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
