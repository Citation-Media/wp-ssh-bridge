package version

import "fmt"

var (
	Version = "0.2.5"
	Commit  = "local"
	Date    = "unknown"
)

// String returns the complete build identity used by the CLI and release assets.
func String() string {
	return fmt.Sprintf("ddev-wp-ssh %s (%s, %s)", Version, Commit, Date)
}
