package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderYAMLUsesWebServiceAndBinary(t *testing.T) {
	t.Parallel()
	body := providerYAML("live", "custom-bin")
	for _, want := range []string{
		"service: web",
		"custom-bin provider auth",
		"custom-bin provider db-pull",
		"custom-bin provider files-pull",
		"custom-bin provider files-import",
		"custom-bin provider db-push",
		"custom-bin provider files-push",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("provider YAML missing %q:\n%s", want, body)
		}
	}
	hook := hookYAML("custom-bin")
	for _, want := range []string{"post-pull:", "post-push:", "exec: custom-bin provider post-push"} {
		if !strings.Contains(hook, want) {
			t.Fatalf("hook YAML missing %q:\n%s", want, hook)
		}
	}
}

func TestInstallProviderFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hostBinary := filepath.Join(dir, ".ddev", "bin", binaryName)
	containerBinary := filepath.Join(dir, ".ddev", "bin", binaryName+"-container")
	if err := os.MkdirAll(filepath.Dir(hostBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostBinary, []byte("host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(containerBinary, []byte("container"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Provider: "live"}
	if err := installProviderFiles(dir, cfg, hostBinary); err != nil {
		t.Fatalf("installProviderFiles() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join(dir, ".ddev", "providers", "live.yaml"),
		filepath.Join(dir, ".ddev", hookConfigName),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated file %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".ddev", "wp-ssh-plugins.txt")); !os.IsNotExist(err) {
		t.Fatalf("provider install should not create default plugin list, got err: %v", err)
	}
	provider, err := os.ReadFile(filepath.Join(dir, ".ddev", "providers", "live.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(provider), "/var/www/html/.ddev/bin/ddev-wp-ssh-container provider db-pull") {
		t.Fatalf("provider did not use container companion binary:\n%s", string(provider))
	}
}

func TestDDEVProviderBinaryPathRequiresCompanionForHostBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hostBinary := filepath.Join(dir, ".ddev", "bin", binaryName)
	if err := os.MkdirAll(filepath.Dir(hostBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostBinary, []byte("host"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ddevProviderBinaryPath(dir, hostBinary); err == nil {
		t.Fatal("ddevProviderBinaryPath accepted host binary without container companion")
	}
}

func TestDefaultPluginListIncludesBackupMigrationAndSMTPPlugins(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"updraftplus",
		"duplicator",
		"all-in-one-wp-migration",
		"migrate-guru",
		"wp-migrate-db",
		"wp-staging",
		"wp-mail-smtp",
		"post-smtp",
		"easy-wp-smtp",
		"fluent-smtp",
		"mailgun",
		"sendgrid-email-delivery-simplified",
		"wp-offload-ses-lite",
	} {
		if !strings.Contains(defaultPluginList, "\n"+want+"\n") {
			t.Fatalf("default plugin list missing %q:\n%s", want, defaultPluginList)
		}
	}
}
