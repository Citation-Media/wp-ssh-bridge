package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeWPConfigContents(t *testing.T) {
	t.Parallel()
	input := `<?php
define('DB_NAME', 'prod');
define('DB_USER', 'prod');
define('COOKIE_DOMAIN', $_SERVER['HTTP_HOST']);
/* That's all, stop editing! Happy publishing. */
require_once ABSPATH . 'wp-settings.php';
`

	got := sanitizeWPConfigContents(input)
	for _, removed := range []string{"DB_NAME", "DB_USER', 'prod"} {
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

	excludeList, err := buildRsyncExcludes(dir, Config{PluginRemoveFile: ".ddev/extra-plugins.txt"})
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

	excludeList, err = buildRsyncExcludes(dir, Config{CloneImages: true})
	if err != nil {
		t.Fatalf("buildRsyncExcludes() with CloneImages error = %v", err)
	}
	excludes = strings.Join(excludeList, "\n")
	if strings.Contains(excludes, "wp-content/uploads/") {
		t.Fatalf("clone images should not exclude uploads:\n%s", excludes)
	}

	if err := os.WriteFile(filepath.Join(dir, ".ddev", "bad-plugins.txt"), []byte("../secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildRsyncExcludes(dir, Config{PluginRemoveFile: ".ddev/bad-plugins.txt"}); err == nil {
		t.Fatal("buildRsyncExcludes() accepted invalid plugin block list")
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

func TestUniqueReplacementPairsRemovesDuplicatesAndNoops(t *testing.T) {
	t.Parallel()
	pairs := uniqueReplacementPairs([]replacementPair{
		{old: "https://example.com", new: "https://local.test"},
		{old: "http://example.com", new: "https://local.test"},
		{old: "https://example.com", new: "https://local.test"},
		{old: "https://local.test", new: "https://local.test"},
		{old: "", new: "https://local.test"},
	})

	if len(pairs) != 2 {
		t.Fatalf("uniqueReplacementPairs() length = %d, pairs = %#v", len(pairs), pairs)
	}
	if pairs[0].old != "https://example.com" || pairs[1].old != "http://example.com" {
		t.Fatalf("uniqueReplacementPairs() kept unexpected order: %#v", pairs)
	}
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
