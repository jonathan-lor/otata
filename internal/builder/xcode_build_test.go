package builder

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestAppFromSettingsPicksAppProduct(t *testing.T) {
	out := []byte(`[
	  {"buildSettings":{"WRAPPER_EXTENSION":"framework","TARGET_BUILD_DIR":"/b/Release-iphoneos","FULL_PRODUCT_NAME":"Lib.framework"}},
	  {"buildSettings":{"WRAPPER_EXTENSION":"app"}},
	  {"buildSettings":{"WRAPPER_EXTENSION":"app","TARGET_BUILD_DIR":"/b/Release-iphoneos","FULL_PRODUCT_NAME":"Demo.app"}}
	]`)
	app, err := appFromSettings(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/b/Release-iphoneos", "Demo.app"); app != want {
		t.Fatalf("got %q, want %q", app, want)
	}
}

func TestAppFromSettingsNoApp(t *testing.T) {
	out := []byte(`[{"buildSettings":{"WRAPPER_EXTENSION":"framework","TARGET_BUILD_DIR":"/b","FULL_PRODUCT_NAME":"Lib.framework"}}]`)
	if _, err := appFromSettings(out); err == nil {
		t.Fatal("expected an error when no entry is an app product")
	}
}

func TestAppFromSettingsBadJSON(t *testing.T) {
	if _, err := appFromSettings([]byte("not json")); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestPackageIPA(t *testing.T) {
	work := t.TempDir()
	app := filepath.Join(t.TempDir(), "Dummy.app")
	if err := os.MkdirAll(filepath.Join(app, "Frameworks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dummy", "Info.plist", "Frameworks/Lib.dylib"} {
		if err := os.WriteFile(filepath.Join(app, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ipa, err := packageIPA(work, app)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(work, "export", "Dummy.ipa"); ipa != want {
		t.Fatalf("ipa at %q, want %q", ipa, want)
	}

	r, err := zip.OpenReader(ipa)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string]bool{}
	for _, f := range r.File {
		got[f.Name] = true
	}
	for _, want := range []string{
		"Payload/Dummy.app/Dummy",
		"Payload/Dummy.app/Info.plist",
		"Payload/Dummy.app/Frameworks/Lib.dylib",
	} {
		if !got[want] {
			t.Fatalf("ipa is missing %s", want)
		}
	}
}
