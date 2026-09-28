package app

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestCreateTableCollectorReadsNamesAcrossChunks(t *testing.T) {
	t.Parallel()
	dump := "-- dump\nDROP TABLE IF EXISTS `wp_options`;\nCREATE TABLE `wp_options` (\n  `option_id` bigint\n);\n" +
		"INSERT INTO `wp_options` VALUES " + strings.Repeat("(1,'x'),", 5000) + "(2,'y');\n" +
		"CREATE TABLE IF NOT EXISTS `wp_odd``name` (\n);\nCREATE TABLE `wp_last` ("
	collector := &createTableCollector{}
	for start := 0; start < len(dump); start += 7 {
		if _, err := collector.Write([]byte(dump[start:min(start+7, len(dump))])); err != nil {
			t.Fatal(err)
		}
	}
	collector.flush()
	want := []string{"wp_options", "wp_odd`name", "wp_last"}
	if !reflect.DeepEqual(collector.tables, want) {
		t.Fatalf("tables = %q, want %q", collector.tables, want)
	}
}

func TestInstallationTablesSeparatesInstallsByTheirUsersTable(t *testing.T) {
	t.Parallel()
	tables := []string{
		"wp_options", "wp_users", "wp_posts",
		"wp_2_options", "wp_2_posts", // multisite blog: no users table of its own
		"wp_2019_options", "wp_2019_users", // a second standalone install
		"wp_myplugin_options", "wp_myplugin_log", // a plugin, not an install
		"WP_Shop_options", "wp_shop_users", // another install, as lower_case_table_names lists it
		"other_options",
	}
	got := installationTables(tables, "wp_")
	want := []string{"wp_options", "wp_users", "wp_posts", "wp_2_options", "wp_2_posts", "wp_myplugin_options", "wp_myplugin_log"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installationTables() = %q, want %q", got, want)
	}
}

func TestRenameQueriesMovePrefixDerivedKeys(t *testing.T) {
	t.Parallel()
	queries := renameQueries([]tableRename{
		{from: "abc_options", to: "wp_options"},
		{from: "abc_2_options", to: "wp_2_options"},
		{from: "abc_usermeta", to: "wp_usermeta"},
		{from: "abc_posts", to: "wp_posts"},
	}, "abc_", "wp_")
	want := []string{
		"RENAME TABLE `abc_options` TO `wp_options`, `abc_2_options` TO `wp_2_options`, `abc_usermeta` TO `wp_usermeta`, `abc_posts` TO `wp_posts`",
		"UPDATE `wp_options` SET option_name = 'wp_user_roles' WHERE option_name = 'abc_user_roles'",
		"UPDATE `wp_2_options` SET option_name = 'wp_2_user_roles' WHERE option_name = 'abc_2_user_roles'",
		"UPDATE `wp_usermeta` SET meta_key = CONCAT('wp_', SUBSTRING(meta_key, 5)) WHERE BINARY LEFT(meta_key, 4) = 'abc_'",
	}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("renameQueries() =\n%s\nwant\n%s", strings.Join(queries, "\n"), strings.Join(want, "\n"))
	}
}

func TestReadCloneTargetUsesResolvedConfig(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "old_", "target_db")
	installFakeTablesWP(t, dir)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	got := app.readCloneTarget(context.Background(), dir, Config{})
	want := cloneTarget{prefix: "old_", dbName: "target_db", dbHost: "localhost"}
	if got != want {
		t.Fatalf("readCloneTarget() = %+v, want %+v", got, want)
	}
	if !got.sameDatabase(cloneTarget{dbName: "target_db", dbHost: "LOCALHOST"}) {
		t.Fatal("same database name and host should match")
	}
	if (cloneTarget{}).sameDatabase(cloneTarget{}) {
		t.Fatal("unknown databases must never match")
	}
	if empty := app.readCloneTarget(context.Background(), t.TempDir(), Config{}); empty != (cloneTarget{}) {
		t.Fatalf("a target without wp-config.php = %+v, want empty", empty)
	}
}

// cloneTables plans against the fake target database and applies the plan like the
// pipeline does around the import.
func cloneTables(t *testing.T, dir string, cfg Config, previous cloneTarget, dump []string) (string, string, error) {
	t.Helper()
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)
	plan, err := app.planCloneTables(context.Background(), dir, cfg, previous, dump)
	if err == nil {
		err = app.applyTablePlan(context.Background(), dir, cfg, plan)
	}
	return readTestLog(t, dir+"/wp.log"), stderr.String(), err
}

func TestCloneDropsOnlyThePreviousInstallation(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "abc_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	installFakeTablesWP(t, dir,
		"old_options", "old_posts", "old_users", "old_2_options",
		"blog_options", "blog_users",
		"abc_orphan",
	)

	previous := cloneTarget{prefix: "old_", dbName: "target_db", dbHost: "localhost"}
	log, stderr, err := cloneTables(t, dir, Config{}, previous, []string{"abc_options", "abc_posts"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	want := "db query SET FOREIGN_KEY_CHECKS = 0; DROP TABLE `old_2_options`, `old_options`, `old_posts`, `old_users`"
	if !strings.Contains(log, want) {
		t.Fatalf("wp log missing %q:\n%s", want, log)
	}
	if strings.Contains(log, "blog_") || strings.Contains(log, "abc_orphan") || strings.Contains(log, "RENAME") {
		t.Fatalf("tables outside the previous installation were touched:\n%s", log)
	}
	if !strings.Contains(stderr, "1 table under the prefix abc_ in the target database is not part of the clone") {
		t.Fatalf("missing warning about the unowned table:\n%s", stderr)
	}
}

func TestCloneKeepsPreviousPrefixInAnotherDatabase(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "abc_", "new_db")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	installFakeTablesWP(t, dir, "old_options", "old_posts", "old_users")

	previous := cloneTarget{prefix: "old_", dbName: "old_db", dbHost: "localhost"}
	log, _, err := cloneTables(t, dir, Config{}, previous, []string{"abc_options"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	if strings.Contains(log, "DROP") {
		t.Fatalf("tables in a database the previous config did not use must stay:\n%s", log)
	}
}

func TestCloneMatchesImportedTablesCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "wp_\n")
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "wp_wfconfig")

	previous := cloneTarget{prefix: "wp_", dbName: "target_db", dbHost: "localhost"}
	log, _, err := cloneTables(t, dir, Config{}, previous, []string{"wp_options", "wp_users", "wp_wfConfig"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	if strings.Contains(log, "DROP") {
		t.Fatalf("an imported table listed in lower case must not be dropped:\n%s", log)
	}
}

func TestCloneRenamesToConfiguredPrefixAfterDroppingThePreviousInstallation(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	installFakeTablesWP(t, dir, "wp_options", "wp_posts", "wp_users")

	previous := cloneTarget{prefix: "wp_", dbName: "target_db", dbHost: "localhost"}
	cfg := Config{CloneDBPrefix: "wp_", CloneDBName: "target_db", CloneDBHost: "localhost"}
	log, _, err := cloneTables(t, dir, cfg, previous, []string{"abc_options", "abc_usermeta"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	drop := strings.Index(log, "DROP TABLE `wp_options`, `wp_posts`, `wp_users`")
	rename := strings.Index(log, "RENAME TABLE `abc_options` TO `wp_options`, `abc_usermeta` TO `wp_usermeta`")
	if drop < 0 || rename < 0 || drop > rename {
		t.Fatalf("previous wp_ tables must be dropped before the rename:\n%s", log)
	}
	for _, want := range []string{"option_name = 'wp_user_roles' WHERE option_name = 'abc_user_roles'", "BINARY LEFT(meta_key, 4) = 'abc_'"} {
		if !strings.Contains(log, want) {
			t.Fatalf("wp log missing %q:\n%s", want, log)
		}
	}
}

func TestCloneRefusesBeforeImportWhenAnotherSiteWouldBeOverwritten(t *testing.T) {
	dir := t.TempDir()
	// The target database runs site X under wp_; no config at the target points at it.
	writeTestWPConfig(t, dir, "wp_", "shared_db")
	writeFile(t, pullSourcePrefixPath(dir), "wp_\n")
	installFakeTablesWP(t, dir, "wp_options", "wp_posts", "wp_users")

	cfg := Config{CloneDBPrefix: "site2_"}
	log, _, err := cloneTables(t, dir, cfg, cloneTarget{}, []string{"wp_options", "wp_posts"})
	if err == nil || !strings.Contains(err.Error(), "already contains wp_options") {
		t.Fatalf("error = %v, want a refusal to overwrite wp_options", err)
	}
	if strings.Contains(log, "DROP") || strings.Contains(log, "RENAME") {
		t.Fatalf("nothing may change when the clone refuses:\n%s", log)
	}
}

func TestCloneRefusesRenameCollisionInDump(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	installFakeTablesWP(t, dir)

	_, _, err := cloneTables(t, dir, Config{CloneDBPrefix: "wp_"}, cloneTarget{}, []string{"abc_options", "wp_options"})
	if err == nil || !strings.Contains(err.Error(), "also contains wp_options") {
		t.Fatalf("error = %v, want rename collision", err)
	}
}

func TestCloneRefusesWhenConfigResolvesAnotherDatabase(t *testing.T) {
	dir := t.TempDir()
	// An included file defined the source database first, so the written constants lost.
	writeTestWPConfig(t, dir, "wp_", "source_db")
	writeFile(t, pullSourcePrefixPath(dir), "wp_\n")
	installFakeTablesWP(t, dir)

	_, _, err := cloneTables(t, dir, Config{CloneDBName: "target_db", CloneDBHost: "localhost"}, cloneTarget{}, []string{"wp_options"})
	if err == nil || !strings.Contains(err.Error(), `resolves the database to "source_db"`) {
		t.Fatalf("error = %v, want a database mismatch", err)
	}
}

func TestCloneRefusesDBPrefixWithoutSourcePrefix(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	installFakeTablesWP(t, dir)

	_, _, err := cloneTables(t, dir, Config{CloneDBPrefix: "wp_"}, cloneTarget{}, []string{"abc_options"})
	if err == nil || !strings.Contains(err.Error(), "clone_db_prefix (--db-prefix) needs") {
		t.Fatalf("error = %v, want a missing source prefix error", err)
	}
}

func TestCloneDBResetDatabaseReplacesTheWholeTargetDatabase(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "wp_\n")
	// No previous config points at these tables; db_reset database declares them replaceable.
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "other_app_data")

	log, _, err := cloneTables(t, dir, Config{DBReset: dbResetDatabase}, cloneTarget{}, []string{"wp_options", "wp_posts"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	if !strings.Contains(log, "DROP TABLE `other_app_data`, `wp_users`") {
		t.Fatalf("db_reset database should drop every table the dump lacks:\n%s", log)
	}
}

func TestCloneDBResetNoneKeepsThePreviousInstallation(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "target_db")
	writeFile(t, pullSourcePrefixPath(dir), "wp_\n")
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "wp_old_plugin", "blog_options", "blog_users")

	previous := cloneTarget{prefix: "wp_", dbName: "target_db", dbHost: "localhost"}
	log, stderr, err := cloneTables(t, dir, Config{DBReset: dbResetNone}, previous, []string{"wp_options", "wp_users"})
	if err != nil {
		t.Fatalf("clone tables error = %v", err)
	}
	if strings.Contains(log, "DROP") {
		t.Fatalf("db_reset none must not drop tables:\n%s", log)
	}
	if !strings.Contains(stderr, "1 table the imported database does not contain is kept because db_reset is none") {
		t.Fatalf("missing kept-tables warning:\n%s", stderr)
	}

	// The guard against overwriting foreign tables stays on with none.
	_, _, err = cloneTables(t, dir, Config{DBReset: dbResetNone}, previous, []string{"blog_options"})
	if err == nil || !strings.Contains(err.Error(), "already contains blog_options") {
		t.Fatalf("error = %v, want the overwrite guard", err)
	}
}
