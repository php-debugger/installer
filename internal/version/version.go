// Package version holds the installer's build version.
package version

// Version is the installer version. It is "dev" for local builds; release builds
// override it with the release tag via:
//
//	-ldflags "-X github.com/php-debugger/installer/internal/version.Version=<tag>"
var Version = "dev"
