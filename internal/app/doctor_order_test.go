package app

import (
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/config"
	"github.com/jonathan-lor/otata/internal/server"
	"github.com/jonathan-lor/otata/internal/storage"
)

// The URL probes fly concurrently but the report must read as it always has:
// the index, then each app's manifest, payload and signing, newest app first.
func TestDoctorReportsChecksInOrderHoweverProbesLand(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Root: root, Store: store, Config: config.Config{
		ServePath: "/otata",
		Transport: "manual",
		Manual: &config.Manual{
			// Port 1 answers nothing, so every URL probe fails fast without
			// leaving the machine; the order they report in is what is under test.
			BaseURL:    "https://127.0.0.1:1/otata",
			KeepPrefix: true,
			Visibility: "private",
		},
	}}

	// Two published records. Their payload files are absent on purpose.
	// The signing check has nothing to read and stays silent.
	now := time.Now()
	for slug, built := range map[string]time.Time{"older": now.Add(-time.Hour), "newer": now} {
		if err := store.PutRecord(artifact.Record{Slug: slug, PayloadName: "App.ipa", BuiltAt: built}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteFile(filepath.Join(store.Public(), "index.html"), []byte("<html>")); err != nil {
		t.Fatal(err)
	}

	// A real server for this root, so doctor gets past its server checks.
	srv, err := server.New(store.Public(), a.IncomingPrefix(), a.RootDigest(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go http.Serve(ln, srv)
	a.Config.Port = ln.Addr().(*net.TCPAddr).Port

	res, err := a.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Checks {
		names = append(names, c.Name)
	}
	want := []string{"index", "newer manifest", "newer payload", "older manifest", "older payload"}
	if !slices.Equal(names, want) {
		t.Errorf("checks = %v, want %v", names, want)
	}
	if res.Healthy {
		t.Error("probes against a dead port reported healthy")
	}
}
