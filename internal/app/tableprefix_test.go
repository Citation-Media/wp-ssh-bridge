package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRemoteExportCommandsExportOnlyTheInstallation(t *testing.T) {
	t.Parallel()
	tables := `wp_options\tBASE TABLE\nwp_users\tBASE TABLE\nWP_2_Options\tBASE TABLE\nwp_shop_options\tBASE TABLE\nwp_shop_users\tBASE TABLE\nwp_myplugin_options\tBASE TABLE\nother_options\tBASE TABLE\n`
	for name, test := range map[string]struct {
		configGet string
		query     string
		want      string
	}{
		"notice before prefix": {
			configGet: `printf 'Deprecated: something\n\nwp_\n'`,
			query:     `printf '` + tables + `'`,
			want:      tablePrefixMarker + "wp_\nexport /tmp/dump.sql --tables=wp_options,wp_users,WP_2_Options,wp_myplugin_options\n",
		},
		"failing prefix lookup": {
			configGet: `echo boom >&2; return 1`,
			query:     `printf '` + tables + `'`,
			want:      tablePrefixMarker + "\nexport /tmp/dump.sql\n",
		},
		"failing table query": {
			configGet: `echo wp_`,
			query:     `return 1`,
			want:      tablePrefixMarker + "wp_\nexport /tmp/dump.sql\n",
		},
	} {
		wp := `wp_ssh_wp() { case "$*" in *"config get"*) ` + test.configGet + ` ;; *"db query"*) ` + test.query + ` ;; *"db export"*) shift 3; echo "export $*" ;; esac; }`
		output, err := exec.Command("sh", "-c", "set -eu; "+wp+"; "+remoteExportCommands("wp_ssh_wp", "/tmp/dump.sql")).Output()
		if err != nil {
			t.Fatalf("%s: command failed: %v", name, err)
		}
		if string(output) != test.want {
			t.Fatalf("%s: output = %q, want %q", name, output, test.want)
		}
	}
}

func TestDBPullRecordsSourceTablePrefixFromExport(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\nprintf 'Success: Exported.\\n"+tablePrefixMarker+"abc_\\n'\n")
	installFakeCommand(t, dir, "scp", "#!/bin/sh\nexit 0\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.dbPull(context.Background(), dir, Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, true)
	if err != nil {
		t.Fatalf("dbPull() error = %v", err)
	}
	got, err := os.ReadFile(pullSourcePrefixPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc_\n" {
		t.Fatalf("recorded prefix = %q, want abc_", got)
	}
	log := readTestLog(t, logPath)
	if !strings.Contains(log, "config get table_prefix") || strings.Count(log, "wp_ssh_wp()") != 1 {
		t.Fatalf("prefix lookup should share the export SSH session:\n%s", log)
	}
}

func TestDBPullRecordsNoPrefixWhenDownloadFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, pullSourcePrefixPath(dir), "old_\n")
	// The export prints a prefix; the download, which streams "cat <dump>", fails.
	installFakeSSH(t, dir, "#!/bin/sh\ncase \"$*\" in\n  *' cat '*) exit 1 ;;\nesac\nprintf '"+tablePrefixMarker+"abc_\\n'\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.dbPull(context.Background(), dir, Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, true); err == nil {
		t.Fatal("dbPull() succeeded despite a failed download")
	}
	if _, err := os.Stat(pullSourcePrefixPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("neither the stale nor the new prefix may stay after a failed download, got err: %v", err)
	}
}

func TestRecordPullSourceTablePrefixWarnsOnInvalidPrefix(t *testing.T) {
	dir := t.TempDir()
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)

	if err := app.recordPullSourceTablePrefix(dir, "Success: Exported.\n"+tablePrefixMarker+"wp_; DROP\n"); err != nil {
		t.Fatalf("recordPullSourceTablePrefix() error = %v", err)
	}
	if _, err := os.Stat(pullSourcePrefixPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("an invalid prefix must not be recorded, got err: %v", err)
	}
	if !strings.Contains(stderr.String(), "Could not detect the pull source table prefix") {
		t.Fatalf("missing detection warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixFollowsImportedTables(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writeTestWPConfig(t, dir, "wp_", "")
	installFakeTablesWP(t, dir, "wp_options", "wp_posts", "wp_abc_options", "wp_abc_posts", "wp_abc_users")
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "wp_abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "$table_prefix = 'wp_abc_';\n") || strings.Contains(string(got), "$table_prefix = 'wp_';") {
		t.Fatalf("wp-config.php prefix not updated:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "Local table prefix set to wp_abc_") {
		t.Fatalf("missing prefix update step:\n%s", stdout.String())
	}
	// The imported wp_abc_ install also starts with wp_ and must not count as leftovers.
	if !strings.Contains(stderr.String(), "The previous prefix wp_ still has 2 tables") {
		t.Fatalf("missing leftover table warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixCountsLeftoversWhenNewPrefixIsShorter(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_abc_", "")
	installFakeTablesWP(t, dir, "wp_abc_options", "wp_abc_posts", "wp_abc_users", "wp_options", "wp_users")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "wp_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "The previous prefix wp_abc_ still has 3 tables") {
		t.Fatalf("missing leftover table warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixSkipsWPCLIWhenLiteralMatches(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "abc_", "")
	logPath := installFakeTablesWP(t, dir, "abc_options")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	if log := readTestLog(t, logPath); log != "" {
		t.Fatalf("matching wp-config.php literal should not start WP-CLI:\n%s", log)
	}
}

func TestAlignLocalTablePrefixResolvesSeveralAssignmentsWithWPCLI(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "wp-config.php"), "<?php\nif (getenv('LOCAL')) {\n  $table_prefix = 'abc_';\n} else {\n  $table_prefix = 'dev_';\n}\n")
	logPath := installFakeTablesWP(t, dir, "abc_options")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	if !strings.Contains(readTestLog(t, logPath), "config get table_prefix") {
		t.Fatal("a config with several $table_prefix assignments must be resolved through WP-CLI")
	}
}

func TestAlignLocalTablePrefixKeepsConfigWithoutImportedTables(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writeTestWPConfig(t, dir, "wp_", "")
	installFakeTablesWP(t, dir, "wp_options", "wp_posts")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "$table_prefix = 'wp_';") {
		t.Fatalf("prefix should stay unchanged without imported tables:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "The imported table abc_options was not found") {
		t.Fatalf("missing warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixMatchesTableNamesCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writeTestWPConfig(t, dir, "wp_", "")
	installFakeTablesWP(t, dir, "wpabc_options")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "WPabc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	if got, _ := os.ReadFile(wpConfig); !strings.Contains(string(got), "$table_prefix = 'WPabc_';") {
		t.Fatalf("lower_case_table_names should not hide the imported tables:\n%s", got)
	}
}

func TestAlignLocalTablePrefixWarnsWithoutPrefixAssignment(t *testing.T) {
	dir := t.TempDir()
	wpConfig := filepath.Join(dir, "wp-config.php")
	original := "<?php\nrequire_once dirname(__DIR__) . '/config/application.php';\n"
	writeFile(t, wpConfig, original)
	installFakeTablesWP(t, dir, "abc_options")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("wp-config.php without $table_prefix should not change:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "has no $table_prefix assignment") {
		t.Fatalf("missing assignment warning:\n%s", stderr.String())
	}
}

func TestPostPullCloneKeepsExplicitTargetPrefix(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writeTestWPConfig(t, dir, "target_", "")
	logPath := installFakeTablesWP(t, dir, "abc_options")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	cfg := Config{CloneDBPrefix: "target_", SkipSearchReplace: true}
	if err := app.postPull(context.Background(), adapter, cfg, true); err != nil {
		t.Fatalf("postPull() error = %v", err)
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "$table_prefix = 'target_';") {
		t.Fatalf("clone --db-prefix should be kept:\n%s", got)
	}
	if _, err := os.Stat(pullSourcePrefixPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("prefix record should be consumed, got err: %v", err)
	}
	if log := readTestLog(t, logPath); strings.Contains(log, "table_prefix") {
		t.Fatalf("clone with --db-prefix should not probe the local prefix:\n%s", log)
	}
}

func TestPostPullSwitchesDefaultLocalPrefixToSourcePrefix(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writeTestWPConfig(t, dir, "wp_", "")
	logPath := installFakeTablesWP(t, dir, "wp_options", "wp_posts", "abc_options", "abc_posts", "abc_users")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	if err := app.postPull(context.Background(), adapter, Config{SkipSearchReplace: true}, false); err != nil {
		t.Fatalf("postPull() error = %v\nstderr:\n%s", err, stderr.String())
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "$table_prefix = 'abc_';") || strings.Contains(string(got), "$table_prefix = 'wp_';") {
		t.Fatalf("local prefix should follow the source prefix:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "The previous prefix wp_ still has 2 tables") {
		t.Fatalf("missing leftover table warning:\n%s", stderr.String())
	}
	// WordPress-loading commands must only run after the prefix points at the imported tables.
	log := readTestLog(t, logPath)
	tables, plugins := strings.Index(log, "SHOW FULL TABLES"), strings.Index(log, "plugin list")
	if tables < 0 || plugins < 0 || plugins < tables {
		t.Fatalf("plugin cleanup must run after the prefix was aligned:\n%s", log)
	}
}

func TestPostPullCloneSwitchesExistingTargetToSourcePrefix(t *testing.T) {
	dir := t.TempDir()
	// A target that already runs WordPress under another prefix, cloned with --skip-files.
	wpConfig := writeTestWPConfig(t, dir, "old_", "")
	installFakeTablesWP(t, dir, "abc_options", "abc_posts", "abc_users")
	writeFile(t, pullSourcePrefixPath(dir), "abc_\n")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	if err := app.postPull(context.Background(), adapter, Config{SkipSearchReplace: true}, true); err != nil {
		t.Fatalf("postPull() error = %v\nstderr:\n%s", err, stderr.String())
	}
	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "$table_prefix = 'abc_';") {
		t.Fatalf("clone target should use the source prefix:\n%s", got)
	}
}

func TestSanitizeWPConfigIsSilentWhenAlreadySanitized(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "wp-config.php"), "<?php\ndefine('DB_NAME', 'prod');\nrequire_once ABSPATH . 'wp-settings.php';\n")
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

	for range 2 {
		if err := app.sanitizeWPConfig(dir, Config{}); err != nil {
			t.Fatalf("sanitizeWPConfig() error = %v", err)
		}
	}
	if count := strings.Count(stdout.String(), "Local wp-config.php sanitized"); count != 1 {
		t.Fatalf("sanitize reported %d times, want once for the pre-import and post-pull runs:\n%s", count, stdout.String())
	}
}

func TestPlanPullTablesDropsEverythingTheDumpDoesNotRecreateInDDEV(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "abc_", "")
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "abc_options", "abc_old_plugin", "other_app_data")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	plan := app.planPullTables(context.Background(), dir, Config{}, modeDDEV, []string{"abc_options", "abc_users"})
	want := []string{"abc_old_plugin", "other_app_data", "wp_options", "wp_users"}
	if strings.Join(plan.drops, ",") != strings.Join(want, ",") {
		t.Fatalf("drops = %q, want %q", plan.drops, want)
	}
}

func TestPlanPullTablesDropsOnlyTheLocalInstallationInStandalone(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "")
	installFakeTablesWP(t, dir, "wp_options", "wp_users", "wp_old_plugin", "wp_shop_options", "wp_shop_users", "blog_options", "abc_options")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	plan := app.planPullTables(context.Background(), dir, Config{}, modeStandalone, []string{"abc_options", "abc_users"})
	want := []string{"wp_old_plugin", "wp_options", "wp_users"}
	if strings.Join(plan.drops, ",") != strings.Join(want, ",") {
		t.Fatalf("drops = %q, want %q (other sites sharing the database must stay)", plan.drops, want)
	}
}

func TestLocalDBDumpExportsOnlyTheLocalInstallation(t *testing.T) {
	dir := t.TempDir()
	writeTestWPConfig(t, dir, "wp_", "")
	logPath := installFakeTablesWP(t, dir, "wp_options", "wp_users", "wp_shop_options", "wp_shop_users", "blog_options")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.ensureLocalDBDump(context.Background(), dir, Config{}, filepath.Join(downloadsDir(dir), "db.sql.gz")); err != nil {
		t.Fatalf("ensureLocalDBDump() error = %v", err)
	}
	if !strings.Contains(readTestLog(t, logPath), "db export - --add-drop-table --tables=wp_options,wp_users") {
		t.Fatalf("push export should list only the local installation:\n%s", readTestLog(t, logPath))
	}
}

func TestRemoveLocalDBDumpDropsALeftoverSourceDump(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(downloadsDir(dir), "db.sql.gz")
	writeFile(t, dump, "source dump from pull --skip-import")
	if err := removeLocalDBDump(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dump); !os.IsNotExist(err) {
		t.Fatalf("a direct push must not reuse an earlier dump, got err: %v", err)
	}
	if err := removeLocalDBDump(dir); err != nil {
		t.Fatalf("a missing dump is not an error: %v", err)
	}
}

func TestCheckPushTablePrefix(t *testing.T) {
	for name, test := range map[string]struct {
		remote  string
		wantErr bool
	}{
		"different prefix stops": {remote: "stg_", wantErr: true},
		"same prefix continues":  {remote: "abc_"},
		"no WordPress continues": {remote: ""},
	} {
		dir := t.TempDir()
		writeTestWPConfig(t, dir, "abc_", "")
		installFakeTablesWP(t, dir)
		installFakeSSH(t, dir, "#!/bin/sh\nprintf '"+test.remote+"\\n'\n")
		app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

		err := app.checkPushTablePrefix(context.Background(), dir, Config{PushUser: "deploy", PushHost: "staging.example.com", PushRemotePath: "/var/www/html"})
		if test.wantErr != (err != nil) {
			t.Fatalf("%s: checkPushTablePrefix() error = %v", name, err)
		}
		if test.wantErr && !strings.Contains(err.Error(), "uses the table prefix stg_, but the local database uses abc_") {
			t.Fatalf("%s: unexpected message: %v", name, err)
		}
	}
}
