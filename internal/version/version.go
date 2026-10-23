// Package version holds the single Go-side source of truth for the
// application version. It mirrors wails.json info.productVersion and the
// root package.json version; the release checklist bumps all three together
// and tags the release v<Version>. Nothing else in the Go tree may hardcode
// the application version.
package version

// Version is the current application version (SemVer, without a "v" prefix).
var Version = "0.6.35"
