package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func TestInstallationTablesSkipsOtherInstallsButKeepsMultisiteBlogs(t *testing.T) {
	t.Parallel()
	tables := []string{"wp_options", "wp_posts", "wp_2_options", "wp_2_posts", "wp_shop_options", "wp_shop_posts", "wp_wc_orders", "other_options"}
	got := installationTables(tables, "wp_")
	want := []string{"wp_options", "wp_posts", "wp_2_options", "wp_2_posts", "wp_wc_orders"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installationTables() = %q, want %q", got, want)
	}
}

func TestRenameQueriesMovePrefixDerivedKeys(t *testing.T) {
	t.Parallel()
	queries := renameQueries([][2]string{
		{"abc_options", "wp_options"},
		{"abc_2_options", "wp_2_options"},
		{"abc_usermeta", "wp_usermeta"},
		{"abc_posts", "wp_posts"},
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

func TestReadCloneTargetParsesLiteralConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "wp-config.php"), "<?php\ndefine( 'DB_NAME', 'target_db' );\ndefine('DB_HOST', \"db.example.com\");\n$table_prefix = 'old_';\n")
	got := readCloneTarget(dir, Config{})
	want := cloneTarget{prefix: "old_", dbName: "target_db", dbHost: "db.example.com"}
	if got != want {
		t.Fatalf("readCloneTarget() = %+v, want %+v", got, want)
	}
	if got.sameDatabase(cloneTarget{dbName: "target_db", dbHost: "DB.example.com"}) != true {
		t.Fatal("same database name and host should match")
	}
	if (cloneTarget{}).sameDatabase(cloneTarget{}) {
		t.Fatal("unknown databases must never match")
	}
}

// installFakeCloneWP fakes a target WP-CLI that lists tables and logs every query.
func installFakeCloneWP(t *testing.T, dir string, tables []string) string {
	t.Helper()
	logPath := filepath.Join(dir, "wp.log")
	listing := ""
	for _, table := range tables {
		listing += table + `\tBASE TABLE\n`
	}
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
case "$*" in
  *"SHOW FULL TABLES"*) printf '`+listing+`'; exit 0 ;;
  *"db query"*) exit 0 ;;
esac
exit 1
`)
	return logPath
}

func writeCloneTargetConfig(t *testing.T, dir string, dbName string, prefix string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "wp-config.php"), "<?php\ndefine('DB_NAME', '"+dbName+"');\ndefine('DB_HOST', 'localhost');\n$table_prefix = '"+prefix+"';\n")
}

func TestReplaceCloneTablesDropsPreviousInstallationOnly(t *testing.T) {
	dir := t.TempDir()
	writeCloneTargetConfig(t, dir, "target_db", "abc_")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	logPath := installFakeCloneWP(t, dir, []string{
		"abc_options", "abc_posts", "abc_old_plugin",
		"old_options", "old_posts", "old_2_options",
		"blog_options", "blog_posts",
	})
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	previous := cloneTarget{prefix: "old_", dbName: "target_db", dbHost: "localhost"}
	if err := app.replaceCloneTables(context.Background(), dir, Config{}, previous, []string{"abc_options", "abc_posts"}); err != nil {
		t.Fatalf("replaceCloneTables() error = %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "db query SET FOREIGN_KEY_CHECKS = 0; DROP TABLE `abc_old_plugin`, `old_2_options`, `old_options`, `old_posts`"
	if !strings.Contains(string(log), want) {
		t.Fatalf("wp log missing %q:\n%s", want, log)
	}
	if strings.Contains(string(log), "blog_") || strings.Contains(string(log), "RENAME") {
		t.Fatalf("unrelated tables or renames touched:\n%s", log)
	}
	if _, err := os.Stat(pullSourcePrefixPath(dir)); err != nil {
		t.Fatalf("the prefix record must stay for post-pull: %v", err)
	}
}

func TestReplaceCloneTablesKeepsPreviousPrefixInAnotherDatabase(t *testing.T) {
	dir := t.TempDir()
	writeCloneTargetConfig(t, dir, "new_db", "abc_")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	logPath := installFakeCloneWP(t, dir, []string{"abc_options", "old_options", "old_posts"})
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	previous := cloneTarget{prefix: "old_", dbName: "old_db", dbHost: "localhost"}
	if err := app.replaceCloneTables(context.Background(), dir, Config{}, previous, []string{"abc_options"}); err != nil {
		t.Fatalf("replaceCloneTables() error = %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "DROP") {
		t.Fatalf("tables in a database the previous config did not use must stay:\n%s", log)
	}
}

func TestReplaceCloneTablesRenamesToConfiguredPrefix(t *testing.T) {
	dir := t.TempDir()
	writeCloneTargetConfig(t, dir, "target_db", "wp_")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	logPath := installFakeCloneWP(t, dir, []string{"abc_options", "abc_usermeta", "wp_options", "wp_posts"})
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

	previous := cloneTarget{prefix: "wp_", dbName: "target_db", dbHost: "localhost"}
	cfg := Config{CloneDBPrefix: "wp_"}
	if err := app.replaceCloneTables(context.Background(), dir, cfg, previous, []string{"abc_options", "abc_usermeta"}); err != nil {
		t.Fatalf("replaceCloneTables() error = %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	drop := strings.Index(string(log), "DROP TABLE `wp_options`, `wp_posts`")
	rename := strings.Index(string(log), "RENAME TABLE `abc_options` TO `wp_options`, `abc_usermeta` TO `wp_usermeta`")
	if drop < 0 || rename < 0 || drop > rename {
		t.Fatalf("previous wp_ tables must be dropped before the rename:\n%s", log)
	}
	for _, want := range []string{"option_name = 'wp_user_roles' WHERE option_name = 'abc_user_roles'", "BINARY LEFT(meta_key, 4) = 'abc_'"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("wp log missing %q:\n%s", want, log)
		}
	}
	if !strings.Contains(stdout.String(), "Cloned tables renamed to wp_") {
		t.Fatalf("missing rename step:\n%s", stdout.String())
	}
}

func TestReplaceCloneTablesRejectsRenameCollisionInDump(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	logPath := installFakeCloneWP(t, dir, []string{"abc_options", "wp_options"})
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.replaceCloneTables(context.Background(), dir, Config{CloneDBPrefix: "wp_"}, cloneTarget{}, []string{"abc_options", "wp_options"})
	if err == nil || !strings.Contains(err.Error(), "already contains wp_options") {
		t.Fatalf("replaceCloneTables() error = %v, want rename collision", err)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "DROP") || strings.Contains(string(log), "RENAME") {
		t.Fatalf("nothing may change before the collision is reported:\n%s", log)
	}
}
