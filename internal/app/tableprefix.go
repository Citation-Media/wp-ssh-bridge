package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	validTablePrefix      = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	wpConfigPrefixLiteral = regexp.MustCompile(`(?m)^[ \t]*\$table_prefix\s*=\s*['"]([A-Za-z0-9_]+)['"]\s*;`)
)

// tablePrefixMarker tags the source prefix in the export output, which also carries
// WP-CLI success and download notices.
const tablePrefixMarker = "wp-ssh-table-prefix="

// remoteTablePrefixCommand prints the source prefix during the export, so the dump and
// the recorded prefix come from one SSH session. A failed lookup must not abort it.
var remoteTablePrefixCommand = "printf '" + tablePrefixMarker + "%s\\n' \"$(wp_ssh_wp --allow-root --skip-plugins --skip-themes config get table_prefix 2>/dev/null || true)\";"

// pullSourcePrefixPath stores the pull source table prefix between db-pull and
// post-pull, which DDEV runs as separate provider commands around its own import.
func pullSourcePrefixPath(projectRoot string) string {
	return filepath.Join(downloadsDir(projectRoot), "db-prefix")
}

// recordPullSourceTablePrefix remembers the source $table_prefix from the export output
// so post-pull can point the local wp-config.php at the imported tables.
func (a *App) recordPullSourceTablePrefix(projectRoot string, exportOutput string) error {
	path := pullSourcePrefixPath(projectRoot)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	prefix := ""
	for _, line := range strings.Split(exportOutput, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), tablePrefixMarker); ok {
			prefix = parseTablePrefix(value)
		}
	}
	if prefix == "" {
		a.UI.Warning("Could not detect the pull source table prefix; the local $table_prefix is left unchanged.")
		return nil
	}
	return os.WriteFile(path, []byte(prefix+"\n"), 0o600)
}

// alignPulledTablePrefix applies the prefix recorded by db-pull and clears it, so a
// later files-only pull cannot reuse it. An explicit clone --db-prefix names the target
// prefix on purpose and stays as set.
func (a *App) alignPulledTablePrefix(ctx context.Context, projectRoot string, cfg Config, clone bool) error {
	path := pullSourcePrefixPath(projectRoot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sourcePrefix := parseTablePrefix(string(data))
	if sourcePrefix == "" || (clone && cfg.CloneDBPrefix != "") {
		return nil
	}
	return a.alignLocalTablePrefix(ctx, projectRoot, cfg, sourcePrefix)
}

// alignLocalTablePrefix points the local wp-config.php at the tables a pulled database
// created. The dump carries the source prefix in table names and in prefixed option
// and user meta keys, so the local config follows the source instead of renaming
// tables, which also keeps a later push consistent with the source.
func (a *App) alignLocalTablePrefix(ctx context.Context, projectRoot string, cfg Config, sourcePrefix string) error {
	wpConfig := filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")
	contents, err := os.ReadFile(wpConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A matching literal settles the common case without starting WP-CLI, which is a
	// container exec in DDEV and wp-env.
	literal := wpConfigTablePrefixLiteral(string(contents))
	if literal == sourcePrefix {
		return nil
	}
	localPrefix := a.localTablePrefix(ctx, projectRoot, cfg)
	if localPrefix == sourcePrefix {
		return nil
	}

	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "db", "query", "SHOW TABLES", "--skip-column-names")
	if err != nil {
		a.warnSetTablePrefix("Could not list local database tables to check the table prefix", sourcePrefix)
		return nil
	}
	// Without the imported options table the dump was not imported (for example with
	// --skip-import), and switching the prefix would point WordPress at missing tables.
	if !lineSetContains(output, sourcePrefix+"options") {
		return nil
	}
	if literal != "" && localPrefix != "" && literal != localPrefix {
		a.warnSetTablePrefix(fmt.Sprintf("Another file overrides the $table_prefix in %s", wpConfig), sourcePrefix)
		return nil
	}
	updated, replaced := replaceWPConfigTablePrefix(string(contents), phpStringLiteral(sourcePrefix))
	if !replaced {
		a.warnSetTablePrefix(fmt.Sprintf("%s has no $table_prefix assignment", wpConfig), sourcePrefix)
		return nil
	}
	title := fmt.Sprintf("Updating local table prefix: %s -> %s", defaultString(localPrefix, "unknown"), sourcePrefix)
	if err := a.runStep(title, "Local table prefix set to "+sourcePrefix, func() error {
		return os.WriteFile(wpConfig, []byte(updated), 0o644)
	}); err != nil {
		return err
	}

	if stale := countTablesWithPrefix(output, localPrefix, sourcePrefix); stale > 0 {
		a.UI.Warning("%d local %s with the previous prefix %s remain in the database; WordPress no longer uses them.", stale, pluralNoun(stale, "table"), localPrefix)
	}
	return nil
}

func (a *App) warnSetTablePrefix(reason string, prefix string) {
	a.UI.Warning("%s; the pulled database uses table prefix %s, so set $table_prefix = '%s'; where the local config defines it.", reason, prefix, prefix)
}

// localTablePrefix reads $table_prefix without loading WordPress, which fails while the
// configured prefix does not match the imported tables.
func (a *App) localTablePrefix(ctx context.Context, projectRoot string, cfg Config) string {
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "config", "get", "table_prefix")
	if err != nil {
		return ""
	}
	return parseTablePrefix(output)
}

// wpConfigTablePrefixLiteral returns a plain quoted $table_prefix value; expressions
// such as wp-env's getenv_docker() call need WP-CLI to resolve.
func wpConfigTablePrefixLiteral(contents string) string {
	match := wpConfigPrefixLiteral.FindStringSubmatch(contents)
	if match == nil {
		return ""
	}
	return match[1]
}

// parseTablePrefix takes the last output line, skipping notices such as the WP-CLI
// download message, and rejects anything that is not a plain table prefix.
func parseTablePrefix(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	prefix := strings.TrimSpace(lines[len(lines)-1])
	if !validTablePrefix.MatchString(prefix) {
		return ""
	}
	return prefix
}

// countTablesWithPrefix counts tables left under the previous prefix. Tables that also
// match the new prefix are skipped, because one prefix can start with the other.
func countTablesWithPrefix(output string, previousPrefix string, currentPrefix string) int {
	if previousPrefix == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(output, "\n") {
		table := strings.TrimSpace(line)
		if strings.HasPrefix(table, previousPrefix) && !strings.HasPrefix(table, currentPrefix) {
			count++
		}
	}
	return count
}
