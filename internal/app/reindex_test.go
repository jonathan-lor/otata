package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/storage"
)

// A manifest is written only for the platform that installs from one. An
// Android app's page links its payload, and a manifest beside it would be a
// file nothing fetches that doctor would then go on to probe.
func TestReindexWritesManifestsOnlyForIOS(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	now := time.Now()
	for _, r := range []artifact.Record{
		{Slug: "iosapp", Platform: artifact.IOS, PayloadName: "App.ipa", BuiltAt: now},
		{Slug: "droid", Platform: artifact.Android, PayloadName: "App.apk", BuiltAt: now},
	} {
		if err := store.PutRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Reindex("https://host/otata"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.html", "iosapp/index.html", "iosapp/manifest.plist", "droid/index.html"} {
		if _, err := os.Stat(filepath.Join(store.Public(), want)); err != nil {
			t.Errorf("%s was not written: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Public(), "droid", "manifest.plist")); err == nil {
		t.Error("a manifest was written for an Android app")
	}
}
