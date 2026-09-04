package app

import (
	"io"
	"os"
	"time"

	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/storage"
)

type ForgetResult struct {
	Slug string `json:"slug"`
}

func (r ForgetResult) Human(w io.Writer) {
	cli.Section(w, "Removed")
	cli.Line(w, "%s", r.Slug)
}

func (a *App) Forget(slug string) (*ForgetResult, error) {
	if err := storage.ValidateSlug(slug); err != nil {
		return nil, cli.Failf(cli.CodeInvalidArgs, "%v", err)
	}
	// A corrupt record used to make an app impossible to remove while it stayed
	// fully installable, so an unreadable record is a reason to remove. Likewise a directory with no record at all.
	_, ok, recErr := a.Store.Record(slug)
	orphan := false
	if !ok && recErr == nil {
		if info, err := os.Stat(a.Store.AppDir(slug)); err == nil && info.IsDir() {
			orphan = true
		}
	}
	if !ok && recErr == nil && !orphan {
		return nil, cli.Failf(cli.CodeNotFound, "no app published under %q", slug)
	}
	// A live build would write its record and payload back after this
	// removed them, leaving an app with no marker, no index entry and a
	// payload on disk.
	if building, err := a.Store.Building(); err == nil {
		if b, held := building[slug]; held && !markerStale(b, time.Now()) {
			return nil, cli.Failf(cli.CodeBuildInProgress,
				"a publish of %q is running (pid %d); it would recreate what forget removes", slug, b.PID).
				WithHint("wait for it to finish, or stop it first")
		}
	}
	if err := a.Store.Remove(slug); err != nil {
		return nil, cli.Failf(cli.CodeInternal, "%v", err)
	}
	if tr, err := a.Transport(); err == nil {
		if st := tr.Status(a.Config.Port); st.BaseURL != "" {
			_ = a.Reindex(st.BaseURL)
		}
	}
	return &ForgetResult{Slug: slug}, nil
}
