package appmeta

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// Reads the real APK at OTATA_TEST_APK through the real build-tools. Skipped
// when unset.
func TestReadsRealAPK(t *testing.T) {
	apkPath := os.Getenv("OTATA_TEST_APK")
	if apkPath == "" {
		t.Skip("OTATA_TEST_APK not set")
	}
	if _, err := os.Stat(apkPath); err != nil {
		t.Fatalf("OTATA_TEST_APK: %v", err)
	}
	payload, err := Open(artifact.Android, apkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()

	info, err := payload.Info()
	if err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(t.TempDir(), "icon")
	ext, iconErr := payload.Icon(icon)
	t.Logf("name=%s package=%s title=%q version=%s build=%s icon=%q %v",
		info.Name, info.BundleID, info.Title, info.Version, info.Build, ext, iconErr)
	if info.BundleID == "" {
		t.Error("no package name")
	}
	if iconErr == nil && ext != ".png" && ext != ".webp" {
		t.Errorf("icon format %q is not one a page can show", ext)
	}

	sig, err := payload.Signing(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signer=%q fingerprint=%s detail=%q", sig.Signer, sig.Fingerprint, sig.Detail(time.Now()))
	if sig.Fingerprint == "" {
		t.Error("no fingerprint")
	}
	if sig.HasDeadline() {
		t.Error("an APK's signing was given a deadline")
	}
}
