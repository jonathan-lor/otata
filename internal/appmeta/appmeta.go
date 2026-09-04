// Package appmeta reads identity, icon and signing out of a built app: what
// the install surface, the manifest and doctor need. Open picks the reader
// for a platform's payload; the iOS one works against an fs.FS rooted at the
// .app inside an .ipa, read in place without unpacking it.
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

// Ceilings for data read out of a .ipa, untrusted when it arrives via
// --artifact. Without them a 0.2 MB archive expands to hundreds: the icon is
// streamed to disk twice, the plist held, piped to plutil, unmarshaled again.
const (
	maxPlistBytes = 4 << 20  // 4 MB; a real Info.plist is a few KB
	maxIconBytes  = 16 << 20 // 16 MB; a real app icon is well under 2 MB

	maxProfileBytes = 4 << 20 // 4 MB; a profile carrying a full device list is well under 1 MB
	// maxProfileCerts bounds the per-certificate parse loop. The list comes
	// out of the profile itself, so without a ceiling a crafted one turns a
	// diagnostic into unbounded certificate parsing.
	maxProfileCerts = 64
)

// Info is an app's identity, as the pages and the manifest state it.
type Info struct {
	// Name is what the served payload is named after: the product's name on
	// disk for an iOS app (MyApp, for Payload/MyApp.app), the label for an
	// Android one, whose package name is a namespace rather than a name.
	Name     string
	BundleID string
	Title    string
	Version  string
	Build    string
}

// infoFrom pulls what the install surface and the manifest need out of a
// decoded Info.plist, with a stand-in for anything a malformed one lacks.
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

// withDefaults fills what the pages and the manifest cannot do without,
// for a payload whose metadata lacks it.
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

// decodePlist converts a plist of any encoding to JSON and parses that.
// Info.plist in a built app is binary, and Go has no stdlib plist reader.
// plutil ships with macOS, which iOS builds already require.
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
		// Include the exec error: when plutil fails without a word, stderr is empty and the message would otherwise end at the colon.
		return nil, fmt.Errorf("could not decode Info.plist: %v %s", err, strings.TrimSpace(errBuf.String()))
	}
	var parsed map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("could not parse decoded Info.plist: %w", err)
	}
	return parsed, nil
}

// readLimited reads at most max bytes, and reports oversize instead of
// truncating, since a truncated plist would fail to parse with a confusing
// error.
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

// findIcon picks the largest flat icon PNG in the bundle root.
//
// Xcode names these after the icon asset, not "AppIcon", and an Icon Composer
// icon carries its own name, so prefixes come from Info.plist instead of a guess.
// A miss is fine because the manifest's image assets are optional.
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
		// Size here is the archive's DECLARED uncompressed size, and the
		// largest candidate wins, so without this bound a zip bomb is actively
		// preferred over the real icon.
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

// ---------- icons ----------

// pngSignature is the 8-byte header every PNG starts with.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// pngFirstChunk returns the type of the first chunk in the PNG at path, or ""
// for anything that is not a PNG. A standard PNG opens with IHDR; one that Xcode's
// packaging has optimized opens with Apple's CgBI chunk instead.
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

// normalizeIcon rewrites the icon at path into standard PNG when Xcode's
// packaging has applied its iphone optimization (a CgBI chunk, byte-swapped
// channels, premultiplied alpha). That form decodes only in iOS's own
// frameworks. The revert runs on the Mac at publish, because pngcrush ships inside Xcode;
// an error means the caller should ship no icon, since the placeholder renders and a crushed PNG does not.
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
	// pngcrush reports some failures only in prose, exiting 0, so the result
	// is checked rather than trusted: what ships must open with IHDR.
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
	// Report the close error. A failed flush would otherwise leave the caller believing a partial file is complete.
	if err := out.Close(); err != nil {
		return err
	}
	if written > maxIconBytes {
		return fmt.Errorf("%s is larger than %d bytes", name, maxIconBytes)
	}
	return nil
}
