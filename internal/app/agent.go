package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// agentSpec is what a unit runs, as otata writes it and reads it back.
type agentSpec struct {
	Program string
	Root    string
	Port    int
	Log     string
}

// agentMatches reports whether spec serves this root and port. otata always
// writes both, so a unit without a port matches nothing.
func agentMatches(spec agentSpec, root string, port int) bool {
	return agentRootDigest(spec) == rootDigest(root) && spec.Port == port
}

// agentRootDigest is the digest of the root spec serves, the default one if unset.
func agentRootDigest(spec agentSpec) string {
	root := spec.Root
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".otata")
	}
	return rootDigest(root)
}

// filesDiffer reports whether two binaries differ, hashing only when the same
// file, size and mtime checks can't settle it.
func filesDiffer(a, b string) bool {
	if a == b {
		return false
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA != nil || errB != nil {
		// An unreadable file hashes to "", which no binary does.
		return fileDigest(a) != fileDigest(b)
	}
	if os.SameFile(ia, ib) {
		return false
	}
	if ia.Size() != ib.Size() {
		return true
	}
	if ia.ModTime().Equal(ib.ModTime()) {
		return false
	}
	return fileDigest(a) != fileDigest(b)
}

func fileDigest(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// describeAgent names the root and port spec serves.
func describeAgent(spec agentSpec) string {
	root := spec.Root
	if root == "" {
		root = "the default root"
	}
	if spec.Port == 0 {
		return root
	}
	return fmt.Sprintf("%s on port %d", root, spec.Port)
}
