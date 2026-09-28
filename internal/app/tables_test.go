package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// installFakeTablesWP fakes a local WP-CLI that resolves wp-config.php like the real one
// (config get and config list read the current file, so rewrites are observed), lists
// the given base tables, accepts every other query, and logs each call.
func installFakeTablesWP(t *testing.T, dir string, tables ...string) string {
	t.Helper()
	logPath := filepath.Join(dir, "wp.log")
	wpConfig := shellQuote(filepath.Join(dir, "wp-config.php"))
	listing := ""
	for _, table := range tables {
		listing += table + `\tBASE TABLE\n`
	}
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
config_value() { sed -n "s/^$1\$/\1/p" `+wpConfig+`; }
case "$*" in
  *"config get table_prefix"*) config_value "\$table_prefix = '\(.*\)';"; exit 0 ;;
  *"config list"*)
    printf '[{"name":"table_prefix","value":"%s"},{"name":"DB_NAME","value":"%s"},{"name":"DB_HOST","value":"%s"}]\n' \
      "$(config_value "\$table_prefix = '\(.*\)';")" "$(config_value "define('DB_NAME', '\(.*\)');")" "$(config_value "define('DB_HOST', '\(.*\)');")"
    exit 0 ;;
  *"SHOW FULL TABLES"*) printf '`+listing+`'; exit 0 ;;
  *"db query"*) exit 0 ;;
  *"db export"*) exit 0 ;;
  *"plugin list"*) printf '[]\n'; exit 0 ;;
esac
exit 1
`)
	return logPath
}

// writeTestWPConfig writes a wp-config.php with a literal prefix and, when dbName is set,
// literal DB_NAME and DB_HOST constants.
func writeTestWPConfig(t *testing.T, dir string, prefix string, dbName string) string {
	t.Helper()
	wpConfig := filepath.Join(dir, "wp-config.php")
	contents := "<?php\n"
	if dbName != "" {
		contents += "define('DB_NAME', '" + dbName + "');\ndefine('DB_HOST', 'localhost');\n"
	}
	contents += "$table_prefix = '" + prefix + "';\nrequire_once ABSPATH . 'wp-settings.php';\n"
	writeFile(t, wpConfig, contents)
	return wpConfig
}

func readTestLog(t *testing.T, path string) string {
	t.Helper()
	log, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(log)
}

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

func TestInstallationTablesAwkMatchesGo(t *testing.T) {
	t.Parallel()
	tables := []string{
		"wp_options", "wp_users", "wp_posts",
		"wp_2_options", "WP_2_Posts",
		"wp_2019_options", "wp_2019_users",
		"wp_myplugin_options", "wp_myplugin_log",
		"WP_Shop_options", "wp_shop_users",
		"other_options",
	}
	listing := ""
	for _, table := range tables {
		listing += table + "\tBASE TABLE\n"
	}
	cmd := exec.Command("awk", "-F", "\t", "-v", "p=wp_", installationTablesAwk)
	cmd.Stdin = strings.NewReader(listing)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("awk failed: %v", err)
	}
	if want := strings.Join(installationTables(tables, "wp_"), ","); string(output) != want {
		t.Fatalf("awk = %q, Go = %q; installationTablesAwk and installationTables must agree", output, want)
	}
}

func TestPlanPullTablesFollowsDBReset(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "")
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "wp_old_plugin", "blog_options", "blog_users")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	dump := []string{"wp_options", "wp_users"}

	for name, test := range map[string]struct {
		mode  runtimeMode
		reset string
		drops string
		kept  int
	}{
		"DDEV defaults to the database":            {mode: modeDDEV, drops: "blog_options,blog_users,wp_old_plugin"},
		"standalone defaults to the installation":  {mode: modeStandalone, drops: "wp_old_plugin"},
		"installation narrows DDEV":                {mode: modeDDEV, reset: dbResetInstallation, drops: "wp_old_plugin"},
		"database widens standalone":               {mode: modeStandalone, reset: dbResetDatabase, drops: "blog_options,blog_users,wp_old_plugin"},
		"none counts within the runtime's default": {mode: modeDDEV, reset: dbResetNone, kept: 3},
	} {
		plan := app.planPullTables(context.Background(), dir, Config{DBReset: test.reset}, test.mode, "wp_", dump)
		if got := strings.Join(plan.drops, ","); got != test.drops || plan.kept != test.kept {
			t.Fatalf("%s: drops = %q, kept = %d; want %q, %d", name, got, plan.kept, test.drops, test.kept)
		}
	}
}
