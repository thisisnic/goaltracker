// Package version reports the build's version.
package version

import "runtime/debug"

// Version is set by the release build with -ldflags "-X ...". When it is
// empty, the module version recorded by `go install pkg@vX.Y.Z` is used,
// and failing that "dev".
var Version = ""

// String returns the version to show the user.
func String() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
