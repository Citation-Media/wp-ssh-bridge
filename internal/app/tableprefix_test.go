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
	installFakeTablesWP(t, dir, "wp_options", "wp_posts", "wp_abc_options", "wp_abc_posts")
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

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
