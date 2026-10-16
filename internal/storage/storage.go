// Package storage owns the on-disk layout under the otata root.
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

// slugPattern is strict because a slug becomes both a path component and a
// URL segment. It accepts exactly what Slugify produces.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// ErrBadSlug is returned for any slug that is not a single safe path element.
var ErrBadSlug = errors.New("slug must be 1-64 characters of a-z, 0-9 and dashes, starting and ending alphanumeric")

// ValidateSlug guards every path this package builds, so no caller can reach
// outside the root.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("%q: %w", slug, ErrBadSlug)
	}
	return nil
}

// Only Public is served. Tmp stages files on their way into it.
func (s *Store) Public() string { return filepath.Join(s.root, "public") }
func (s *Store) State() string  { return filepath.Join(s.root, "state") }
func (s *Store) Tmp() string    { return filepath.Join(s.root, "tmp") }

// TSNetDir locates private node state without creating the store.
func TSNetDir(root string) string { return filepath.Join(root, "state", "tsnet") }

// AppDir returns "" for an invalid slug, a second line of defense after
// ValidateSlug.
func (s *Store) AppDir(slug string) string {
	if ValidateSlug(slug) != nil {
		return ""
	}
	return filepath.Join(s.Public(), slug)
}

// BuildDir holds a slug's archives, exports and build log, or is "" for an
// invalid slug.
func (s *Store) BuildDir(slug string) string {
	if ValidateSlug(slug) != nil {
		return ""
	}
	return filepath.Join(s.root, "build", slug)
}

// ServerLog holds the background server's own log and its access log.
func (s *Store) ServerLog() string { return filepath.Join(s.root, "server.log") }

// StagedBinary is where otata copies itself when the service manager can't
// run the installed binary.
func (s *Store) StagedBinary() string { return filepath.Join(s.root, "bin", "otata") }

// TmpFile is a scratch path under Tmp. Include the slug in name.
func (s *Store) TmpFile(name string) string { return filepath.Join(s.Tmp(), name) }

// The served files. An app's file path is "" for an invalid slug or a name
// that isn't a bare file name.
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

// writeAtomic stages in Tmp rather than beside dest, where a staged file
// would be served, then renames into place.
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

// CopyInto copies src to dest atomically.
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
	// Records from before Android are iOS.
	if r.Platform == "" {
		r.Platform = artifact.IOS
	}
	// Spell out the icon name older records leave out.
	r.IconName = r.IconFile()
	return r, true, nil
}

// Records returns every published app, newest build first.
func (s *Store) Records() ([]artifact.Record, error) {
	entries, err := os.ReadDir(s.State())
	if err != nil {
		return nil, err
	}
	// Never nil, so an empty store is [] in JSON.
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

// Remove deletes an app. Its slug check matters most, since RemoveAll on ".."
// would take the whole store.
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

func (s *Store) buildingPath(slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	return filepath.Join(s.State(), slug+".building"), nil
}

// ClaimBuilding creates the marker with O_EXCL, so two publishes can't both
// claim it. An existing marker comes back with claimed false.
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
			// The holder just finished. Report it held, and the caller's retry claims it.
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

// ClearBuilding removes the marker. Publish clears it on failure as well as
// success.
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

// PruneStalePayloads removes the app's other payloads with extension ext,
// which a renamed app leaves behind.
func (s *Store) PruneStalePayloads(slug, keep, ext string) error {
	return s.pruneAppFiles(slug, keep, func(name string) bool { return strings.HasSuffix(name, ext) })
}

// PruneStaleIcons removes the app's icons other than keep, which is "" when
// the new build has none.
func (s *Store) PruneStaleIcons(slug, keep string) error {
	return s.pruneAppFiles(slug, keep, func(name string) bool { return strings.HasPrefix(name, "icon.") })
}

// pruneAppFiles removes the app's files that match, except keep.
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
