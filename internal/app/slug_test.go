package app

import (
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/cli"
	"github.com/jonathan-lor/otata/internal/storage"
)

func TestDefaultSlug(t *testing.T) {
	cases := []struct {
		dir      string
		platform artifact.Platform
		want     string
	}{
		{"/src/MyApp", artifact.IOS, "myapp"},
		{"/src/MyApp", artifact.Android, "myapp-android"},
		{"/src/my_app 2", artifact.IOS, "my-app-2"},
		{"/src/my_app 2", artifact.Android, "my-app-2-android"},
	}
	for _, c := range cases {
		if got := DefaultSlug(c.dir, c.platform); got != c.want {
			t.Errorf("DefaultSlug(%q, %s) = %q, want %q", c.dir, c.platform, got, c.want)
		}
		if err := storage.ValidateSlug(DefaultSlug(c.dir, c.platform)); err != nil {
			t.Errorf("DefaultSlug(%q, %s) does not validate: %v", c.dir, c.platform, err)
		}
	}
}

func TestCheckSlugRefusesAnotherPlatformsBuild(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Store: store}
	if err := store.PutRecord(artifact.Record{Slug: "myapp", Platform: artifact.IOS, ProjectPath: "/src/MyApp"}); err != nil {
		t.Fatal(err)
	}

	if err := a.CheckSlug("myapp", "/src/MyApp", artifact.IOS); err != nil {
		t.Errorf("republishing the same platform was refused: %v", err)
	}
	if err := a.CheckSlug("other", "/src/MyApp", artifact.Android); err != nil {
		t.Errorf("an unused slug was refused: %v", err)
	}

	err = a.CheckSlug("myapp", "/src/Other", artifact.IOS)
	if f := cli.AsFailure(err); err == nil || f.Code != cli.CodeSlugConflict {
		t.Errorf("another project's publish: err=%v", err)
	}

	err = a.CheckSlug("myapp", "/src/MyApp", artifact.Android)
	f := cli.AsFailure(err)
	if err == nil || f.Code != cli.CodeSlugConflict {
		t.Fatalf("another platform's publish under the same slug: err=%v", err)
	}
	if !strings.Contains(f.Message, "ios") || !strings.Contains(f.Hint, `"myapp-android"`) {
		t.Errorf("the refusal does not name the held platform and the default slug: %q / %q", f.Message, f.Hint)
	}
}
