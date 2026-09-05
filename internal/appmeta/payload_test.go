package appmeta

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// A minimal Info.plist in the XML form, which plutil decodes like a binary one.
const demoInfoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
	<key>CFBundleIdentifier</key><string>com.example.demo</string>
	<key>CFBundleDisplayName</key><string>Demo &amp; Co</string>
	<key>CFBundleShortVersionString</key><string>1.2</string>
	<key>CFBundleVersion</key><string>34</string>
	<key>CFBundleIcons</key><dict>
		<key>CFBundlePrimaryIcon</key><dict>
			<key>CFBundleIconFiles</key><array><string>AppIcon60x60</string></array>
		</dict>
	</dict>
</dict></plist>`

// writeDemoIPA builds the shape of a real .ipa (Payload/<Name>.app/…) with an
// Info.plist, a small and a large icon, and no provisioning profile.
func writeDemoIPA(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Demo.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name string, body []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	icon := func(side int) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, side, side))); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	add("Payload/Demo.app/Info.plist", []byte(demoInfoPlist))
	add("Payload/Demo.app/AppIcon60x60@2x.png", icon(8))
	add("Payload/Demo.app/AppIcon60x60@3x.png", icon(64))
	add("Payload/Demo.app/Demo", []byte("binary"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// The iOS reader against a synthetic payload: identity from the plist with
// the product's name from the bundle, the largest icon out as a standard PNG,
// and a payload with no profile saying so rather than failing.
func TestOpenReadsAnIPA(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil is not installed")
	}
	payload, err := Open(artifact.IOS, writeDemoIPA(t))
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()

	info, err := payload.Info()
	if err != nil {
		t.Fatal(err)
	}
	want := Info{Name: "Demo", BundleID: "com.example.demo", Title: "Demo & Co", Version: "1.2", Build: "34"}
	if info != want {
		t.Errorf("Info = %+v, want %+v", info, want)
	}

	dest := filepath.Join(t.TempDir(), "icon")
	ext, err := payload.Icon(dest)
	if err != nil {
		t.Fatalf("Icon: %v", err)
	}
	if ext != ".png" {
		t.Errorf("an iOS icon reported as %q, want .png", ext)
	}
	f, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("the icon written is not a standard PNG: %v", err)
	}
	if img.Bounds().Dx() != 64 {
		t.Errorf("icon is %d px wide; the largest candidate should win", img.Bounds().Dx())
	}

	_, err = payload.Signing(nil)
	switch {
	case runtime.GOOS == "darwin" && !errors.Is(err, ErrNoProfile):
		t.Errorf("a payload without a profile: got %v, want ErrNoProfile", err)
	case runtime.GOOS != "darwin" && !errors.Is(err, ErrUnsupported):
		t.Errorf("reading a profile off macOS: got %v, want ErrUnsupported", err)
	}
}

// A platform with no reader is refused as unsupported, so doctor and publish
// say nothing about it rather than something wrong, and a payload that is
// not there does not open for any platform.
func TestOpenRefusesAPlatformWithoutAReader(t *testing.T) {
	_, err := Open(artifact.Platform("windows"), "whatever.exe")
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("got %v, want ErrUnsupported", err)
	}
	for _, p := range []artifact.Platform{artifact.IOS, artifact.Android} {
		if _, err := Open(p, filepath.Join(t.TempDir(), "missing"+p.PayloadExt())); err == nil {
			t.Errorf("a missing %s payload opened", p)
		}
	}
}
