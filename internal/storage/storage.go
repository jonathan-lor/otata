// Package storage owns the on-disk layout and is the single definition of it.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jonathan-lor/otata/internal/artifact"
	"github.com/jonathan-lor/otata/internal/atomicfile"
)

type Store struct{ root string }

// slugPattern is the only shape a slug may take, and is deliberately strict because
// a slug becomes a path component AND a URL path segment, so anything that could
// traverse, escape or need encoding is rejected.
//
// It accepts exactly what Slugify produces, alphanumeric at both ends, so a
// derived slug always validates and an explicit --slug must look the same. A
// looser validator would let the two disagree about what a slug is.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ErrBadSlug is returned for any slug that is not a single safe path element.
var ErrBadSlug = errors.New("slug must be 1-64 characters of a-z, 0-9 and dashes, starting and ending alphanumeric")

// ValidateSlug guards every path this package builds. It lives here rather
// than in the caller because this package owns the layout. A caller that
// forgets to validate must not be able to write or delete outside the root.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("%q: %w", slug, ErrBadSlug)
	}
	return nil
}

// ---------- layout ----------

// Every path under the root is spelled here and nowhere else. Only Public is
// ever served; State names local paths; Tmp stages what becomes served; the
// rest never enters the served tree at all.
func (s *Store) Public() string { return filepath.Join(s.root, "public") }
func (s *Store) State() string  { return filepath.Join(s.root, "state") }
func (s *Store) Tmp() string    { return filepath.Join(s.root, "tmp") }

// AppDir returns "" for an invalid slug. every caller that writes or deletes
// checks ValidateSlug first, and this is the second line of defense.
func (s *Store) AppDir(slug string) string {
	if ValidateSlug(slug) != nil {
		return ""
	}
	return filepath.Join(s.Public(), slug)
}

// BuildDir is a slug's scratch space for archives, exports and the build
// log. "" for an invalid slug, as AppDir.
func (s *Store) BuildDir(slug string) string {
	if ValidateSlug(slug) != nil {
		return ""
	}
	return filepath.Join(s.root, "build", slug)
}

// ServerLog is where the background server writes, its own log and the
// access log in one file.
func (s *Store) ServerLog() string { return filepath.Join(s.root, "server.log") }

// StagedBinary is where a copy of the otata binary goes when the installed
// one sits where the service manager cannot run it. Under the root, which is never
// inside a protected directory.
func (s *Store) StagedBinary() string { return filepath.Join(s.root, "bin", "otata") }

// TmpFile is a scratch path under Tmp. name should carry the slug, so two
// publishes never share one.
func (s *Store) TmpFile(name string) string { return filepath.Join(s.Tmp(), name) }

// The served files. IndexPath is the one index; the rest live in an app's
// directory and are "" for an invalid slug, or for a payload name that is not
// a bare file name, so a hand-edited record cannot name a file elsewhere.
func (s *Store) IndexPath() string                    { return filepath.Join(s.Public(), "index.html") }
func (s *Store) AppIndexPath(slug string) string      { return s.appFile(slug, "index.html") }
func (s *Store) ManifestPath(slug string) string      { return s.appFile(slug, "manifest.plist") }
func (s *Store) IconPath(slug, name string) string    { return s.appFile(slug, name) }
func (s *Store) PayloadPath(slug, name string) string { return s.appFile(slug, name) }

func (s *Store) appFile(slug, name string) string {
	dir := s.AppDir(slug)
	if dir == "" || name == "" || filepath.Base(name) != name {
		return ""
	}
	return filepath.Join(dir, name)
}

func Open(root string) (*Store, error) {
	s := &Store{root: root}
	for _, dir := range []string{s.Public(), s.State(), s.Tmp()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("could not create %s: %w", dir, err)
		}
	}
	return s, nil
}

// ---------- atomic writes ----------

// writeAtomic stages into tmp and renames into place. tmp, never beside the
// destination because the destination tree is served, and a staged file beside a
// payload would be momentarily fetchable. It lives under the same root, hence
// the same filesystem, which is what makes the rename atomic rather than a
// copy so a phone can never observe a half-written file.
func (s *Store) writeAtomic(dest string, write func(io.Writer) error) error {
	return atomicfile.Write(s.Tmp(), dest, 0o644, write)
}

// WriteFile places bytes at dest atomically.
func (s *Store) WriteFile(dest string, data []byte) error {
	return s.writeAtomic(dest, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// CopyInto places the contents of src at dest atomically. Used for payloads,
// which are large enough that a non-atomic copy would be observable.
func (s *Store) CopyInto(dest, src string) error {
	return s.writeAtomic(dest, func(w io.Writer) error {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}

// ---------- records ----------

func (s *Store) recordPath(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	return filepath.Join(s.State(), slug+".json"), nil
}

func (s *Store) PutRecord(r artifact.Record) error {
	path, err := s.recordPath(r.Slug)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return s.WriteFile(path, data)
}

func (s *Store) Record(slug string) (artifact.Record, bool, error) {
	var r artifact.Record
	path, err := s.recordPath(slug)
	if err != nil {
		return r, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, false, fmt.Errorf("record for %q is unreadable: %w", slug, err)
	}
	r.Slug = slug // the filename is authoritative
	// A record with no platform is an iOS one: nothing else was ever written,
	// and a hand-edited record must not lose its install link over it.
	if r.Platform == "" {
		r.Platform = artifact.IOS
	}
	// Likewise a record with an icon but no name for it predates the name,
	// when every icon was icon.png; spelled out so a JSON reader need not know.
	r.IconName = r.IconFile()
	return r, true, nil
}

// Records returns every published app, newest build first.
func (s *Store) Records() ([]artifact.Record, error) {
	entries, err := os.ReadDir(s.State())
	if err != nil {
		return nil, err
	}
	// Never nil. An empty store is [] in JSON, not null, so a caller counting
	// apps need not special-case the first run.
	out := []artifact.Record{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".json")
		r, ok, err := s.Record(slug)
		if err != nil || !ok {
			continue // a corrupt record must not hide the healthy ones
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuiltAt.After(out[j].BuiltAt) })
	return out, nil
}

// Remove deletes an app. The slug check is not optional here. This calls
// RemoveAll, and an unvalidated slug of ".." would take the entire store with it.
func (s *Store) Remove(slug string) error {
	if err := ValidateSlug(slug); err != nil {
		return err
	}
	if err := os.RemoveAll(s.AppDir(slug)); err != nil {
		return err
	}
	path, err := s.recordPath(slug)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(s.BuildDir(slug)); err != nil {
		return err
	}
	return s.ClearBuilding(slug)
}

// ---------- build markers ----------

func (s *Store) buildingPath(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	return filepath.Join(s.State(), slug+".building"), nil
}

// ClaimBuilding creates the marker only if none exists, so two publishes of one
// slug cannot both believe they hold it. An existing marker comes back with
// claimed=false and nothing is written, leaving the caller to decide whether its owner is
// alive. O_EXCL makes the check and the write a single operation. A read followed
// by a write would let both pass the read, and the first to finish would
// clear the second's marker and offer a half-built app as installable.
func (s *Store) ClaimBuilding(b artifact.Building) (existing artifact.Building, claimed bool, err error) {
	path, err := s.buildingPath(b.Slug)
	if err != nil {
		return existing, false, err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return existing, false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			// Gone between the two calls – the holder just finished. Report it
			// as held anyway. The caller's retry will claim it.
			return existing, false, nil
		}
		_ = json.Unmarshal(raw, &existing)
		existing.Slug = b.Slug
		return existing, false, nil
	}
	if err != nil {
		return existing, false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return existing, false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return existing, false, err
	}
	return existing, true, nil
}

// ClearBuilding must run on failure as well as success, or a crashed build
// leaves an app permanently displayed as in-progress.
func (s *Store) ClearBuilding(slug string) error {
	path, err := s.buildingPath(slug)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) Building() (map[string]artifact.Building, error) {
	entries, err := os.ReadDir(s.State())
	if err != nil {
		return nil, err
	}
	out := map[string]artifact.Building{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".building") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".building")
		data, err := os.ReadFile(filepath.Join(s.State(), e.Name()))
		if err != nil {
			continue
		}
		var b artifact.Building
		if json.Unmarshal(data, &b) == nil {
			b.Slug = slug
			out[slug] = b
		}
	}
	return out, nil
}

// PruneStalePayloads removes payloads left behind when an app is renamed, which
// would otherwise accumulate silently inside the served directory. ext is
// what marks a file as a payload, and is the platform's to say.
func (s *Store) PruneStalePayloads(slug, keep, ext string) error {
	return s.pruneAppFiles(slug, keep, func(name string) bool { return strings.HasSuffix(name, ext) })
}

// PruneStaleIcons removes an app's icons other than keep, which is empty when
// the new build ships none. The icon's name carries its format, so a publish
// whose icon changed format, or that has none now, would otherwise leave the
// old one served beside the record that no longer names it.
func (s *Store) PruneStaleIcons(slug, keep string) error {
	return s.pruneAppFiles(slug, keep, func(name string) bool { return strings.HasPrefix(name, "icon.") })
}

// pruneAppFiles removes the files in an app's directory that match, keep
// excepted. A directory that is not there has nothing to prune.
func (s *Store) pruneAppFiles(slug, keep string, match func(name string) bool) error {
	if err := ValidateSlug(slug); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.AppDir(slug))
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if name != keep && match(name) {
			_ = os.Remove(filepath.Join(s.AppDir(slug), name))
		}
	}
	return nil
}
