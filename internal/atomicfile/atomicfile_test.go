package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDataPlacesContentWithPerm(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "sub", "config.json")
	if err := WriteData(dir, dest, 0o600, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteData(dir, dest, 0o600, []byte("second-and-longer")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "second-and-longer" {
		t.Fatalf("read back %q, %v", data, err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("perm = %o, want 600", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("staging dir holds %d entries, want only the destination's directory", len(entries))
	}
}

// A failed write must leave the destination as it was and the staging
// directory empty. Torn or abandoned files are the whole reason to stage.
func TestFailedWriteLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "file")
	if err := WriteData(dir, dest, 0o644, []byte("old")); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err := Write(dir, dest, 0o644, func(io.Writer) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the callback's", err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "old" {
		t.Errorf("destination = %q after a failed write, want the old content", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("staging dir holds %d entries after a failed write, want just the destination", len(entries))
	}
}
