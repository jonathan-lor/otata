package appmeta

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The shape of a real provisioning profile, trimmed: every value type one
// carries, the XML declaration and DOCTYPE, an entity in a string, and base64
// wrapped across lines the way Apple writes it.
const profileShapedPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>AppIDName</key><string>A &amp; B</string>
	<key>ExpirationDate</key><date>2026-11-11T00:27:46Z</date>
	<key>TeamIdentifier</key>
	<array>
		<string>WDT3B55TUP</string>
	</array>
	<key>DeveloperCertificates</key>
	<array>
		<data>
		aGVs
		bG8=
		</data>
	</array>
	<key>TimeToLive</key><integer>365</integer>
	<key>IsXcodeManaged</key><false/>
	<key>Entitlements</key>
	<dict>
		<key>get-task-allow</key><true/>
	</dict>
</dict>
</plist>`

func TestParseProfileReadsEveryValueType(t *testing.T) {
	profile, err := parseProfile([]byte(profileShapedPlist))
	if err != nil {
		t.Fatal(err)
	}
	if got := profile["AppIDName"]; got != "A & B" {
		t.Errorf("string with an entity = %q, want %q", got, "A & B")
	}
	want := time.Date(2026, 11, 11, 0, 27, 46, 0, time.UTC)
	if got, ok := profile["ExpirationDate"].(time.Time); !ok || !got.Equal(want) {
		t.Errorf("date = %v, want %s", profile["ExpirationDate"], want)
	}
	if ids, _ := profile["TeamIdentifier"].([]any); len(ids) != 1 || ids[0] != "WDT3B55TUP" {
		t.Errorf("array of strings = %v", profile["TeamIdentifier"])
	}
	certs, _ := profile["DeveloperCertificates"].([]any)
	if len(certs) != 1 {
		t.Fatalf("data array = %v", profile["DeveloperCertificates"])
	}
	if got, _ := certs[0].([]byte); !bytes.Equal(got, []byte("hello")) {
		t.Errorf("wrapped base64 decoded to %q, want %q", got, "hello")
	}
	if got, _ := profile["TimeToLive"].(int64); got != 365 {
		t.Errorf("integer = %v", profile["TimeToLive"])
	}
	if got, ok := profile["IsXcodeManaged"].(bool); !ok || got {
		t.Errorf("false = %v", profile["IsXcodeManaged"])
	}
	ent, _ := profile["Entitlements"].(map[string]any)
	if got, ok := ent["get-task-allow"].(bool); !ok || !got {
		t.Errorf("nested dict bool = %v", ent)
	}
}

// A document the parser cannot faithfully represent must be refused.
// A silently dropped or nil value here would surface as a wrong signing deadline.
func TestParsePlistXMLFailsClosed(t *testing.T) {
	bad := map[string]string{
		"not xml":             `hello`,
		"not a plist":         `<html><body>x</body></html>`,
		"truncated":           `<plist><dict><key>Name</key><string>x</string>`,
		"value with no key":   `<plist><dict><string>x</string></dict></plist>`,
		"key with no value":   `<plist><dict><key>Name</key></dict></plist>`,
		"unsupported element": `<plist><dict><key>X</key><uid>1</uid></dict></plist>`,
		"bad base64":          `<plist><dict><key>X</key><data>!!!</data></dict></plist>`,
		"bad date":            `<plist><dict><key>X</key><date>yesterday</date></dict></plist>`,
		"bad integer":         `<plist><dict><key>X</key><integer>seven</integer></dict></plist>`,
		"stray text in dict":  `<plist><dict>surprise<key>X</key><true/></dict></plist>`,
	}
	for name, doc := range bad {
		if _, err := parsePlistXML([]byte(doc)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	// A parseable plist whose root is not a dictionary is not a profile.
	if _, err := parseProfile([]byte(`<plist><array/></plist>`)); err == nil {
		t.Error("an array-rooted plist was accepted as a profile")
	}
}

// A binary payload would be a first for a profile, but the fallback exists, so it is exercised.
func TestParseProfileConvertsABinaryPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS's")
	}
	path := filepath.Join(t.TempDir(), "profile.plist")
	if err := os.WriteFile(path, []byte(profileShapedPlist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-convert", "binary1", path).CombinedOutput(); err != nil {
		t.Fatalf("could not make the binary fixture: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("bplist")) {
		t.Fatal("fixture did not convert to binary")
	}
	profile, err := parseProfile(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 11, 0, 27, 46, 0, time.UTC)
	if got, ok := profile["ExpirationDate"].(time.Time); !ok || !got.Equal(want) {
		t.Errorf("date through the binary fallback = %v, want %s", profile["ExpirationDate"], want)
	}
	if got := profile["AppIDName"]; got != "A & B" {
		t.Errorf("string through the binary fallback = %q", got)
	}
}
