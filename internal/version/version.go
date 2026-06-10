package version

import "fmt"

var (
	Version = "0.3.4"
	Commit  = "local"
	Date    = "unknown"
)

// String returns the complete build identity used by the CLI and release assets.
func String() string {
	return fmt.Sprintf("wp-ssh-bridge %s (%s, %s)", Version, Commit, Date)
}
