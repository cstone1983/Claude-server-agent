// Package version holds build metadata, set via -ldflags at release time.
package version

var (
	Version = "dev"
	Commit  = "none"
)

func String() string { return Version + " (" + Commit + ")" }
