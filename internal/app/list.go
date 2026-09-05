package app

import (
	"io"
	"strings"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/render"
)

type ListResult struct {
	Apps     []artifact.Record            `json:"apps"`
	Building map[string]artifact.Building `json:"building,omitempty"`
	IndexURL string                       `json:"index_url,omitempty"`
	// ServerRunning is whether the URL above answers right now. The list is
	// read from disk and is correct either way (`stop` unpublishes nothing),
	// but a URL printed for tapping must not look live when it is not.
	ServerRunning bool `json:"server_running"`
}

// listColumns is the single format the header and every row go through, so a
// column cannot be labeled at one width and printed at another. The size is
// rendered to a string first for the same reason: "%6.1f MB" and a heading for
// it would be two formats to keep in agreement.
const listColumns = "%-18s %-20s %-9s %-8s %-11s %9s  %-16s %s"

func (r ListResult) Human(w io.Writer) {
	cli.Section(w, "Published")
	if len(r.Apps) == 0 {
		cli.Line(w, "nothing published yet; run 'otata publish' inside a project")
	} else {
		// Dim: a header is orientation, not content, findable when wanted,
		// and ignorable once the columns are known.
		cli.Line(w, "\033[2m"+listColumns+"\033[0m",
			"SLUG", "APP", "VERSION", "CONFIG", "SIGNER", "SIZE", "COMMIT", "BUILT")
	}
	now := time.Now()
	for _, a := range r.Apps {
		commit := a.Commit
		if a.Dirty {
			commit += " +dirty"
		}
		state := render.Age(a.BuiltAt, now)
		if _, ok := r.Building[a.Slug]; ok {
			state = "BUILDING"
		}
		cli.Line(w, listColumns,
			a.Slug, a.Title, a.Version+" ("+a.Build+")", a.Config, orDash(a.SignedBy()),
			render.Size(a.SizeMB()), orDash(commit), state)
	}
	switch {
	case r.IndexURL != "" && r.ServerRunning:
		cli.Line(w, "\n\033[1;32m%s\033[0m\n", r.IndexURL)
	case r.IndexURL != "":
		cli.Line(w, "\n\033[1;33mserver is not running\033[0m; 'otata start' brings %s back\n", r.IndexURL)
	}
}

// orDash distinguishes an absent value from a blank one. A payload with no
// readable signature has no signer recorded, which is not "signed by nobody".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *App) List() (*ListResult, error) {
	records, err := a.Store.Records()
	if err != nil {
		return nil, cli.Failf(cli.CodeInternal, "%v", err)
	}
	building, _ := a.Store.Building()
	res := &ListResult{Apps: records, Building: building, ServerRunning: a.ServerRunning()}
	if tr, err := a.Transport(); err == nil {
		if st := tr.Status(a.Config.Port); st.BaseURL != "" {
			res.IndexURL = strings.TrimSuffix(st.BaseURL, "/") + "/"
		}
	}
	return res, nil
}
