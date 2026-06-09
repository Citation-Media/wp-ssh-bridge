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

	excludes := strings.Join(buildRsyncExcludes(dir, Config{PluginRemoveFile: ".ddev/extra-plugins.txt"}), "\n")
	for _, want := range []string{
		"wp-content/uploads/",
		"wp-content/plugins/updraftplus/",
		"wp-content/plugins/plugin-file.php",
	} {
		if !strings.Contains(excludes, want) {
			t.Fatalf("excludes missing %q:\n%s", want, excludes)
		}
	}

	excludes = strings.Join(buildRsyncExcludes(dir, Config{CloneImages: true}), "\n")
	if strings.Contains(excludes, "wp-content/uploads/") {
		t.Fatalf("clone images should not exclude uploads:\n%s", excludes)
	}
}

func TestProviderAuthValidatesMissingDDEVUserBeforeSSHAgent(t *testing.T) {
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
	if strings.Contains(err.Error(), "ssh-add") || strings.Contains(err.Error(), "ddev auth ssh") {
		t.Fatalf("validated SSH agent before missing config: %v", err)
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
