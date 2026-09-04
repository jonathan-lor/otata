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
	// Version is this binary's; ServerVersion is what the running server
	// reported. They differ when an upgrade replaced the binary on disk and
	// the long-lived server still runs the old image, a state nothing else
	// surfaces. ServerOutdated flags it so an agent doesn't need to compare strings.
	Version        string `json:"version"`
	ServerVersion  string `json:"server_version,omitempty"`
	ServerOutdated bool   `json:"server_outdated,omitempty"`
	ServerRunning  bool   `json:"server_running"`
	// ServerOtherRoot is set when an otata server holds the port but serves a
	// different store, so "down" isn't read as "nothing there".
	ServerOtherRoot bool `json:"server_other_root,omitempty"`
	// Autostart is whether the server returns at login: the agent is installed.
	// AutostartLoaded is whether launchd currently manages it: false after
	// `otata stop`, which boots the job out but leaves it installed. One field
	// answering the second question while labeled as the first made status say
	// "off" about a server that would come back at next login.
	Autostart       bool `json:"autostart"`
	AutostartLoaded bool `json:"autostart_loaded"`
	// AutostartDisabled is set when the user has switched the agent off at the
	// launchd level: the System Settings Login Items toggle, or `launchctl
	// disable`. The plist alone says autostart is on, while macOS will load
	// nothing until it is re-enabled, so the two cannot be conflated.
	AutostartDisabled bool `json:"autostart_disabled,omitempty"`
	// AutostartOtherRoot is set when the user's one launch agent serves a
	// different root or port than this invocation, so "off" is not read as
	// "nothing installed", and so it is clear why `autostart on` refuses.
	AutostartOtherRoot string                       `json:"autostart_other_root,omitempty"`
	AutostartProgram   string                       `json:"autostart_program,omitempty"`
	AutostartStale     bool                         `json:"autostart_stale"`
	Transport          transport.Status             `json:"transport"`
	Apps               []artifact.Record            `json:"apps"`
	Building           map[string]artifact.Building `json:"building,omitempty"`
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
	if r.AutostartOtherRoot != "" {
		cli.Line(w, "           the launch agent serves %s, not this root", r.AutostartOtherRoot)
	}
	// Disabled trumps not-loaded. Reloading a disabled agent cannot work, so
	// the not-loaded line's advice would prescribe a dead end.
	switch {
	case r.Autostart && r.AutostartDisabled:
		cli.Line(w, "           \033[1;33mthe launch agent is disabled\033[0m; enable otata in System Settings > General > Login Items")
	case r.Autostart && !r.AutostartLoaded:
		cli.Line(w, "           \033[1;33mthe launch agent is installed but not loaded\033[0m; 'otata start' reloads it")
	}
	if r.AutostartStale {
		cli.Line(w, "           \033[1;33mthe launch agent runs a stale copy\033[0m; re-run 'otata autostart on'")
	}
	// "not ready", not "not wired": the detail line below says which, and a
	// logged-out tailscale is not a wiring problem.
	cli.Line(w, "transport: %s (%s) %s", r.Transport.Name, r.Transport.Visibility,
		boolWord(r.Transport.Ready, "ready", "not ready"))
	if r.Transport.BaseURL != "" {
		cli.Line(w, "url:       %s/", strings.TrimSuffix(r.Transport.BaseURL, "/"))
	}
	if r.Transport.Detail != "" {
		cli.Line(w, "detail:    %s", r.Transport.Detail)
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
	// One probe answers running, other-root and version at once.
	p, ok := a.probeServer("/")
	res.ServerRunning = ok && p.Root == a.RootDigest()
	if res.ServerRunning {
		res.ServerVersion = p.Version
		res.ServerOutdated = p.Version != res.Version
	} else {
		res.ServerOtherRoot = ok
	}
	if res.Autostart {
		res.AutostartLoaded = a.agentLoaded()
		res.AutostartProgram, res.AutostartStale = a.AutostartProgram()
		res.AutostartDisabled = agentDisabled()
	} else if spec, ok := readAgentPlist(); ok {
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
