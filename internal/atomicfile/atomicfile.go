// Package atomicfile writes files by staging and renaming, so a crash
// mid-write can never leave a torn file at the destination.
package atomicfile

import (
	"io"
	"os"
	"path/filepath"
)

// Write stages in stageDir, then renames into place with perm applied.
// stageDir must be on the same filesystem as dest because that's what makes the
// rename atomic instead of a copy. It is a parameter, not always the
// destination's directory, because staging beside a served destination would
// make the half-written file momentarily fetchable. Storage stages in its
// tmp dir for exactly that reason.
func Write(stageDir, dest string, perm os.FileMode, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stageDir, "."+filepath.Base(dest)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, dest)
}

// WriteData is Write for content already in hand.
func WriteData(stageDir, dest string, perm os.FileMode, data []byte) error {
	return Write(stageDir, dest, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
