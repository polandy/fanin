// Package version reports the fanin build version.
package version

// Version is set at build time with -ldflags -X.
var Version = "dev"

// String returns the version line both binaries print.
func String(binary string) string {
	return binary + " " + Version
}
