// Package appmeta reads identity, icon and signing out of a built .ipa or .apk.
package appmeta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

// Limits on what is read out of a payload, which is untrusted from --artifact.
const (
	maxPlistBytes = 4 << 20  // 4 MB; a real Info.plist is a few KB
	maxIconBytes  = 16 << 20 // 16 MB; a real app icon is well under 2 MB

	maxProfileBytes = 4 << 20 // 4 MB; a profile carrying a full device list is well under 1 MB
	// maxProfileCerts caps how many of a profile's certificates are parsed.
	maxProfileCerts = 64
)

// Info is an app's identity.
type Info struct {
	// Name names the served payload: MyApp for Payload/MyApp.app, or an APK's label.
	Name     string
	BundleID string
	Title    string
	Version  string
	Build    string
}

// infoFrom reads Info out of a decoded Info.plist.
func infoFrom(name string, plist map[string]any) Info {
	info := Info{
		Name:     name,
		BundleID: str(plist, "CFBundleIdentifier"),
		Version:  str(plist, "CFBundleShortVersionString"),
		Build:    str(plist, "CFBundleVersion"),
	}
	if info.Title = str(plist, "CFBundleDisplayName"); info.Title == "" {
		info.Title = str(plist, "CFBundleName")
	}
	return withDefaults(info)
}

// withDefaults fills in what a malformed payload leaves out.
func withDefaults(info Info) Info {
	if info.Title == "" {
		info.Title = "App"
	}
	if info.Version == "" {
		info.Version = "0"
	}
	if info.Build == "" {
		info.Build = "0"
	}
	return info
}

// decodePlist parses a plist of any encoding through plutil, since a built
// Info.plist is binary and Go has no plist reader.
func decodePlist(raw []byte) (map[string]any, error) {
	if _, err := exec.LookPath("plutil"); err != nil {
		return nil, fmt.Errorf("Info.plist %w: it needs plutil, which macOS ships", ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "-")
	cmd.Stdin = bytes.NewReader(raw)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// Include err, since plutil can fail with nothing on stderr.
		return nil, fmt.Errorf("could not decode Info.plist: %v %s", err, strings.TrimSpace(errBuf.String()))
	}
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("could not parse decoded Info.plist: %w", err)
	}
	return parsed, nil
}

// readLimited reads name, failing rather than truncating past max bytes.
func readLimited(app fs.FS, name string, max int64) ([]byte, error) {
	f, err := app.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return data, nil
}

func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// findIcon picks the largest flat icon PNG in the bundle root, matching the
// icon names Info.plist declares, since Xcode doesn't always call it AppIcon.
func findIcon(app fs.FS, plist map[string]any) string {
	var prefixes []string
	if icons, ok := plist["CFBundleIcons"].(map[string]any); ok {
		if primary, ok := icons["CFBundlePrimaryIcon"].(map[string]any); ok {
			if files, ok := primary["CFBundleIconFiles"].([]any); ok {
				for _, f := range files {
					if s, ok := f.(string); ok {
						prefixes = append(prefixes, s)
					}
				}
			}
			if name := str(primary, "CFBundleIconName"); name != "" {
				prefixes = append(prefixes, name)
			}
		}
	}
	prefixes = append(prefixes, "AppIcon")

	entries, err := fs.ReadDir(app, ".")
	if err != nil {
		return ""
	}
	type candidate struct {
		name string
		size int64
	}
	var found []candidate
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(path.Ext(name), ".png") {
			continue
		}
		matches := strings.Contains(strings.ToLower(name), "icon")
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// The size is what the zip declares, so cap it or a zip bomb wins.
		if info.Size() > maxIconBytes {
			continue
		}
		found = append(found, candidate{name, info.Size()})
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool { return found[i].size > found[j].size })
	return found[0].name
}

// pngSignature is the 8-byte header every PNG starts with.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// pngFirstChunk returns the type of the first chunk in the PNG at path, or ""
// if it isn't a PNG. Xcode's optimized PNGs open with CgBI instead of IHDR.
func pngFirstChunk(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 16)
	if _, err := io.ReadFull(f, head); err != nil {
		return ""
	}
	if !bytes.Equal(head[:8], pngSignature) {
		return ""
	}
	return string(head[12:16])
}

// normalizeIcon reverts Xcode's iPhone PNG optimization, which only iOS can
// decode, with the pngcrush that ships with Xcode.
func normalizeIcon(path string) error {
	if pngFirstChunk(path) != "CgBI" {
		return nil // standard PNG, or not a PNG at all; nothing to revert
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out := path + ".standard"
	defer os.Remove(out) // no-op once renamed
	cmd := exec.CommandContext(ctx, "xcrun", "pngcrush", "-revert-iphone-optimizations", "-q", path, out)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not revert the icon's iphone optimization: %v %s",
			err, strings.TrimSpace(errBuf.String()))
	}
	// pngcrush can fail and still exit 0, so check the result.
	if pngFirstChunk(out) != "IHDR" {
		return fmt.Errorf("pngcrush did not produce a standard PNG from %s", path)
	}
	return os.Rename(out, path)
}

// copyOut writes one file from the bundle to dest, bounded.
func copyOut(app fs.FS, name, dest string) error {
	src, err := app.Open(name)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	written, err := io.Copy(out, io.LimitReader(src, maxIconBytes+1))
	if err != nil {
		out.Close()
		return err
	}
	// A failed close can mean a partial file.
	if err := out.Close(); err != nil {
		return err
	}
	if written > maxIconBytes {
		return fmt.Errorf("%s is larger than %d bytes", name, maxIconBytes)
	}
	return nil
}
