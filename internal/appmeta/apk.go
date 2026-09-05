package appmeta

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxBadgingBytes bounds what aapt2 prints for one APK. Real output is a few
// KB; a manifest crafted to carry thousands of localized labels is not a
// reason to hold megabytes.
const maxBadgingBytes = 4 << 20

// apk reads an Android payload. Identity, label and icon come out of `aapt2
// dump badging`, which resolves the binary manifest's resource references
// against the resource table, and the signer out of `apksigner verify
// --print-certs`. Both ship in the SDK's build-tools, which building an APK
// needs anyway; a machine without them serves the payload and cannot read
// it, which is ErrUnsupported, as an .ipa is off macOS.
type apk struct {
	path string
	zr   *zip.ReadCloser
	// badging is aapt2's report, kept once read: Info and Icon both need it
	// and reading it is a subprocess.
	badging *badging
}

func openAPK(apkPath string) (*apk, error) {
	zr, err := zip.OpenReader(apkPath)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", apkPath, err)
	}
	if _, err := fs.Stat(zr, "AndroidManifest.xml"); err != nil {
		zr.Close()
		return nil, fmt.Errorf("no AndroidManifest.xml inside %s", apkPath)
	}
	return &apk{path: apkPath, zr: zr}, nil
}

func (p *apk) Close() error { return p.zr.Close() }

func (p *apk) Info() (Info, error) {
	b, err := p.dump()
	if err != nil {
		return Info{}, err
	}
	// The served payload is named after the label, which is what the person
	// tapping it knows the app as; the package name is a namespace.
	name := b.Label
	if name == "" {
		name = b.Package[strings.LastIndex(b.Package, ".")+1:]
	}
	return withDefaults(Info{Name: name, BundleID: b.Package, Title: b.Label, Version: b.VersionName, Build: b.VersionCode}), nil
}

// Icon writes the launcher icon at the highest density that is a raster
// file. Adaptive icons are XML at anydpi, which a page cannot show, so the
// density-specific fallback every template ships is what is picked, WebP in
// recent ones, and served as it is.
func (p *apk) Icon(dest string) (string, error) {
	b, err := p.dump()
	if err != nil {
		return "", err
	}
	name := pickIcon(b.Icons)
	if name == "" {
		return "", ErrNoIcon
	}
	if err := copyOut(p.zr, name, dest); err != nil {
		return "", err
	}
	return strings.ToLower(path.Ext(name)), nil
}

func (p *apk) Signing(map[string]bool) (Signing, error) { return readAPKSigning(p.path) }

// dump runs aapt2 once and keeps its report.
func (p *apk) dump() (*badging, error) {
	if p.badging != nil {
		return p.badging, nil
	}
	aapt2, err := buildTool("aapt2")
	if err != nil {
		return nil, err
	}
	out, stderr, err := runTool(aapt2, "dump", "badging", p.path)
	if err != nil {
		return nil, fmt.Errorf("could not read the APK's manifest: %v %s", err, stderr)
	}
	if len(out) > maxBadgingBytes {
		return nil, fmt.Errorf("the APK's manifest report is larger than %d bytes", maxBadgingBytes)
	}
	p.badging = parseBadging(out)
	return p.badging, nil
}

// badging is what this package reads out of `aapt2 dump badging`.
type badging struct {
	Package, VersionCode, VersionName, Label string
	// Icons is the launcher icon's file per density, as aapt2 lists them.
	Icons map[int]string
}

// parseBadging reads aapt2's report: one entry per line, the entry's name
// before the first colon, then either bare quoted values or name='value'
// pairs. Only the entries this package acts on are kept.
func parseBadging(out []byte) *badging {
	b := &badging{Icons: map[int]string{}}
	for line := range strings.SplitSeq(string(out), "\n") {
		key, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		pairs := badgingPairs(rest)
		switch {
		case key == "package":
			for _, kv := range pairs {
				switch kv.name {
				case "name":
					b.Package = kv.value
				case "versionCode":
					b.VersionCode = kv.value
				case "versionName":
					b.VersionName = kv.value
				}
			}
		case key == "application-label":
			if len(pairs) > 0 {
				b.Label = pairs[0].value
			}
		case key == "application":
			// The default label again, for a report with no application-label line.
			for _, kv := range pairs {
				if kv.name == "label" && b.Label == "" {
					b.Label = kv.value
				}
			}
		case strings.HasPrefix(key, "application-icon-"):
			density, err := strconv.Atoi(strings.TrimPrefix(key, "application-icon-"))
			if err == nil && len(pairs) > 0 {
				b.Icons[density] = pairs[0].value
			}
		}
	}
	return b
}

type badgingPair struct{ name, value string }

// badgingPairs splits the rest of a badging line into its quoted values,
// each with the name before its equals sign, or none for a bare value. A
// quote inside a value is escaped with a backslash.
func badgingPairs(s string) []badgingPair {
	var pairs []badgingPair
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		name := ""
		if s[i] != '\'' {
			start := i
			for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != '\t' {
				i++
			}
			if i >= len(s) || s[i] != '=' || i+1 >= len(s) || s[i+1] != '\'' {
				continue // a word with no quoted value, as in "main"
			}
			name = s[start:i]
			i++
		}
		// At an opening quote.
		i++
		var value strings.Builder
		for i < len(s) && s[i] != '\'' {
			if s[i] == '\\' && i+1 < len(s) {
				i++
			}
			value.WriteByte(s[i])
			i++
		}
		i++ // the closing quote, or the end
		pairs = append(pairs, badgingPair{name, value.String()})
	}
	return pairs
}

// pickIcon is the raster icon at the highest density. anydpi entries are
// adaptive-icon XML and are skipped, as is anything that is not a PNG or
// WebP, since nothing here can turn it into one.
func pickIcon(icons map[int]string) string {
	best, bestDensity := "", -1
	for density, name := range icons {
		switch strings.ToLower(path.Ext(name)) {
		case ".png", ".webp":
		default:
			continue
		}
		if density > bestDensity {
			best, bestDensity = name, density
		}
	}
	return best
}

// ---------- signing ----------

// readAPKSigning reports who signed the APK. The signature is verified, not
// merely read: an APK that does not verify does not install, which is
// ErrUnsigned, and an unsigned release build is exactly what a project with
// no signing config produces.
func readAPKSigning(apkPath string) (Signing, error) {
	apksigner, err := buildTool("apksigner")
	if err != nil {
		return Signing{}, err
	}
	out, stderr, err := runTool(apksigner, "verify", "--print-certs", apkPath)
	if err != nil {
		all := string(out) + "\n" + stderr
		if strings.Contains(all, "DOES NOT VERIFY") {
			return Signing{}, fmt.Errorf("%w: %s", ErrUnsigned, firstErrorLine(all))
		}
		return Signing{}, fmt.Errorf("could not verify the APK's signature: %v %s", err, stderr)
	}
	return signingFromCerts(out)
}

// signingFromCerts reads the first signer out of `apksigner verify
// --print-certs`. Split from the running so the parse is testable.
func signingFromCerts(out []byte) (Signing, error) {
	var s Signing
	seenDN := false
	for line := range strings.SplitSeq(string(out), "\n") {
		if _, dn, ok := strings.Cut(line, "certificate DN: "); ok && !seenDN {
			s.Signer, seenDN = commonName(strings.TrimSpace(dn)), true
		}
		if _, hex, ok := strings.Cut(line, "certificate SHA-256 digest: "); ok && s.Fingerprint == "" {
			s.Fingerprint = strings.ToLower(strings.TrimSpace(hex))
		}
	}
	if s.Fingerprint == "" {
		return Signing{}, fmt.Errorf("apksigner reported no signing certificate")
	}
	return s, nil
}

// commonName is the CN out of a distinguished name as apksigner prints it,
// "CN=Android Debug, O=Android, C=US", where a comma inside a value is
// escaped with a backslash.
func commonName(dn string) string {
	var fields []string
	var field strings.Builder
	for i := 0; i < len(dn); i++ {
		switch {
		case dn[i] == '\\' && i+1 < len(dn):
			i++
			field.WriteByte(dn[i])
		case dn[i] == ',':
			fields = append(fields, field.String())
			field.Reset()
		default:
			field.WriteByte(dn[i])
		}
	}
	fields = append(fields, field.String())
	for _, f := range fields {
		if cn, ok := strings.CutPrefix(strings.TrimSpace(f), "CN="); ok {
			return strings.TrimSpace(cn)
		}
	}
	return ""
}

// firstErrorLine is the line that says why, for a failure that says so.
func firstErrorLine(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "ERROR") {
			return strings.TrimSpace(line)
		}
	}
	return "the signature does not verify"
}

// ---------- the build-tools ----------

// buildTool finds one of the SDK's build-tools: the newest release under
// ANDROID_HOME, or ANDROID_SDK_ROOT for an older setup, then PATH. Neither
// is a fixed path, since every release lands in a directory of its own.
func buildTool(name string) (string, error) {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if sdk := os.Getenv(env); sdk != "" {
			if p := newestBuildTool(filepath.Join(sdk, "build-tools"), name); p != "" {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("an APK %w: it needs %s from the Android SDK's build-tools; install them and set ANDROID_HOME", ErrUnsupported, name)
}

// newestBuildTool is name inside the highest-versioned release directory
// under dir that holds it and can run it.
func newestBuildTool(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	var bestVersion []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name(), name)
		if info, err := os.Stat(p); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if v := versionOf(e.Name()); best == "" || versionLess(bestVersion, v) {
			best, bestVersion = p, v
		}
	}
	return best
}

// versionOf reads "36.0.0" or "36.0.0-rc1" as its numeric parts.
func versionOf(s string) []int {
	var v []int
	for part := range strings.SplitSeq(s, ".") {
		digits := part
		if i := strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			digits = part[:i]
		}
		n, _ := strconv.Atoi(digits)
		v = append(v, n)
	}
	return v
}

func versionLess(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// runTool runs one of the build-tools with a bound on its time, returning
// what it printed on each stream.
func runTool(bin string, args ...string) (stdout []byte, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err = cmd.Run()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("%s took longer than 30 seconds", filepath.Base(bin))
	}
	return out.Bytes(), strings.TrimSpace(errBuf.String()), err
}
