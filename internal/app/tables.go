package app

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var createTableStatement = regexp.MustCompile("^CREATE TABLE (?:IF NOT EXISTS )?`((?:[^`]|``)+)`")

// baseTablesQuery lists tables without views and runs without loading WordPress, which
// fails while the configured prefix does not match the tables.
const baseTablesQuery = "SHOW FULL TABLES WHERE Table_type = 'BASE TABLE'"

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

// installationTables returns the tables of the WordPress installation using prefix. A
// longer prefix with its own options and users tables is another installation sharing
// the database; multisite blogs such as wp_2_ have no users table of their own. Names
// compare case-insensitively, as MySQL does with lower_case_table_names.
// installationTablesAwk applies the same rule on the source host; change both together.
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

// installationTablesAwk is installationTables for the source host: it reads the output of
// baseTablesQuery and prints the installation's tables for prefix p, comma-separated for
// db export --tables.
const installationTablesAwk = `{ t = $1; if (t == "") next; n[++c] = t; s[tolower(t)] = 1 } END { lp = tolower(p); for (i = 1; i <= c; i++) { l = tolower(n[i]); if (length(l) > 7 && substr(l, length(l) - 6) == "options") { st = substr(l, 1, length(l) - 7); if (st != lp && index(st, lp) == 1 && ((st "users") in s)) o[st] = 1 } } sep = ""; for (i = 1; i <= c; i++) { l = tolower(n[i]); if (index(l, lp) != 1) continue; skip = 0; for (x in o) if (index(l, x) == 1) skip = 1; if (!skip) { printf "%s%s", sep, n[i]; sep = "," } } }`

// localTableNames lists the local database's base tables.
func (a *App) localTableNames(ctx context.Context, projectRoot string, cfg Config) ([]string, error) {
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "db", "query", baseTablesQuery, "--skip-column-names")
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

// dbResetScope resolves db_reset to the tables an import may replace. Without a value,
// and for none, which only reports, DDEV and wp-env use the whole project database and a
// standalone project, whose database other sites may share, only its installation.
func dbResetScope(reset string, mode runtimeMode) string {
	if reset == dbResetDatabase || reset == dbResetInstallation {
		return reset
	}
	if mode == modeStandalone {
		return dbResetInstallation
	}
	return dbResetDatabase
}

type tableRename struct {
	from string
	to   string
}

// tablePlan is decided before the import, while the database still shows which tables
// belong to the installation the import replaces, and applied after it succeeded.
type tablePlan struct {
	sourcePrefix string
	finalPrefix  string
	// drops are replaced tables the imported database does not contain.
	drops   []string
	renames []tableRename
	// kept counts those tables instead of dropping them, because db_reset is none.
	kept int
	// unowned counts tables under the clone's prefix that belong to no known
	// installation, so the clone leaves them in place.
	unowned int
}

// setLeftovers records the replaceable tables the import does not recreate; with
// db_reset none they are only counted.
func (plan *tablePlan) setLeftovers(owned []string, imported map[string]bool, keep bool) {
	leftovers := []string{}
	for _, table := range owned {
		if !imported[strings.ToLower(table)] {
			leftovers = append(leftovers, table)
		}
	}
	sort.Strings(leftovers)
	if keep {
		plan.kept = len(leftovers)
		return
	}
	plan.drops = leftovers
}

// planPullTables decides which local tables the pulled database replaces, following
// db_reset: by default the whole project database in DDEV and wp-env, as `ddev
// import-db` does, and only the local installation in a standalone project. localPrefix
// is the installation's prefix, read before the file sync could replace wp-config.php.
func (a *App) planPullTables(ctx context.Context, projectRoot string, cfg Config, mode runtimeMode, localPrefix string, dumpTables []string) tablePlan {
	if len(dumpTables) == 0 {
		return tablePlan{}
	}
	existing, err := a.localTableNames(ctx, projectRoot, cfg)
	if err != nil {
		a.UI.Warning("Could not list the local tables before the import; tables the pulled database does not contain are left in place.")
		return tablePlan{}
	}
	owned := existing
	if dbResetScope(cfg.DBReset, mode) == dbResetInstallation {
		if localPrefix == "" {
			a.UI.Warning("Could not read the local $table_prefix; tables the pulled database does not contain are left in place.")
			return tablePlan{}
		}
		owned = installationTables(existing, localPrefix)
	}
	plan := tablePlan{}
	plan.setLeftovers(owned, foldSet(dumpTables), cfg.DBReset == dbResetNone)
	return plan
}

// applyTablePlan runs after the import. Leftovers go first, because the previous
// installation may hold the names the renamed tables need.
func (a *App) applyTablePlan(ctx context.Context, projectRoot string, cfg Config, plan tablePlan) error {
	if len(plan.drops) > 0 {
		title := fmt.Sprintf("Removing %d %s the imported database does not contain", len(plan.drops), pluralNoun(len(plan.drops), "table"))
		if err := a.runStep(title, "Leftover tables removed", func() error {
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
	if plan.kept > 0 {
		a.UI.Warning("%d %s the imported database does not contain %s kept because db_reset is none.", plan.kept, pluralNoun(plan.kept, "table"), pluralVerb(plan.kept))
	}
	if plan.unowned > 0 {
		a.UI.Warning("%d %s under the prefix %s in the target database %s not part of the clone and no WordPress config at the target used them; they were left in place.", plan.unowned, pluralNoun(plan.unowned, "table"), plan.finalPrefix, pluralVerb(plan.unowned))
	}
	return nil
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
