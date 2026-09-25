package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderYAMLUsesHostServiceAndBinary(t *testing.T) {
	t.Parallel()
	body := providerYAML("live", "custom-bin")
	for _, want := range []string{
		"service: host",
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
	for _, want := range []string{"post-pull:", "post-push:", `exec-host: "custom-bin provider post-push"`} {
		if !strings.Contains(hook, want) {
			t.Fatalf("hook YAML missing %q:\n%s", want, hook)
		}
	}
}

func TestProviderYAMLQuotesBinaryPathWithSpaces(t *testing.T) {
	t.Parallel()
	body := providerYAML("live", "/Users/me/My Tools/wp-ssh-bridge")
	want := `command: "'/Users/me/My Tools/wp-ssh-bridge' provider auth"`
	if !strings.Contains(body, want) {
		t.Fatalf("provider YAML did not quote binary path with spaces; missing %q:\n%s", want, body)
	}
}

func TestProviderDBPullFallsBackToSCPWhenRemoteRsyncIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", configFileName), []byte("pull_user: deploy\npull_host: example.com\npull_remote_path: /var/www/html\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installFakeCommand(t, dir, "ddev", "#!/bin/sh\nif [ \"$1\" = describe ] && [ \"$2\" = -j ]; then\n  printf '%s\\n' "+shellQuote(`{"type":"wordpress","app_root":"`+dir+`"}`)+"\nfi\n")

	sshLog := filepath.Join(dir, "ssh.log")
	rsyncLog := filepath.Join(dir, "rsync.log")
	// The fallback downloads the dump with "ssh <host> cat <file>", so the fake ssh
	// records that call and answers the rsync probe with failure.
	installFakeSSH(t, dir, "#!/bin/sh\ncase \"$*\" in\n  *'command -v rsync'*) exit 1 ;;\n  *' cat '*) printf '%s\\n' \"$*\" >> "+shellQuote(sshLog)+" ;;\n  *'cli info --format=json'*) printf '{\"php_binary_path\":\"/usr/bin/php\",\"wp_cli_phar_path\":\"phar:///usr/local/bin/wp\"}\\n' ;;\n  *reenable=*) printf 'reenable=\\n' ;;\nesac\n")
	installFakeCommand(t, dir, "rsync", "#!/bin/sh\nprintf '%s\\n' \"$*\" > "+shellQuote(rsyncLog)+"\nexit 1\n")

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir
	if err := app.commandProviderRuntime("db-pull", []string{"--project-root", dir}); err != nil {
		t.Fatalf("provider db-pull() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	logged, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("provider db-pull did not stream the dump through ssh: %v", err)
	}
	if !strings.Contains(string(logged), "deploy@example.com cat ") {
		t.Fatalf("fallback download did not run cat over ssh:\n%s", logged)
	}
	if _, err := os.Stat(rsyncLog); !os.IsNotExist(err) {
		t.Fatalf("provider db-pull should not use rsync when remote rsync is missing, got err: %v", err)
	}
	if !strings.Contains(stderr.String(), "Remote rsync not available — falling back to scp/tar transfer") {
		t.Fatalf("provider db-pull did not report fallback:\n%s", stderr.String())
	}
}

func TestProviderAuthAllowsMissingLocalRsync(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", configFileName), []byte("pull_user: deploy\npull_host: example.com\npull_remote_path: /var/www/html\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installFakeCommand(t, dir, "ddev", "#!/bin/sh\nif [ \"$1\" = describe ] && [ \"$2\" = -j ]; then\n  printf '%s\\n' "+shellQuote(`{"type":"wordpress","app_root":"`+dir+`"}`)+"\nfi\n")
	installFakeSSH(t, dir, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir
	if err := app.commandProviderRuntime("auth", []string{"--project-root", dir}); err != nil {
		t.Fatalf("provider auth() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
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
	cfg := Config{Provider: "live"}
	if err := installProviderFiles(dir, cfg, "wp-ssh-bridge"); err != nil {
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
}

func TestInstallProviderFilesPersistsDDEVAdditionalHostnames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, ".ddev", "config.yaml")
	if err := os.WriteFile(configPath, []byte("type: wordpress\nadditional_hostnames: [existing]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Provider: "live",
		PullDomainReplacements: []DomainReplacement{
			{Old: "https://example.com", New: "https://site.ddev.site"},
			{Old: "https://shop.example.com", New: "https://shop.ddev.site"},
		},
		PushDomainReplacements: []DomainReplacement{
			{Old: "blog.ddev.site", New: "blog.example.com"},
		},
	}

	if err := installProviderFiles(dir, cfg, "wp-ssh-bridge"); err != nil {
		t.Fatalf("installProviderFiles() error = %v", err)
	}

	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"additional_hostnames:",
		"  - existing",
		"  - blog",
		"  - shop",
		"  - site",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "site.ddev.site") {
		t.Fatalf("DDEV hostname should be stored without project_tld suffix:\n%s", text)
	}
}

func TestDDEVHostnamesUseResolvedTLDEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DDEV_TLD", "ddev.test")
	got, err := ddevHostnamesForReplacements(dir, []DomainReplacement{{New: "site.ddev.test"}})
	if err != nil {
		t.Fatalf("ddevHostnamesForReplacements() error = %v", err)
	}
	if len(got) != 1 || got[0] != "site" {
		t.Fatalf("ddevHostnamesForReplacements() = %#v, want []string{\"site\"}", got)
	}
}

func TestInstallProviderFilesUsesRelativeProjectBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, ".ddev", "bin", "wp-ssh-bridge")
	cfg := Config{Provider: "live"}

	if err := installProviderFiles(dir, cfg, binary); err != nil {
		t.Fatalf("installProviderFiles() error = %v", err)
	}

	provider, err := os.ReadFile(filepath.Join(dir, ".ddev", "providers", "live.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hook, err := os.ReadFile(filepath.Join(dir, ".ddev", hookConfigName))
	if err != nil {
		t.Fatal(err)
	}
	generated := string(provider) + string(hook)
	if strings.Contains(generated, dir) {
		t.Fatalf("generated DDEV YAML contains absolute project path %q:\n%s", dir, generated)
	}
	if !strings.Contains(generated, "./.ddev/bin/wp-ssh-bridge provider auth") {
		t.Fatalf("generated provider YAML missing relative binary path:\n%s", generated)
	}
	if !strings.Contains(generated, "./.ddev/bin/wp-ssh-bridge provider post-pull") {
		t.Fatalf("generated hook YAML missing relative binary path:\n%s", generated)
	}
}

func TestPortableProviderBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if got := portableProviderBinary(dir, filepath.Join(dir, ".ddev", "bin", "wp-ssh-bridge")); got != "./.ddev/bin/wp-ssh-bridge" {
		t.Fatalf("portableProviderBinary(project binary) = %q", got)
	}
	if got := portableProviderBinary(dir, "/usr/local/bin/wp-ssh-bridge"); got != "wp-ssh-bridge" {
		t.Fatalf("portableProviderBinary(global binary) = %q", got)
	}
	if got := portableProviderBinary(dir, "tools/wp-ssh-bridge"); got != "tools/wp-ssh-bridge" {
		t.Fatalf("portableProviderBinary(relative binary) = %q", got)
	}
}

func TestDefaultPluginListIncludesBackupMigrationSMTPAndMonitoringPlugins(t *testing.T) {
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
		"patchstack",
		"wp-health",
	} {
		if !strings.Contains(defaultPluginList, "\n"+want+"\n") {
			t.Fatalf("default plugin list missing %q:\n%s", want, defaultPluginList)
		}
	}
}
