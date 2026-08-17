// Package buildinfo exposes the build-time version metadata injected via
// -ldflags "-X vocat/internal/buildinfo.Version=... -X vocat/internal/buildinfo.BuildTime=...".
// It is imported by the server (to report version through /api/system/info),
// the CLI subcommands (vocat version / update), and the self-updater (to
// compare the running build against a GitHub release).
package buildinfo

// Version is the semantic version of this build. It defaults to the dev
// sentinel when no -ldflags override is supplied.
var Version = "1.1.0"

// BuildTime is the UTC timestamp the binary was built at (RFC3339), or empty
// for a local dev build.
var BuildTime = ""

// Build returns a human-readable version string. When BuildTime is populated
// it appends the timestamp in parentheses.
func Build() string {
	if BuildTime == "" {
		return Version
	}
	return Version + " (" + BuildTime + ")"
}
