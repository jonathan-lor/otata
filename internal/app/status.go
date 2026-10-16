package app

import (
	"io"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/transport"
	"github.com/jonathan-lor/otata/internal/version"
)

type StatusResult struct {
	Root string `json:"root"`
	Port int    `json:"port"`
	// ServerVersion differs from Version when an upgrade replaced the binary
	// under a running server, which ServerOutdated flags.
	Version        string `json:"version"`
	ServerVersion  string `json:"server_version,omitempty"`
	ServerOutdated bool   `json:"server_outdated,omitempty"`
	ServerRunning  bool   `json:"server_running"`
	// ServerOtherRoot means an otata server for another root holds the port.
	ServerOtherRoot bool `json:"server_other_root,omitempty"`
	// Autostart means the unit is installed. AutostartLoaded means the manager
	// is running it, which `otata stop` turns off.
	Autostart       bool `json:"autostart"`
	AutostartLoaded bool `json:"autostart_loaded"`
	// AutostartDisabled means the user switched the installed unit off (Login
	// Items, launchctl disable, systemctl disable or mask).
	AutostartDisabled bool `json:"autostart_disabled,omitempty"`
	// AutostartOtherRoot names what the user's one unit serves when it isn't this root.
	AutostartOtherRoot string                       `json:"autostart_other_root,omitempty"`
	AutostartProgram   string                       `json:"autostart_program,omitempty"`
	AutostartStale     bool                         `json:"autostart_stale,omitempty"`
	Transport          transport.Status             `json:"transport"`
	Apps               []artifact.Record            `json:"apps"`
	Building           map[string]artifact.Building `json:"building,omitempty"`

	// For the human output.
	kind, disabledHint string
}

func (r StatusResult) Human(w io.Writer) {
	cli.Section(w, "Status")
	cli.Line(w, "root:      %s", r.Root)
	cli.Line(w, "server:    %s on 127.0.0.1:%d (autostart %s)",
		boolWord(r.ServerRunning, "running", "down"), r.Port, boolWord(r.Autostart, "on", "off"))
	if r.ServerOutdated {
		v := r.ServerVersion
		if v == "" {
			v = "an unreported version"
		}
		cli.Line(w, "           \033[1;33mthe server is running %s and this binary is %s\033[0m; 'otata restart' picks up the new one", v, r.Version)
	}
	if r.ServerOtherRoot {
		cli.Line(w, "           \033[1;33man otata server that is not this root's holds the port\033[0m; 'otata restart' replaces it")
	}
	kind := r.kind
	if kind == "" {
		kind = "autostart service"
	}
	if r.AutostartOtherRoot != "" {
		cli.Line(w, "           the %s serves %s, not this root", kind, r.AutostartOtherRoot)
	}
	// A disabled unit won't reload, so don't suggest 'otata start' for it.
	switch {
	case r.Autostart && r.AutostartDisabled:
		cli.Line(w, "           \033[1;33mthe %s is disabled\033[0m; %s", kind, r.disabledHint)
	case r.Autostart && !r.AutostartLoaded:
		cli.Line(w, "           \033[1;33mthe %s is installed but not loaded\033[0m; 'otata start' reloads it", kind)
	}
	if r.AutostartStale {
		cli.Line(w, "           \033[1;33mthe %s runs a stale copy\033[0m; re-run 'otata autostart on'", kind)
	}
	cli.Line(w, "transport: %s (%s) %s", r.Transport.Name, r.Transport.Visibility,
		boolWord(r.Transport.Ready, "ready", "not ready"))
	if r.Transport.BaseURL != "" {
		cli.Line(w, "url:       %s/", strings.TrimSuffix(r.Transport.BaseURL, "/"))
	}
	if r.Transport.Detail != "" {
		cli.Line(w, "detail:    %s", r.Transport.Detail)
	}
	if r.Transport.AuthURL != "" {
		cli.Line(w, "login:     %s", r.Transport.AuthURL)
	}
	cli.Line(w, "apps:      %d published, %d building", len(r.Apps), len(r.Building))
}

func boolWord(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// Status never mutates.
func (a *App) Status() (*StatusResult, error) {
	res := &StatusResult{Root: a.Root, Port: a.Config.Port,
		Version: version.String(), Autostart: a.AutostartEnabled()}
	p, ok := a.probeServer("/")
	res.ServerRunning = ok && p.Root == a.RootDigest()
	if res.ServerRunning {
		res.ServerVersion = p.Version
		res.ServerOutdated = p.Version != res.Version
	} else {
		res.ServerOtherRoot = ok
	}
	sup := a.autostart()
	res.kind, res.disabledHint = sup.Kind(), sup.DisabledHint()
	if res.Autostart {
		res.AutostartLoaded = a.agentLoaded()
		res.AutostartProgram, res.AutostartStale = a.AutostartProgram()
		res.AutostartDisabled = sup.Disabled()
	} else if spec, ok := sup.Installed(); ok {
		res.AutostartOtherRoot = describeAgent(spec)
	}
	if tr, err := a.Transport(); err == nil {
		res.Transport = tr.Status(a.Config.Port)
	} else {
		res.Transport = transport.Status{Name: "none", Detail: failureDetail(err)}
	}
	if res.Apps, _ = a.Store.Records(); res.Apps == nil {
		res.Apps = []artifact.Record{} // unreadable state dir: still [] not null
	}
	res.Building, _ = a.Store.Building()
	return res, nil
}
