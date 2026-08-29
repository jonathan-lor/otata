// Package version reports this binary's own version.
package version

import "runtime/debug"

// String is the module version for a released build and "dev" for a local
// one. Two different local builds both say "dev", so drift between them is
// undetectable. Releases are what the comparison exists for.
func String() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
