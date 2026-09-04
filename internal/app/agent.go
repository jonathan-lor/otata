package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// agentSpec is what a supervisor's unit says it runs: the definition otata
// writes and reads back. One unit exists per user, and it embeds the root,
// port and serve path it was installed with.
type agentSpec struct {
	Program   string
	Root      string
	Port      int
	ServePath string
	Log       string
}

// agentMatches reports whether an installed agent serves this root and port and both must match exactly.
// Every plist this binary writes embeds both, so one missing either is hand-made or foreign.
func agentMatches(spec agentSpec, root string, port int) bool {
	return agentRootDigest(spec) == rootDigest(root) && spec.Port == port
}

// agentRootDigest identifies the store an installed agent serves. A plist
// with no OTATA_ROOT runs against the default root, because that is what the
// binary defaults to.
func agentRootDigest(spec agentSpec) string {
	root := spec.Root
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".otata")
	}
	return rootDigest(root)
}

/*
disabledIn reads `launchctl print-disabled` output and reports whether
label is disabled there. It's the state the System Settings Login Items toggle
and `launchctl disable` record.

It's split from the fetching just so the parse is testable.
Lines read `"label" => disabled` on current macOS and `"label" => true` on older ones.
Both mean disabled, and a label absent from the list is enabled.
*/
func disabledIn(out, label string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		rest, found := strings.CutPrefix(line, `"`+label+`"`)
		if !found {
			continue
		}
		return strings.Contains(rest, "disabled") || strings.Contains(rest, "true")
	}
	return false
}

// filesDiffer reports whether two binaries differ in content, as cheaply as
// it can be sure. One file under two names (a symlink beside its target) is
// no difference; different sizes are; the same size and modification time is
// the same content, which the staged copy keeps true by carrying its
// source's time; only what is left is settled by hashing both. Hashing two
// binaries on every `otata status` was most of what status cost.
func filesDiffer(a, b string) bool {
	if a == b {
		return false
	}
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA != nil || errB != nil {
		// A missing or unreadable file has no content to match; the
		// digest of nothing differs from any binary's, as before.
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

// describeAgent names what an agent serves, for a status line or a refusal.
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
