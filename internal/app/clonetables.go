package app

import (
	"bytes"
	"context"
	"encoding/json"
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

// cloneTarget is the database and prefix a wp-config.php resolves to.
type cloneTarget struct {
	prefix string
	dbName string
	dbHost string
}

// readCloneTarget evaluates the target's wp-config.php through WP-CLI, so constants from
// included files or the environment count, not the first literal in the file.
func (a *App) readCloneTarget(ctx context.Context, projectRoot string, cfg Config) cloneTarget {
	if _, err := os.Stat(filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")); err != nil {
		return cloneTarget{}
	}
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "config", "list", "table_prefix", "DB_NAME", "DB_HOST", "--strict", "--format=json")
	if err != nil {
		return cloneTarget{}
	}
	payload, err := jsonPayload(output)
	if err != nil {
		return cloneTarget{}
	}
	var entries []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	}
	if err := json.Unmarshal([]byte(payload), &entries); err != nil {
		return cloneTarget{}
	}
	target := cloneTarget{}
	for _, entry := range entries {
		value, _ := entry.Value.(string)
		switch entry.Name {
		case "table_prefix":
			target.prefix = parseTablePrefix(value)
		case "DB_NAME":
			target.dbName = value
		case "DB_HOST":
			target.dbHost = value
		}
	}
	return target
}

// sameDatabase never matches unknown values, so an unreadable config cannot make the
// clone treat another database's tables as the previous installation.
func (target cloneTarget) sameDatabase(other cloneTarget) bool {
	return target.dbName != "" && target.dbHost != "" && target.dbName == other.dbName && strings.EqualFold(target.dbHost, other.dbHost)
}

type tableRename struct {
	from string
	to   string
}

// tablePlan is decided before the import, while the database still shows which tables
// belong to the installation the import replaces.
type tablePlan struct {
	sourcePrefix string
	finalPrefix  string
	drops        []string
	renames      []tableRename
	unowned      int
}

// planCloneTables makes the imported database the target's only WordPress install
// without touching anything else in a shared database. Only tables of the previous
// installation, found through the target's previous wp-config.php, may be overwritten
// or dropped; the plan refuses before the import when the dump or a --db-prefix rename
// would overwrite any other table.
func (a *App) planCloneTables(ctx context.Context, projectRoot string, cfg Config, previous cloneTarget, dumpTables []string) (tablePlan, error) {
	current := a.readCloneTarget(ctx, projectRoot, cfg)
	if cfg.CloneDBName != "" && (current.dbName != cfg.CloneDBName || !strings.EqualFold(current.dbHost, cfg.CloneDBHost)) {
		return tablePlan{}, fmt.Errorf("wp-config.php resolves the database to %q on %q, not the clone target %q on %q; another file or the environment defines the database constants, so the import would go elsewhere", current.dbName, current.dbHost, cfg.CloneDBName, cfg.CloneDBHost)
	}
	sourcePrefix, err := readPullSourceTablePrefix(projectRoot)
	if err != nil {
		return tablePlan{}, err
	}
	if sourcePrefix == "" || len(dumpTables) == 0 {
		if cfg.CloneDBPrefix != "" {
			return tablePlan{}, fmt.Errorf("could not detect the source table prefix or the cloned tables, which clone_db_prefix (--db-prefix) needs to rename them")
		}
		a.UI.Warning("Could not determine the cloned tables; tables of a previous installation in the target database are left in place.")
		return tablePlan{}, nil
	}
	plan := tablePlan{sourcePrefix: sourcePrefix, finalPrefix: defaultString(cfg.CloneDBPrefix, sourcePrefix)}

	existing, err := a.localTableNames(ctx, projectRoot, cfg)
	if err != nil {
		return tablePlan{}, fmt.Errorf("list target database tables: %w", err)
	}
	owned := map[string]bool{}
	if previous.prefix != "" && previous.sameDatabase(current) {
		owned = foldSet(installationTables(existing, previous.prefix))
	}
	imported := foldSet(dumpTables)

	written := append([]string{}, dumpTables...)
	if !strings.EqualFold(plan.finalPrefix, sourcePrefix) {
		for _, table := range installationTables(dumpTables, sourcePrefix) {
			renamed := plan.finalPrefix + table[len(sourcePrefix):]
			if imported[strings.ToLower(renamed)] {
				return tablePlan{}, fmt.Errorf("cannot rename %s to %s for clone_db_prefix (--db-prefix): the source database also contains %s", table, renamed, renamed)
			}
			plan.renames = append(plan.renames, tableRename{from: table, to: renamed})
			written = append(written, renamed)
		}
	}
	existingSet := foldSet(existing)
	for _, table := range written {
		key := strings.ToLower(table)
		if existingSet[key] && !owned[key] {
			return tablePlan{}, fmt.Errorf("the target database already contains %s, which does not belong to the WordPress installation at the target; the clone would overwrite it. Remove it or clone into another database", table)
		}
	}

	for _, table := range existing {
		if owned[strings.ToLower(table)] && !imported[strings.ToLower(table)] {
			plan.drops = append(plan.drops, table)
		}
	}
	sort.Strings(plan.drops)
	for _, table := range installationTables(existing, plan.finalPrefix) {
		if !owned[strings.ToLower(table)] && !imported[strings.ToLower(table)] {
			plan.unowned++
		}
	}
	return plan, nil
}

// planPullTables decides which local tables the pulled database replaces. DDEV and wp-env
// give each project a database of its own, so everything the dump does not recreate goes,
// as `ddev import-db` does. A standalone project may share its database with other local
// sites, so only the tables of the installation its wp-config.php describes go.
func (a *App) planPullTables(ctx context.Context, projectRoot string, cfg Config, mode runtimeMode, dumpTables []string) tablePlan {
	if len(dumpTables) == 0 {
		return tablePlan{}
	}
	existing, err := a.localTableNames(ctx, projectRoot, cfg)
	if err != nil {
		a.UI.Warning("Could not list the local tables before the import; tables the pulled database does not contain are left in place.")
		return tablePlan{}
	}
	owned := existing
	if mode == modeStandalone {
		prefix := a.localTablePrefix(ctx, projectRoot, cfg)
		if prefix == "" {
			a.UI.Warning("Could not read the local $table_prefix; tables the pulled database does not contain are left in place.")
			return tablePlan{}
		}
		owned = installationTables(existing, prefix)
	}
	imported := foldSet(dumpTables)
	plan := tablePlan{}
	for _, table := range owned {
		if !imported[strings.ToLower(table)] {
			plan.drops = append(plan.drops, table)
		}
	}
	sort.Strings(plan.drops)
	return plan
}

// localInstallationTables lists the tables of the local installation, so a push leaves
// other sites sharing the local database behind. Without a readable prefix or table list
// it returns nothing, and the whole database is exported.
func (a *App) localInstallationTables(ctx context.Context, projectRoot string, cfg Config) []string {
	prefix := a.localTablePrefix(ctx, projectRoot, cfg)
	if prefix == "" {
		return nil
	}
	tables, err := a.localTableNames(ctx, projectRoot, cfg)
	if err != nil {
		return nil
	}
	return installationTables(tables, prefix)
}

// applyTablePlan runs after the import. Leftovers go first, because the previous
// installation may hold the names the renamed tables need.
func (a *App) applyTablePlan(ctx context.Context, projectRoot string, cfg Config, plan tablePlan) error {
	if len(plan.drops) > 0 {
		title := fmt.Sprintf("Removing %d %s of the previous installation", len(plan.drops), pluralNoun(len(plan.drops), "table"))
		if err := a.runStep(title, "Previous installation tables removed", func() error {
			// Plugin tables may reference each other through foreign keys.
			return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "db", "query", "SET FOREIGN_KEY_CHECKS = 0; DROP TABLE "+quoteIdentifiers(plan.drops))
		}); err != nil {
			return err
		}
	}
	if len(plan.renames) > 0 {
		title := fmt.Sprintf("Renaming cloned tables: %s -> %s", plan.sourcePrefix, plan.finalPrefix)
		if err := a.runStep(title, "Cloned tables renamed to "+plan.finalPrefix, func() error {
			for _, query := range renameQueries(plan.renames, plan.sourcePrefix, plan.finalPrefix) {
				if err := a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "db", "query", query); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if plan.unowned > 0 {
		a.UI.Warning("%d %s under the prefix %s in the target database %s not part of the clone and no WordPress config at the target used them; they were left in place.", plan.unowned, pluralNoun(plan.unowned, "table"), plan.finalPrefix, pluralVerb(plan.unowned))
	}
	return nil
}

// renameQueries moves the tables and the keys WordPress derives from the prefix: the
// user_roles option of every site and all user meta keys, which include user options
// written through update_user_option().
func renameQueries(renames []tableRename, sourcePrefix string, finalPrefix string) []string {
	pairs := make([]string, 0, len(renames))
	queries := []string{}
	for _, rename := range renames {
		pairs = append(pairs, quoteIdentifier(rename.from)+" TO "+quoteIdentifier(rename.to))
		suffix := rename.to[len(finalPrefix):]
		if blog, ok := strings.CutSuffix(suffix, "options"); ok && (blog == "" || multisiteBlogPrefix.MatchString(blog)) {
			queries = append(queries, fmt.Sprintf("UPDATE %s SET option_name = %s WHERE option_name = %s",
				quoteIdentifier(rename.to), sqlQuote(finalPrefix+blog+"user_roles"), sqlQuote(sourcePrefix+blog+"user_roles")))
		}
		if suffix == "usermeta" {
			queries = append(queries, fmt.Sprintf("UPDATE %s SET meta_key = CONCAT(%s, SUBSTRING(meta_key, %d)) WHERE BINARY LEFT(meta_key, %d) = %s",
				quoteIdentifier(rename.to), sqlQuote(finalPrefix), len(sourcePrefix)+1, len(sourcePrefix), sqlQuote(sourcePrefix)))
		}
	}
	return append([]string{"RENAME TABLE " + strings.Join(pairs, ", ")}, queries...)
}

// installationTables returns the tables of the WordPress install using prefix. A longer
// prefix with its own options and users tables is another install sharing the database;
// multisite blogs such as wp_2_ have no users table of their own. Names compare
// case-insensitively, as MySQL does with lower_case_table_names.
func installationTables(tables []string, prefix string) []string {
	lowerPrefix := strings.ToLower(prefix)
	names := foldSet(tables)
	others := []string{}
	for name := range names {
		other, ok := strings.CutSuffix(name, "options")
		if ok && other != lowerPrefix && strings.HasPrefix(other, lowerPrefix) && names[other+"users"] {
			others = append(others, other)
		}
	}
	owned := []string{}
	for _, table := range tables {
		name := strings.ToLower(table)
		if strings.HasPrefix(name, lowerPrefix) && !hasAnyPrefix(name, others) {
			owned = append(owned, table)
		}
	}
	return owned
}

// localTableNames lists base tables without loading WordPress, which fails while the
// configured prefix does not match the tables.
func (a *App) localTableNames(ctx context.Context, projectRoot string, cfg Config) ([]string, error) {
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "db", "query", "SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'", "--skip-column-names")
	if err != nil {
		return nil, err
	}
	tables := []string{}
	for _, line := range strings.Split(output, "\n") {
		if table, _, _ := strings.Cut(strings.TrimSpace(line), "\t"); table != "" {
			tables = append(tables, table)
		}
	}
	return tables, nil
}

func foldSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[strings.ToLower(value)] = true
	}
	return set
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func pluralVerb(count int) string {
	if count == 1 {
		return "is"
	}
	return "are"
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
