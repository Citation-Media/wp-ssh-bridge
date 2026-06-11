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

func TestSanitizeWPConfigContents(t *testing.T) {
	t.Parallel()
	input := `<?php
define('DB_NAME', 'prod');
define('DB_USER', 'prod');
define('DB_SSL_CA', '/prod/ca.pem');
define('WP_DEBUG', false);
define('COOKIE_DOMAIN', $_SERVER['HTTP_HOST']);
/* That's all, stop editing! Happy publishing. */
require_once ABSPATH . 'wp-settings.php';
`

	got := sanitizeWPConfigContents(input)
	for _, removed := range []string{"DB_NAME", "DB_USER', 'prod", "DB_SSL_CA", "define('WP_DEBUG'"} {
		if strings.Contains(got, removed) {
			t.Fatalf("sanitized config still contains %q:\n%s", removed, got)
		}
	}
	for _, want := range []string{
		"define('COOKIE_DOMAIN', $_SERVER['HTTP_HOST'] ?? '');",
		"define('WP_ENVIRONMENT_TYPE', 'development');",
		"wp-config-ddev.php",
		"require_once ABSPATH . 'wp-settings.php';",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized config missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "<?php\n"+ddevConfigIncludeSnippet) {
		t.Fatalf("DDEV config include should be first PHP command:\n%s", got)
	}
	if count := strings.Count(got, "$ddev_settings = __DIR__ . '/wp-config-ddev.php';"); count != 1 {
		t.Fatalf("DDEV config include count = %d, want 1:\n%s", count, got)
	}
}

func TestSanitizeWPConfigContentsPreservesExistingDDEVInclude(t *testing.T) {
	t.Parallel()
	input := `<?php
// Custom bootstrap.

` + ddevConfigIncludeSnippet + `require_once ABSPATH . 'wp-settings.php';
`

	got := sanitizeWPConfigContents(input)
	if !strings.Contains(got, "// Custom bootstrap.\n\n"+ddevConfigIncludeSnippet) {
		t.Fatalf("existing DDEV config include should be preserved in place with its comment:\n%s", got)
	}
	if count := strings.Count(got, "$ddev_settings = __DIR__ . '/wp-config-ddev.php';"); count != 1 {
		t.Fatalf("DDEV config include count = %d, want 1:\n%s", count, got)
	}
}

func TestSanitizeWPConfigForRuntimeSkipsStandalone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := `<?php
define('DB_NAME', 'prod');
define('DB_USER', 'prod');
define('DB_SSL_CA', '/prod/ca.pem');
require_once ABSPATH . 'wp-settings.php';
`
	wpConfig := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(wpConfig, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	app := newApp(bytes.NewReader(nil), &stdout, &stderr)
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})
	for _, hook := range adapter.PostPullHooks() {
		if err := hook(context.Background(), app, adapter.Root(), Config{}); err != nil {
			t.Fatalf("PostPullHooks() error = %v", err)
		}
	}

	got, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("standalone config was changed:\n%s", got)
	}
}

func TestSanitizeWPConfigForRuntimeSanitizesDDEV(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := `<?php
define('DB_NAME', 'prod');
define('DB_USER', 'prod');
define('DB_PASSWORD', 'prod');
define('DB_HOST', 'prod-db');
define('DB_SSL_CA', '/prod/ca.pem');
require_once ABSPATH . 'wp-settings.php';
`
	wpConfig := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(wpConfig, []byte(input), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	app := newApp(bytes.NewReader(nil), &stdout, &stderr)
	adapter := adapterForRuntime(runtimeContext{Mode: modeDDEV, Root: dir})
	for _, hook := range adapter.PostPullHooks() {
		if err := hook(context.Background(), app, adapter.Root(), Config{}); err != nil {
			t.Fatalf("PostPullHooks() error = %v", err)
		}
	}

	gotBytes, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)
	for _, removed := range []string{"define('DB_NAME'", "define('DB_USER'", "define('DB_PASSWORD'", "define('DB_HOST'", "define('DB_SSL_CA'"} {
		if strings.Contains(got, removed) {
			t.Fatalf("DDEV config still contains %q:\n%s", removed, got)
		}
	}
	if !strings.Contains(got, "wp-config-ddev.php") {
		t.Fatalf("DDEV config missing wp-config-ddev.php include:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "wp-config-ddev.php")); !os.IsNotExist(err) {
		t.Fatalf("sanitize should not create or overwrite wp-config-ddev.php, got err: %v", err)
	}
}

func TestUpdateWPConfigURLConstantsContentsLetsDDEVOwnHomeAndSiteURL(t *testing.T) {
	t.Parallel()
	input := `<?php
define('WP_HOME', "https://acme-corp.de");
define('WP_SITEURL', "https://acme-corp.de");
define( 'DOMAIN_CURRENT_SITE', 'acme-corp.de' );
require_once ABSPATH . 'wp-settings.php';
`

	got := updateWPConfigURLConstantsContents(input, wpConfigURLConstantValues("/project", Config{LocalURL: "https://project.ddev.site"}, modeDDEV))
	for _, removed := range []string{
		"define('WP_HOME'",
		"define('WP_SITEURL'",
	} {
		if strings.Contains(got, removed) {
			t.Fatalf("DDEV config should own %q, but wp-config.php still contains it:\n%s", removed, got)
		}
	}
	want := "define('DOMAIN_CURRENT_SITE', parse_url(getenv('DDEV_PRIMARY_URL_WITHOUT_PORT') ?: getenv('DDEV_PRIMARY_URL') ?: 'https://project.ddev.site', PHP_URL_HOST));"
	if !strings.Contains(got, want) {
		t.Fatalf("updated config missing %q:\n%s", want, got)
	}
	if strings.Contains(got, "acme-corp.de") {
		t.Fatalf("production domain remained in DDEV constants:\n%s", got)
	}
}

func TestUpdateWPConfigURLConstantsContentsUsesStandaloneLiterals(t *testing.T) {
	t.Parallel()
	input := `<?php
/* That's all, stop editing! Happy publishing. */
`

	got := updateWPConfigURLConstantsContents(input, wpConfigURLConstantValues("/project", Config{LocalURL: "https://local.test"}, modeStandalone))
	for _, want := range []string{
		"define('WP_HOME', 'https://local.test');",
		"define('WP_SITEURL', 'https://local.test');",
		"define('DOMAIN_CURRENT_SITE', 'local.test');",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated config missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "define('WP_HOME'") > strings.Index(got, "stop editing") {
		t.Fatalf("constants should be inserted before stop-editing marker:\n%s", got)
	}
}

func TestBuildRsyncExcludes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "extra-plugins.txt"), []byte("updraftplus\nplugin-file.php\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	excludeList, err := buildRsyncExcludes(dir, Config{PluginRemoveFile: ".ddev/extra-plugins.txt"}, false)
	if err != nil {
		t.Fatalf("buildRsyncExcludes() error = %v", err)
	}
	excludes := strings.Join(excludeList, "\n")
	for _, want := range []string{
		"wp-content/uploads/",
		"wp-content/plugins/updraftplus/",
		"wp-content/plugins/plugin-file.php",
	} {
		if !strings.Contains(excludes, want) {
			t.Fatalf("excludes missing %q:\n%s", want, excludes)
		}
	}
	if strings.Contains(excludes, "*.log") {
		t.Fatalf("logs should use sender-side hide rules, not excludes:\n%s", excludes)
	}
	if got := strings.Join(buildRsyncHideRules(), "\n"); !strings.Contains(got, "*.log") {
		t.Fatalf("hide rules missing recursive log rule:\n%s", got)
	}

	excludeList, err = buildRsyncExcludes(dir, Config{CloneImages: true}, false)
	if err != nil {
		t.Fatalf("buildRsyncExcludes() with CloneImages error = %v", err)
	}
	excludes = strings.Join(excludeList, "\n")
	if strings.Contains(excludes, "wp-content/uploads/") {
		t.Fatalf("clone images should not exclude uploads:\n%s", excludes)
	}
	if strings.Contains(excludes, "wp-config.php") {
		t.Fatalf("DDEV pulls should allow wp-config.php so post-pull can sanitize it:\n%s", excludes)
	}

	if err := os.WriteFile(filepath.Join(dir, ".ddev", "bad-plugins.txt"), []byte("../secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildRsyncExcludes(dir, Config{PluginRemoveFile: ".ddev/bad-plugins.txt"}, false); err == nil {
		t.Fatal("buildRsyncExcludes() accepted invalid plugin block list")
	}
}

func TestBuildRsyncExcludesPreservesStandaloneWPConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	excludeList, err := buildRsyncExcludes(dir, Config{}, true)
	if err != nil {
		t.Fatalf("buildRsyncExcludes() error = %v", err)
	}
	excludes := strings.Join(excludeList, "\n")
	if !strings.Contains(excludes, "wp-config.php") {
		t.Fatalf("standalone pulls should preserve local wp-config.php:\n%s", excludes)
	}
}

func TestFilesPullContinuesAfterRsyncVanishedWarning(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "rsync", `#!/bin/sh
printf 'file vanished during transfer\n' >&2
exit 24
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.filesPull(context.Background(), dir, Config{
		User:       "deploy",
		Host:       "example.com",
		RemotePath: "/var/www/html",
	}, false)
	if err != nil {
		t.Fatalf("filesPull() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	output := stdout.String() + stderr.String()
	if strings.Contains(output, "✗ Syncing WordPress files from pull source failed") {
		t.Fatalf("tolerated rsync warning printed a failed step:\n%s", output)
	}
	for _, want := range []string{
		"✓ WordPress files synced",
		"rsync  │ file vanished during transfer",
		"! Continuing after rsync warning: remote files vanished during transfer.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestFilesPullRsyncUsesDeleteAndHideRuleForRecursiveLogs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "extra-plugins.txt"), []byte("updraftplus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "rsync-args.txt")
	installFakeCommand(t, dir, "rsync", `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsPath)+`
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.filesPull(context.Background(), dir, Config{
		User:             "deploy",
		Host:             "example.com",
		RemotePath:       "/var/www/html",
		PluginRemoveFile: ".ddev/extra-plugins.txt",
	}, false)
	if err != nil {
		t.Fatalf("filesPull() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsBytes)
	for _, want := range []string{
		"--delete",
		"--filter=H *.log",
		"--exclude=.ddev/",
		"--exclude=wp-content/uploads/",
		"--exclude=wp-content/plugins/updraftplus/",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("rsync args missing %q:\n%s", want, args)
		}
	}
	for _, unwanted := range []string{
		"--delete-excluded",
		"--exclude=*.log",
	} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("rsync args should not contain %q:\n%s", unwanted, args)
		}
	}
}

func TestRsyncHideRuleDeletesLocalLogsRecursively(t *testing.T) {
	t.Parallel()
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not available")
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	for _, path := range []string{
		filepath.Join(src, "wp-content", "plugins", "foo"),
		filepath.Join(dst, ".ddev", "bin"),
		filepath.Join(dst, ".ddev", ".logs"),
		filepath.Join(dst, "wp-content", "themes", "theme"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(src, "wp-content", "plugins", "foo", "keep.php"):        "remote\n",
		filepath.Join(src, "wp-content", "plugins", "foo", "remote.log"):      "remote log\n",
		filepath.Join(dst, ".ddev", "bin", "wp-ssh-bridge"):                   "bridge\n",
		filepath.Join(dst, ".ddev", "provider.log"):                           "provider log\n",
		filepath.Join(dst, ".ddev", ".logs", "webserver.log"):                 "webserver log\n",
		filepath.Join(dst, "root.log"):                                        "root log\n",
		filepath.Join(dst, "wp-content", "themes", "theme", "nested.log"):     "nested log\n",
		filepath.Join(dst, "wp-content", "plugins", "foo", "stale-local.log"): "stale log\n",
	}
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(rsync, "-az", "--delete", "--safe-links", "--filter=H *.log", "--exclude=.ddev/", src+"/", dst+"/")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync failed: %v\n%s", err, output)
	}
	for _, path := range []string{
		filepath.Join(dst, "root.log"),
		filepath.Join(dst, "wp-content", "themes", "theme", "nested.log"),
		filepath.Join(dst, "wp-content", "plugins", "foo", "stale-local.log"),
		filepath.Join(dst, "wp-content", "plugins", "foo", "remote.log"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("log file should be absent at %s, got err: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(dst, ".ddev", "bin", "wp-ssh-bridge"),
		filepath.Join(dst, ".ddev", "provider.log"),
		filepath.Join(dst, ".ddev", ".logs", "webserver.log"),
		filepath.Join(dst, "wp-content", "plugins", "foo", "keep.php"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected file at %s: %v", path, err)
		}
	}
}

func TestEnsureLocalPullDestinationWritableRepairsUserOwnedDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wpContent := filepath.Join(dir, "wp-content")
	if err := os.MkdirAll(wpContent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(wpContent, 0o700)
	})
	if localDirWritable(wpContent) {
		t.Skip("filesystem permits writes to read-only directories")
	}

	if err := ensureLocalPullDestinationWritable(dir); err != nil {
		t.Fatalf("ensureLocalPullDestinationWritable() error = %v", err)
	}
	if !localDirWritable(wpContent) {
		t.Fatal("ensureLocalPullDestinationWritable() did not repair directory write access")
	}
}

func TestRsyncArchiveArgsSupportMacOSRsync(t *testing.T) {
	t.Parallel()
	args := strings.Join(rsyncArchiveArgs(), " ")
	if strings.Contains(args, "s") || strings.Contains(args, "--protect-args") {
		t.Fatalf("rsync archive args must support macOS bundled rsync, got: %q", args)
	}
	if args != "-az" {
		t.Fatalf("rsync archive args = %q", args)
	}
}

func TestUniqueReplacementPairsRemovesDuplicatesNoopsAndOrdersLongestFirst(t *testing.T) {
	t.Parallel()
	pairs := uniqueReplacementPairs([]replacementPair{
		{old: "example.com", new: "example.ddev.site"},
		{old: "shop.example.com", new: "shop.ddev.site"},
		{old: "https://shop.example.com", new: "https://shop.ddev.site"},
		{old: "example.com", new: "example.ddev.site"},
		{old: "https://local.test", new: "https://local.test"},
		{old: "", new: "https://local.test"},
	})

	if len(pairs) != 3 {
		t.Fatalf("uniqueReplacementPairs() length = %d, pairs = %#v", len(pairs), pairs)
	}
	gotOrder := []string{pairs[0].old, pairs[1].old, pairs[2].old}
	wantOrder := []string{"https://shop.example.com", "shop.example.com", "example.com"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("uniqueReplacementPairs() order = %#v, want %#v", gotOrder, wantOrder)
	}
}

func TestReplacementPairsIncludeHostOnlyValues(t *testing.T) {
	t.Parallel()
	pairs := replacementPairsForConfiguredDomain(DomainReplacement{
		Old: "https://acme-corp.de",
		New: "https://acme-corp.ddev.site",
	})

	for _, want := range []replacementPair{
		{old: "https://acme-corp.de", new: "https://acme-corp.ddev.site"},
		{old: "http://acme-corp.de", new: "https://acme-corp.ddev.site"},
		{old: "acme-corp.de", new: "acme-corp.ddev.site"},
	} {
		if !replacementPairsContain(pairs, want) {
			t.Fatalf("replacementPairsForConfiguredDomain() missing %#v in %#v", want, pairs)
		}
	}
}

func replacementPairsContain(pairs []replacementPair, want replacementPair) bool {
	for _, pair := range pairs {
		if pair == want {
			return true
		}
	}
	return false
}

func TestLineSetContains(t *testing.T) {
	t.Parallel()
	output := "wp_options\nwp_site\nwp_blogs\n"
	if !lineSetContains(output, "wp_site") {
		t.Fatal("lineSetContains() missed existing table")
	}
	if lineSetContains(output, "wp_site_meta") {
		t.Fatal("lineSetContains() matched partial table name")
	}
}

func TestReplaceSiteURLsWarnsWhenURLDetectionFails(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "wp", `#!/bin/sh
exit 1
`)
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	if err := app.replaceSiteURLs(context.Background(), dir, Config{LocalURL: "https://local.test"}); err != nil {
		t.Fatalf("replaceSiteURLs() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Skipping WordPress URL replacement because no configured replacement pairs exist and the old or new URL could not be detected.") {
		t.Fatalf("missing URL replacement warning:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestReplaceSiteURLsRunsConfiguredReplacementsPerMultisiteBlog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "wp.log")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
case "$*" in
  *"option get home"*) printf 'https://example.com\n'; exit 0 ;;
  *"core is-installed --network"*) exit 0 ;;
  *"site list --field=url"*) printf 'https://example.ddev.site/\nhttps://shop.ddev.site/\n'; exit 0 ;;
  *"db prefix"*) printf 'wp_\n'; exit 0 ;;
  *"db tables"*) printf 'wp_options\nwp_site\nwp_blogs\n'; exit 0 ;;
  *"db query"*) exit 0 ;;
  *"search-replace"*) exit 0 ;;
esac
exit 1
`)
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{
		LocalURL: "https://example.ddev.site",
		PullDomainReplacements: []DomainReplacement{
			{Old: "https://shop.example.com", New: "https://shop.ddev.site"},
		},
	}
	if err := app.replaceSiteURLs(context.Background(), dir, cfg); err != nil {
		t.Fatalf("replaceSiteURLs() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"WordPress URL replacement finished (example.ddev.site)",
		"WordPress URL replacement finished (shop.ddev.site)",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	for _, want := range []string{
		"--url=https://example.ddev.site/ search-replace https://example.com https://example.ddev.site",
		"--url=https://shop.ddev.site/ search-replace https://shop.example.com https://shop.ddev.site",
		"db query UPDATE `wp_site` SET domain = 'shop.ddev.site' WHERE domain = 'shop.example.com'",
		"db query UPDATE `wp_blogs` SET domain = 'shop.ddev.site' WHERE domain = 'shop.example.com'",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("wp log missing %q:\n%s", want, log)
		}
	}
}

func TestBlockedPluginRemovalTargetsOnlyUsesListedPlugins(t *testing.T) {
	t.Parallel()
	targets := blockedPluginRemovalTargets([]string{
		"updraftplus",
		"missing-plugin",
		"wp-mail-smtp.php",
		"wp-mail-smtp",
		"nested-plugin/plugin.php",
	}, map[string]string{
		"updraftplus":   "active",
		"wp-mail-smtp":  "inactive",
		"nested-plugin": "active-network",
	})

	want := []string{"nested-plugin", "updraftplus", "wp-mail-smtp"}
	if strings.Join(targets, ",") != strings.Join(want, ",") {
		t.Fatalf("blockedPluginRemovalTargets() = %#v, want %#v", targets, want)
	}
}

func TestPluginsWithStatus(t *testing.T) {
	t.Parallel()
	plugins := []string{"network-plugin", "regular-plugin", "inactive-plugin"}
	statuses := map[string]string{
		"network-plugin":  "active-network",
		"regular-plugin":  "active",
		"inactive-plugin": "inactive",
	}

	if got := strings.Join(pluginsWithStatus(plugins, statuses, "active"), ","); got != "regular-plugin" {
		t.Fatalf("active plugins = %q", got)
	}
	if got := strings.Join(pluginsWithStatus(plugins, statuses, "active-network"), ","); got != "network-plugin" {
		t.Fatalf("network-active plugins = %q", got)
	}
}

func TestParsePluginStatusesAcceptsWarningPrefixedJSON(t *testing.T) {
	t.Parallel()
	statuses, err := parsePluginStatuses(`Warning: [debug] Constant WP_DEBUG already defined
[{"name":"updraftplus","status":"active"},{"name":"wp-mail-smtp","status":"inactive"}]`)
	if err != nil {
		t.Fatalf("parsePluginStatuses() error = %v", err)
	}
	if statuses["updraftplus"] != "active" || statuses["wp-mail-smtp"] != "inactive" {
		t.Fatalf("parsePluginStatuses() statuses = %#v", statuses)
	}
}

func TestParsePluginStatusesRejectsMissingJSON(t *testing.T) {
	t.Parallel()
	if _, err := parsePluginStatuses("Warning: WordPress emitted a bootstrap warning\n"); err == nil {
		t.Fatal("parsePluginStatuses() accepted output without JSON")
	}
}

func TestTruthyConfigValue(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		if !isTruthyConfigValue(value) {
			t.Fatalf("isTruthyConfigValue(%q) = false", value)
		}
	}
	for _, value := range []string{"", "0", "false", "off", "WP_ALLOW_MULTISITE"} {
		if isTruthyConfigValue(value) {
			t.Fatalf("isTruthyConfigValue(%q) = true", value)
		}
	}
}

func TestProviderAuthValidatesMissingDDEVUserBeforeSSH(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.providerAuth(context.Background(), dir, Config{Host: "example.com", RemotePath: "/var/www/html"})
	if err == nil {
		t.Fatal("providerAuth() accepted missing SSH user")
	}
	if !strings.Contains(err.Error(), "missing SSH user") {
		t.Fatalf("expected missing SSH user error, got: %v", err)
	}
}

func TestURLHelpers(t *testing.T) {
	t.Parallel()
	if got := urlBase("https://example.com:8443/path"); got != "https://example.com:8443" {
		t.Fatalf("urlBase() = %q", got)
	}
	if got := urlHost("https://example.com:8443/path"); got != "example.com" {
		t.Fatalf("urlHost() = %q", got)
	}
	if got := sqlQuote(`a\b'c`); got != `'a\\b\'c'` {
		t.Fatalf("sqlQuote() = %q", got)
	}
}
