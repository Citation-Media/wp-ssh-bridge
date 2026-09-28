package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakePrefixWP fakes a local WP-CLI whose config get reads the current
// $table_prefix literal from wp-config.php, so rewrites are observed like real WP-CLI.
func installFakePrefixWP(t *testing.T, dir string, tables string) string {
	t.Helper()
	logPath := filepath.Join(dir, "wp.log")
	wpConfig := filepath.Join(dir, "wp-config.php")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
case "$*" in
  *"config get table_prefix"*) sed -n "s/^\$table_prefix = '\(.*\)';\$/\1/p" `+shellQuote(wpConfig)+`; exit 0 ;;
  *"db query SHOW TABLES --skip-column-names"*) printf '`+tables+`'; exit 0 ;;
  *"plugin list"*) printf '[]\n'; exit 0 ;;
esac
exit 1
`)
	return logPath
}

func writePrefixWPConfig(t *testing.T, dir string, prefix string) string {
	t.Helper()
	wpConfig := filepath.Join(dir, "wp-config.php")
	writeFile(t, wpConfig, "<?php\n$table_prefix = '"+prefix+"';\nrequire_once ABSPATH . 'wp-settings.php';\n")
	return wpConfig
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
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "config get table_prefix") || strings.Count(string(log), "wp_ssh_wp()") != 1 {
		t.Fatalf("prefix lookup should share the export SSH session:\n%s", log)
	}
}

func TestRecordPullSourceTablePrefixDropsStaleRecordWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, pullSourcePrefixPath(dir), "old_\n")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)

	if err := app.recordPullSourceTablePrefix(dir, "Success: Exported.\n"+tablePrefixMarker+"wp_; DROP\n"); err != nil {
		t.Fatalf("recordPullSourceTablePrefix() error = %v", err)
	}
	if _, err := os.Stat(pullSourcePrefixPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("stale prefix record should be removed, got err: %v", err)
	}
	if !strings.Contains(stderr.String(), "Could not detect the pull source table prefix") {
		t.Fatalf("missing detection warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixFollowsImportedTables(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writePrefixWPConfig(t, dir, "wp_")
	installFakePrefixWP(t, dir, `wp_options\nwp_posts\nwp_abc_options\nwp_abc_posts\n`)
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
	// Tables under the new prefix also start with the old one and must not count as stale.
	if !strings.Contains(stderr.String(), "2 local tables with the previous prefix wp_ remain") {
		t.Fatalf("missing stale table warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixSkipsWPCLIWhenLiteralMatches(t *testing.T) {
	dir := t.TempDir()
	writePrefixWPConfig(t, dir, "abc_")
	logPath := installFakePrefixWP(t, dir, `abc_options\n`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := app.alignLocalTablePrefix(context.Background(), dir, Config{}, "abc_"); err != nil {
		t.Fatalf("alignLocalTablePrefix() error = %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("matching wp-config.php literal should not start WP-CLI, got err: %v", err)
	}
}

func TestAlignLocalTablePrefixKeepsConfigWithoutImportedTables(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writePrefixWPConfig(t, dir, "wp_")
	installFakePrefixWP(t, dir, `wp_options\nwp_posts\n`)
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
	if stderr.Len() != 0 {
		t.Fatalf("unexpected warning:\n%s", stderr.String())
	}
}

func TestAlignLocalTablePrefixWarnsWithoutPrefixAssignment(t *testing.T) {
	dir := t.TempDir()
	wpConfig := filepath.Join(dir, "wp-config.php")
	original := "<?php\nrequire_once dirname(__DIR__) . '/config/application.php';\n"
	writeFile(t, wpConfig, original)
	installFakePrefixWP(t, dir, `abc_options\n`)
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
	wpConfig := writePrefixWPConfig(t, dir, "target_")
	logPath := installFakePrefixWP(t, dir, `abc_options\n`)
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
	if log, err := os.ReadFile(logPath); err == nil && strings.Contains(string(log), "table_prefix") {
		t.Fatalf("clone with --db-prefix should not probe the local prefix:\n%s", log)
	}
}

func TestPostPullSwitchesDefaultLocalPrefixToSourcePrefix(t *testing.T) {
	dir := t.TempDir()
	wpConfig := writePrefixWPConfig(t, dir, "wp_")
	logPath := installFakePrefixWP(t, dir, `wp_options\nwp_posts\nabc_options\nabc_posts\nabc_users\n`)
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
	if !strings.Contains(stderr.String(), "2 local tables with the previous prefix wp_ remain") {
		t.Fatalf("missing stale table warning:\n%s", stderr.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	// WordPress-loading commands must only run after the prefix points at the imported tables.
	if strings.Index(string(log), "plugin list") < strings.Index(string(log), "db query SHOW TABLES") {
		t.Fatalf("plugin cleanup ran before the prefix was aligned:\n%s", log)
	}
}
