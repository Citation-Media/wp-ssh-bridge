package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var multisiteBlogPrefix = regexp.MustCompile(`^[0-9]+_$`)

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

// planCloneTables makes the imported database the target's only WordPress installation
// without touching anything else in a shared database. By default only the tables of
// the previous installation, found through the target's previous wp-config.php, may be
// overwritten or dropped; db_reset database widens that to the whole target database,
// and none only counts the leftovers. The plan refuses before the import when the dump
// or a --db-prefix rename would overwrite any other table.
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
	existingSet := foldSet(existing)
	keep := cfg.DBReset == dbResetNone
	// A clone always runs standalone, so the default scope is the previous installation.
	var owned []string
	switch {
	case dbResetScope(cfg.DBReset, modeStandalone) == dbResetDatabase:
		owned = existing
	case previous.prefix != "" && previous.sameDatabase(current):
		owned = installationTables(existing, previous.prefix)
	}
	ownedSet := foldSet(owned)
	imported := foldSet(dumpTables)

	written := append([]string{}, dumpTables...)
	if !strings.EqualFold(plan.finalPrefix, sourcePrefix) {
		for _, table := range installationTables(dumpTables, sourcePrefix) {
			renamed := plan.finalPrefix + table[len(sourcePrefix):]
			if imported[strings.ToLower(renamed)] {
				return tablePlan{}, fmt.Errorf("cannot rename %s to %s for clone_db_prefix (--db-prefix): the source database also contains %s", table, renamed, renamed)
			}
			if keep && existingSet[strings.ToLower(renamed)] {
				return tablePlan{}, fmt.Errorf("cannot rename %s to %s for clone_db_prefix (--db-prefix): db_reset none keeps the existing %s", table, renamed, renamed)
			}
			plan.renames = append(plan.renames, tableRename{from: table, to: renamed})
			written = append(written, renamed)
		}
	}
	for _, table := range written {
		key := strings.ToLower(table)
		if existingSet[key] && !ownedSet[key] {
			return tablePlan{}, fmt.Errorf("the target database already contains %s, which does not belong to the WordPress installation at the target; the clone would overwrite it. Remove it or clone into another database", table)
		}
	}

	plan.setLeftovers(owned, imported, keep)
	for _, table := range installationTables(existing, plan.finalPrefix) {
		if !ownedSet[strings.ToLower(table)] && !imported[strings.ToLower(table)] {
			plan.unowned++
		}
	}
	return plan, nil
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
