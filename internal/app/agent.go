package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// agentSpec is what a LaunchAgent plist says it runs. One agent exists per
// user, under one label, and it embeds the root and port it was installed with.
type agentSpec struct {
	Program string
	Root    string
	Port    int
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
