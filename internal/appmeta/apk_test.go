package appmeta

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jonathan-lor/otata/internal/artifact"
)

// The shape of `aapt2 dump badging` for a fresh template's debug build: the
// package line's several pairs, a label with an ampersand, one PNG among
// WebP icons, the adaptive icon's XML at anydpi, bare-word and value-only
// lines, and a label carrying an escaped quote in another locale.
const demoBadging = `package: name='com.example.demo' versionCode='34' versionName='1.2' platformBuildVersionName='16' platformBuildVersionCode='36' compileSdkVersion='36' compileSdkVersionCodename='16'
sdkVersion:'24'
targetSdkVersion:'36'
uses-permission: name='android.permission.INTERNET'
application-label:'Demo & Co'
application-label-en:'Demo & Co'
application-label-fr:'Demo \' Cie'
application-icon-160:'res/mipmap-mdpi-v4/ic_launcher.png'
application-icon-240:'res/mipmap-hdpi-v4/ic_launcher.webp'
application-icon-320:'res/mipmap-xhdpi-v4/ic_launcher.webp'
application-icon-480:'res/mipmap-xxhdpi-v4/ic_launcher.webp'
application-icon-640:'res/mipmap-xxxhdpi-v4/ic_launcher.webp'
application-icon-65534:'res/mipmap-anydpi-v26/ic_launcher.xml'
application: label='Demo & Co' icon='res/mipmap-anydpi-v26/ic_launcher.xml'
launchable-activity: name='com.example.demo.MainActivity'  label='' icon=''
feature-group: label=''
  uses-feature: name='android.hardware.faketouch'
main
supports-screens: 'small' 'normal' 'large' 'xlarge'
supports-any-density: 'true'
locales: '--_--' 'en' 'fr'
densities: '160' '240' '320' '480' '640' '65534'
native-code: 'arm64-v8a' 'x86_64'
`

const demoCerts = `Signer #1 certificate DN: CN=Android Debug, O=Android, C=US
Signer #1 certificate SHA-256 digest: 4F9C1A2B3C4D5E6F7A8B9C0D1E2F3A4B5C6D7E8F9A0B1C2D3E4F5A6B7C8D9E0F
Signer #1 certificate SHA-1 digest: 0123456789abcdef0123456789abcdef01234567
Signer #1 certificate MD5 digest: 0123456789abcdef0123456789abcdef
`

const unsignedVerdict = `DOES NOT VERIFY
ERROR: Missing META-INF/MANIFEST.MF
`

func TestParseBadging(t *testing.T) {
	b := parseBadging([]byte(demoBadging))
	if b.Package != "com.example.demo" || b.VersionCode != "34" || b.VersionName != "1.2" {
		t.Errorf("package = %q %q %q", b.Package, b.VersionCode, b.VersionName)
	}
	if b.Label != "Demo & Co" {
		t.Errorf("label = %q", b.Label)
	}
	if got := b.Icons[640]; got != "res/mipmap-xxxhdpi-v4/ic_launcher.webp" {
		t.Errorf("icon at 640 = %q", got)
	}
	if len(b.Icons) != 6 {
		t.Errorf("%d icons read, want 6: %v", len(b.Icons), b.Icons)
	}
	// With no application-label line the application line's label stands in.
	only := parseBadging([]byte("application: label='Plain' icon='x.png'\n"))
	if only.Label != "Plain" {
		t.Errorf("label from the application line = %q", only.Label)
	}
}

func TestBadgingPairs(t *testing.T) {
	cases := []struct {
		in   string
		want []badgingPair
	}{
		{` name='a' versionCode='1'`, []badgingPair{{"name", "a"}, {"versionCode", "1"}}},
		{`'bare'`, []badgingPair{{"", "bare"}}},
		{` 'a' 'b c'`, []badgingPair{{"", "a"}, {"", "b c"}}},
		{`'it\'s'`, []badgingPair{{"", "it's"}}},
		{` label='' icon=''`, []badgingPair{{"label", ""}, {"icon", ""}}},
		{``, nil},
		{`   `, nil},
		{`'unterminated`, []badgingPair{{"", "unterminated"}}},
	}
	for _, c := range cases {
		if got := badgingPairs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("badgingPairs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The highest density wins among raster icons; the adaptive icon's XML,
// though listed at the highest density of all, cannot be shown and is skipped.
func TestPickIcon(t *testing.T) {
	b := parseBadging([]byte(demoBadging))
	if got := pickIcon(b.Icons); got != "res/mipmap-xxxhdpi-v4/ic_launcher.webp" {
		t.Errorf("picked %q", got)
	}
	if got := pickIcon(map[int]string{65534: "res/x.xml"}); got != "" {
		t.Errorf("picked the XML: %q", got)
	}
	if got := pickIcon(map[int]string{160: "res/a.PNG", 320: "res/b.svg"}); got != "res/a.PNG" {
		t.Errorf("picked %q, want the PNG whatever its case", got)
	}
}

func TestSigningFromCerts(t *testing.T) {
	s, err := signingFromCerts([]byte(demoCerts))
	if err != nil {
		t.Fatal(err)
	}
	if s.Signer != "Android Debug" || s.Fingerprint != strings.ToLower("4F9C1A2B3C4D5E6F7A8B9C0D1E2F3A4B5C6D7E8F9A0B1C2D3E4F5A6B7C8D9E0F") {
		t.Errorf("signer = %q fingerprint = %q", s.Signer, s.Fingerprint)
	}
	if s.HasDeadline() {
		t.Error("an APK's signing was given a deadline")
	}
	// Only the first signer is read; a certificate that names nobody is an
	// identity by fingerprint alone.
	two := "Signer #1 certificate DN: O=Nobody Inc\nSigner #1 certificate SHA-256 digest: abc\nSigner #2 certificate DN: CN=Other\nSigner #2 certificate SHA-256 digest: def\n"
	s, err = signingFromCerts([]byte(two))
	if err != nil || s.Signer != "" || s.Fingerprint != "abc" {
		t.Errorf("got %+v, %v", s, err)
	}
	if _, err := signingFromCerts([]byte("WARNING: something\n")); err == nil {
		t.Error("output naming no certificate was accepted")
	}
}

func TestCommonName(t *testing.T) {
	cases := map[string]string{
		"CN=Android Debug, O=Android, C=US": "Android Debug",
		"O=Android, CN=Later":               "Later",
		`CN=Lor\, Jonathan, O=Anakepha`:     "Lor, Jonathan",
		"O=Nobody":                          "",
		"":                                  "",
		"CN=":                               "",
		"C=US,O=Tight,CN=No Spaces":         "No Spaces",
	}
	for dn, want := range cases {
		if got := commonName(dn); got != want {
			t.Errorf("commonName(%q) = %q, want %q", dn, got, want)
		}
	}
}

// The newest build-tools release wins, a directory without the tool is
// skipped, and with no SDK at all PATH is the fallback.
func TestBuildToolPrefersTheNewestRelease(t *testing.T) {
	sdk := t.TempDir()
	for _, rel := range []string{"34.0.0", "36.0.0", "36.1.0-rc1", "9.0.0"} {
		dir := filepath.Join(sdk, "build-tools", rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if rel == "36.1.0-rc1" {
			continue // a release without the tool
		}
		if err := os.WriteFile(filepath.Join(dir, "aapt2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ANDROID_HOME", sdk)
	t.Setenv("ANDROID_SDK_ROOT", "")
	got, err := buildTool("aapt2")
	if err != nil || got != filepath.Join(sdk, "build-tools", "36.0.0", "aapt2") {
		t.Errorf("buildTool = %q, %v", got, err)
	}
	if _, err := buildTool("apksigner"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a tool no release holds: %v, want ErrUnsupported", err)
	}

	onPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(onPath, "apksigner"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("PATH", onPath)
	if got, err := buildTool("apksigner"); err != nil || got != filepath.Join(onPath, "apksigner") {
		t.Errorf("fallback to PATH: %q, %v", got, err)
	}
}

func TestVersionOrder(t *testing.T) {
	if !versionLess(versionOf("34.0.0"), versionOf("36.0.0")) || versionLess(versionOf("36.0.0"), versionOf("9.0.0")) {
		t.Error("versions compared as strings")
	}
	if !versionLess(versionOf("36.0.0-rc1"), versionOf("36.0.1")) || versionLess(versionOf("36.0"), versionOf("36.0.0")) {
		t.Error("a suffix or a missing part misordered")
	}
}

// stubTool writes a fake build-tool at dir/name that prints the file at
// output and exits with status, logging its arguments beside it.
func stubTool(t *testing.T, dir, name, output string, status int) (callLog string) {
	t.Helper()
	callLog = filepath.Join(dir, name+".calls")
	if err := os.WriteFile(filepath.Join(dir, name+".out"), []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> " + shellQuote(callLog) + "\n" +
		"while IFS= read -r line || [ -n \"$line\" ]; do printf '%s\\n' \"$line\"; done < " + shellQuote(filepath.Join(dir, name+".out")) + "\n" +
		"exit " + strconv.Itoa(status) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return callLog
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeDemoAPK builds the shape of a real APK: a manifest, the launcher
// icon at several densities in two formats, and the adaptive icon's XML.
func writeDemoAPK(t *testing.T) (path string, xxxhdpi []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "Demo.apk")
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
	xxxhdpi = []byte("RIFF....WEBPVP8 ") // a WebP's first bytes; only identity is checked
	add("AndroidManifest.xml", []byte("\x03\x00\x08\x00"))
	add("res/mipmap-mdpi-v4/ic_launcher.png", append(append([]byte(nil), pngSignature...), 0, 0, 0, 13))
	add("res/mipmap-xxxhdpi-v4/ic_launcher.webp", xxxhdpi)
	add("res/mipmap-anydpi-v26/ic_launcher.xml", []byte("<adaptive-icon/>"))
	add("classes.dex", []byte("dex\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path, xxxhdpi
}

// The Android reader end to end against a synthetic APK and stubbed
// build-tools: identity out of the badging, the highest-density raster icon
// out of the archive under its own format, the signer out of apksigner,
// and each tool invoked once with the APK's path.
func TestOpenReadsAnAPK(t *testing.T) {
	apkPath, xxxhdpi := writeDemoAPK(t)
	tools := t.TempDir()
	aaptCalls := stubTool(t, tools, "aapt2", demoBadging, 0)
	signerCalls := stubTool(t, tools, "apksigner", demoCerts, 0)
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	t.Setenv("PATH", tools)

	payload, err := Open(artifact.Android, apkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()

	info, err := payload.Info()
	if err != nil {
		t.Fatal(err)
	}
	want := Info{Name: "Demo & Co", BundleID: "com.example.demo", Title: "Demo & Co", Version: "1.2", Build: "34"}
	if info != want {
		t.Errorf("Info = %+v, want %+v", info, want)
	}

	dest := filepath.Join(t.TempDir(), "icon")
	ext, err := payload.Icon(dest)
	if err != nil {
		t.Fatalf("Icon: %v", err)
	}
	if ext != ".webp" {
		t.Errorf("icon format = %q, want .webp", ext)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(xxxhdpi) {
		t.Errorf("the icon written is not the xxxhdpi WebP: %q", got)
	}

	sig, err := payload.Signing(nil)
	if err != nil {
		t.Fatalf("Signing: %v", err)
	}
	if sig.Signer != "Android Debug" || sig.HasDeadline() || sig.Free {
		t.Errorf("Signing = %+v", sig)
	}

	// aapt2 ran once for Info and Icon together; each tool saw the APK.
	for name, log := range map[string]string{"aapt2": aaptCalls, "apksigner": signerCalls} {
		calls, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%s never ran", name)
		}
		lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], apkPath) {
			t.Errorf("%s calls = %q, want one naming the APK", name, lines)
		}
	}
}

// An APK that does not verify is ErrUnsigned, carrying apksigner's reason;
// one whose tools are absent is ErrUnsupported, which is a fact about this
// machine rather than the payload; and a zip that is not an APK is refused.
func TestAPKSigningFailures(t *testing.T) {
	apkPath, _ := writeDemoAPK(t)
	tools := t.TempDir()
	stubTool(t, tools, "apksigner", unsignedVerdict, 1)
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	t.Setenv("PATH", tools)

	payload, err := Open(artifact.Android, apkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer payload.Close()
	_, err = payload.Signing(nil)
	if !errors.Is(err, ErrUnsigned) || !strings.Contains(err.Error(), "MANIFEST.MF") {
		t.Errorf("an unverifiable APK: %v, want ErrUnsigned with the reason", err)
	}
	if _, err := payload.Info(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("with no aapt2: %v, want ErrUnsupported", err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := payload.Signing(nil); !errors.Is(err, ErrUnsupported) {
		t.Errorf("with no apksigner: %v, want ErrUnsupported", err)
	}

	notAPK := filepath.Join(t.TempDir(), "x.apk")
	f, err := os.Create(notAPK)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	if _, err := zw.Create("hello.txt"); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	f.Close()
	if _, err := Open(artifact.Android, notAPK); err == nil || !strings.Contains(err.Error(), "AndroidManifest.xml") {
		t.Errorf("a zip with no manifest opened as an APK: %v", err)
	}
}
