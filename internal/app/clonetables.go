package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	createTableStatement = regexp.MustCompile("^CREATE TABLE (?:IF NOT EXISTS )?`((?:[^`]|``)+)`")
	multisiteBlogPrefix  = regexp.MustCompile(`^[0-9]+_$`)
)

// createTableProbe is enough of a line to read a CREATE TABLE name; the rest of long
// INSERT lines is never buffered.
const createTableProbe = 256

// createTableCollector records the tables a dump creates while it streams to disk.
type createTableCollector struct {
	line   []byte
	tables []string
}

func (c *createTableCollector) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		chunk := p
		if end >= 0 {
			chunk = p[:end]
		}
		if room := createTableProbe - len(c.line); room > 0 {
			c.line = append(c.line, chunk[:min(room, len(chunk))]...)
		}
		if end < 0 {
			break
		}
		c.flush()
		p = p[end+1:]
	}
	return written, nil
}

func (c *createTableCollector) flush() {
	if match := createTableStatement.FindSubmatch(c.line); match != nil {
		c.tables = append(c.tables, strings.ReplaceAll(string(match[1]), "``", "`"))
	}
	c.line = c.line[:0]
}

// cloneTarget is what the target's wp-config.php said before the clone replaced it.
type cloneTarget struct {
	prefix string
	dbName string
	dbHost string
}

func readCloneTarget(projectRoot string, cfg Config) cloneTarget {
	contents, err := os.ReadFile(filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php"))
	if err != nil {
		return cloneTarget{}
	}
	return cloneTarget{
		prefix: wpConfigTablePrefixLiteral(string(contents)),
		dbName: wpConfigDefineLiteral(string(contents), "DB_NAME"),
		dbHost: wpConfigDefineLiteral(string(contents), "DB_HOST"),
	}
}

// sameDatabase only matches literal values, so an unreadable config never lets the clone
// treat another database's tables as the previous installation.
func (target cloneTarget) sameDatabase(other cloneTarget) bool {
	return target.dbName != "" && target.dbHost != "" && target.dbName == other.dbName && strings.EqualFold(target.dbHost, other.dbHost)
}

func wpConfigDefineLiteral(contents string, name string) string {
	pattern := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*['"]([^'"\\]*)['"]\s*\)\s*;`)
	match := pattern.FindStringSubmatch(contents)
	if match == nil {
		return ""
	}
	return match[1]
}

// replaceCloneTables makes the imported database the target's only WordPress install.
// It drops the tables a previous installation left behind, and with --db-prefix renames
// the imported tables and prefix-derived keys to the configured prefix. Tables under
// other prefixes, including other WordPress installs sharing the database, stay.
func (a *App) replaceCloneTables(ctx context.Context, projectRoot string, cfg Config, previous cloneTarget, dumpTables []string) error {
	sourcePrefix, err := readPullSourceTablePrefix(projectRoot)
	if err != nil {
		return err
	}
	if sourcePrefix == "" || len(dumpTables) == 0 {
		a.UI.Warning("Could not determine the cloned tables; tables of a previous installation in the target database are left in place.")
		return nil
	}
	finalPrefix := defaultString(cfg.CloneDBPrefix, sourcePrefix)
	scopes := []string{finalPrefix}
	if previous.prefix != "" && previous.prefix != finalPrefix && previous.sameDatabase(readCloneTarget(projectRoot, cfg)) {
		scopes = append(scopes, previous.prefix)
	}

	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "db", "query", "SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'", "--skip-column-names")
	if err != nil {
		return fmt.Errorf("list target database tables: %w", err)
	}
	existing := firstColumn(output)
	imported := map[string]bool{}
	for _, table := range dumpTables {
		imported[table] = true
	}

	renames := [][2]string{}
	if finalPrefix != sourcePrefix {
		for _, table := range installationTables(dumpTables, sourcePrefix) {
			renamed := finalPrefix + strings.TrimPrefix(table, sourcePrefix)
			if imported[renamed] {
				return fmt.Errorf("cannot rename %s to %s for --db-prefix: the cloned database already contains %s", table, renamed, renamed)
			}
			renames = append(renames, [2]string{table, renamed})
		}
	}

	leftoverSet := map[string]bool{}
	for _, scope := range scopes {
		for _, table := range installationTables(existing, scope) {
			if !imported[table] {
				leftoverSet[table] = true
			}
		}
	}
	leftovers := make([]string, 0, len(leftoverSet))
	for table := range leftoverSet {
		leftovers = append(leftovers, table)
	}
	sort.Strings(leftovers)

	// Leftovers go first, because a previous installation may hold the names the
	// renamed tables need.
	if len(leftovers) > 0 {
		title := fmt.Sprintf("Removing %d %s of the previous installation (%s)", len(leftovers), pluralNoun(len(leftovers), "table"), strings.Join(scopes, ", "))
		if err := a.runStep(title, "Previous installation tables removed", func() error {
			// Plugin tables may reference each other through foreign keys.
			return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "db", "query", "SET FOREIGN_KEY_CHECKS = 0; DROP TABLE "+quoteIdentifiers(leftovers))
		}); err != nil {
			return err
		}
	}
	if len(renames) == 0 {
		return nil
	}
	title := fmt.Sprintf("Renaming cloned tables: %s -> %s", sourcePrefix, finalPrefix)
	return a.runStep(title, "Cloned tables renamed to "+finalPrefix, func() error {
		for _, query := range renameQueries(renames, sourcePrefix, finalPrefix) {
			if err := a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "db", "query", query); err != nil {
				return err
			}
		}
		return nil
	})
}

// renameQueries moves the tables and the keys WordPress derives from the prefix: the
// user_roles option of every site and all user meta keys, which include user options
// written through update_user_option().
func renameQueries(renames [][2]string, sourcePrefix string, finalPrefix string) []string {
	pairs := make([]string, 0, len(renames))
	queries := []string{}
	for _, rename := range renames {
		pairs = append(pairs, quoteIdentifier(rename[0])+" TO "+quoteIdentifier(rename[1]))
		suffix := strings.TrimPrefix(rename[1], finalPrefix)
		if blog, ok := strings.CutSuffix(suffix, "options"); ok && (blog == "" || multisiteBlogPrefix.MatchString(blog)) {
			queries = append(queries, fmt.Sprintf("UPDATE %s SET option_name = %s WHERE option_name = %s",
				quoteIdentifier(rename[1]), sqlQuote(finalPrefix+blog+"user_roles"), sqlQuote(sourcePrefix+blog+"user_roles")))
		}
		if suffix == "usermeta" {
			queries = append(queries, fmt.Sprintf("UPDATE %s SET meta_key = CONCAT(%s, SUBSTRING(meta_key, %d)) WHERE BINARY LEFT(meta_key, %d) = %s",
				quoteIdentifier(rename[1]), sqlQuote(finalPrefix), len(sourcePrefix)+1, len(sourcePrefix), sqlQuote(sourcePrefix)))
		}
	}
	return append([]string{"RENAME TABLE " + strings.Join(pairs, ", ")}, queries...)
}

// installationTables returns the tables of the WordPress install using prefix. A longer
// prefix with its own options table is another install sharing the database, unless it
// is a multisite blog prefix such as wp_2_.
func installationTables(tables []string, prefix string) []string {
	others := []string{}
	for _, table := range tables {
		other, ok := strings.CutSuffix(table, "options")
		if !ok || other == prefix || !strings.HasPrefix(other, prefix) || multisiteBlogPrefix.MatchString(strings.TrimPrefix(other, prefix)) {
			continue
		}
		others = append(others, other)
	}
	owned := []string{}
	for _, table := range tables {
		if !strings.HasPrefix(table, prefix) || hasAnyPrefix(table, others) {
			continue
		}
		owned = append(owned, table)
	}
	return owned
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func firstColumn(output string) []string {
	values := []string{}
	for _, line := range strings.Split(output, "\n") {
		value, _, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func quoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func quoteIdentifiers(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, quoteIdentifier(name))
	}
	return strings.Join(quoted, ", ")
}
