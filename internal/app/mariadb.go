package app

import (
	"fmt"
	"strings"
)

// remoteMariaDBCompatibilityCommands builds a temporary remote PATH shim and its
// cleanup command so WP-CLI can use the legacy MariaDB client names safely.
func remoteMariaDBCompatibilityCommands(remoteTmp string, id string, required bool) (string, string) {
	if !required {
		return "", ":"
	}

	dir := fmt.Sprintf("%s/.wp-ssh-bridge-mariadb-%s", trimTrailingSlash(remoteTmp), id)
	setup := strings.Join([]string{
		"WP_SSH_MARIADB_COMPAT_DIR=" + shellQuote(dir) + "; WP_SSH_MARIADB_COMPAT_CREATED=0;",
		"mkdir -m 700 \"$WP_SSH_MARIADB_COMPAT_DIR\"; WP_SSH_MARIADB_COMPAT_CREATED=1;",
		"if ! command -v mariadb >/dev/null 2>&1; then ln -sf \"$(command -v mysql)\" \"$WP_SSH_MARIADB_COMPAT_DIR/mariadb\"; fi;",
		"if ! command -v mariadb-dump >/dev/null 2>&1 && command -v mysqldump >/dev/null 2>&1; then ln -sf \"$(command -v mysqldump)\" \"$WP_SSH_MARIADB_COMPAT_DIR/mariadb-dump\"; fi;",
		"PATH=\"$WP_SSH_MARIADB_COMPAT_DIR:$PATH\"; export PATH;",
	}, " ")
	cleanup := strings.Join([]string{
		"if [ \"${WP_SSH_MARIADB_COMPAT_CREATED:-0}\" = 1 ]; then",
		"rm -f " + shellQuote(dir+"/mariadb") + " " + shellQuote(dir+"/mariadb-dump") + " || true;",
		"rmdir " + shellQuote(dir) + " 2>/dev/null || true;",
		"fi",
	}, " ")
	return setup, cleanup
}
