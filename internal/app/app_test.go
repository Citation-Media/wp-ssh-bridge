package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitSilentAllowsPartialConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir

	if err := app.commandInit([]string{"--silent"}); err != nil {
		t.Fatalf("commandInit() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".wp-ssh.yaml")); err != nil {
		t.Fatalf("expected standalone config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wp-ssh-plugins.txt")); !os.IsNotExist(err) {
		t.Fatalf("standalone init should not create default plugin list, got err: %v", err)
	}
}

func TestInitSilentSupportsCustomConfigFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir

	if err := app.commandInit([]string{"--silent", "--config-file", "config/wp-ssh.yaml", "--user", "deploy"}); err != nil {
		t.Fatalf("commandInit() error = %v\nstderr:\n%s", err, stderr.String())
	}
	customPath := filepath.Join(dir, "config", "wp-ssh.yaml")
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("expected custom config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".wp-ssh.yaml")); !os.IsNotExist(err) {
		t.Fatalf("default standalone config should not be written, got err: %v", err)
	}
}

func TestInitSilentSupportsConfigFileEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WP_SSH_CONFIG_FILE", "config/env-wp-ssh.yaml")
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir

	if err := app.commandInit([]string{"--silent", "--user", "deploy"}); err != nil {
		t.Fatalf("commandInit() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "env-wp-ssh.yaml")); err != nil {
		t.Fatalf("expected env config file: %v", err)
	}
}

func TestDomainsAddConfiguresPullAndInversePushMappings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir

	if err := app.commandInit([]string{"--silent"}); err != nil {
		t.Fatalf("commandInit() error = %v\nstderr:\n%s", err, stderr.String())
	}
	if err := app.commandDomains([]string{"add", "--old", "example.com", "--new", "example.ddev.site"}); err != nil {
		t.Fatalf("commandDomains(add) error = %v\nstderr:\n%s", err, stderr.String())
	}

	cfg, err := readConfigFile(filepath.Join(dir, ".wp-ssh.yaml"))
	if err != nil {
		t.Fatalf("readConfigFile() error = %v", err)
	}
	if len(cfg.PullDomainReplacements) != 1 || cfg.PullDomainReplacements[0] != (DomainReplacement{Old: "example.com", New: "example.ddev.site"}) {
		t.Fatalf("pull replacements = %#v", cfg.PullDomainReplacements)
	}
	if len(cfg.PushDomainReplacements) != 1 || cfg.PushDomainReplacements[0] != (DomainReplacement{Old: "example.ddev.site", New: "example.com"}) {
		t.Fatalf("push replacements = %#v", cfg.PushDomainReplacements)
	}
}

func TestPullRejectsMigrationFlags(t *testing.T) {
	t.Parallel()
	if _, err := parseConfigCommand("pull", []string{"--migrate"}, new(strings.Builder)); err == nil {
		t.Fatal("parseConfigCommand(pull) accepted --migrate")
	}
	if _, err := parseConfigCommand("pull", []string{"--db-host", "db.example.com"}, new(strings.Builder)); err == nil {
		t.Fatal("parseConfigCommand(pull) accepted --db-host")
	}
}

func TestMigrateRejectsDDEVAdapter(t *testing.T) {
	t.Parallel()
	ddev := adapterForRuntime(runtimeContext{Mode: modeDDEV, Root: t.TempDir()})
	if err := ensureMigrateAdapterSupported(ddev); err == nil {
		t.Fatal("ensureMigrateAdapterSupported() accepted the DDEV adapter")
	}
	standalone := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: t.TempDir()})
	if err := ensureMigrateAdapterSupported(standalone); err != nil {
		t.Fatalf("ensureMigrateAdapterSupported() rejected the standalone adapter: %v", err)
	}
}

func TestMigrateCommandRequiresCredentialsForFilePull(t *testing.T) {
	t.Parallel()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = t.TempDir()

	err := app.commandMigrate([]string{
		"--silent",
		"--user", "deploy",
		"--host", "source.example.com",
		"--remote-path", "/var/www/html",
	})
	if err == nil {
		t.Fatal("commandMigrate() accepted file migration without credentials")
	}
	if !strings.Contains(err.Error(), "migrate requires target database credentials") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMigrateCommandAcceptsDBFlags(t *testing.T) {
	t.Parallel()
	opts, err := parseConfigCommand("migrate", []string{
		"--db-host", "db.example.com",
		"--db-name", "target",
		"--db-user", "user",
		"--db-password", "pass",
	}, new(strings.Builder))
	if err != nil {
		t.Fatalf("parseConfigCommand(migrate) error = %v", err)
	}
	opts.Migrate = true

	cfg := Config{
		MigrateDBHost:     opts.MigrateDBHost,
		MigrateDBName:     opts.MigrateDBName,
		MigrateDBUser:     opts.MigrateDBUser,
		MigrateDBPassword: opts.MigrateDBPassword,
	}
	if err := opts.validateMigrationCommand(cfg); err != nil {
		t.Fatalf("validateMigrationCommand() rejected complete credentials: %v", err)
	}
}

func TestPushRejectsMigrateFlag(t *testing.T) {
	t.Parallel()
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = t.TempDir()

	err := app.commandPush([]string{"--migrate"})
	if err == nil {
		t.Fatal("commandPush() accepted --migrate")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFillPullConfigDoesNotPromptForConfiguredRequiredValues(t *testing.T) {
	t.Parallel()
	cfg := Config{
		User:       "deploy",
		Host:       "example.com",
		RemotePath: "/var/www/html",
	}
	input := strings.NewReader("\n\n\n\nn\nn\n")
	output := bytes.Buffer{}
	prompter := newPrompter(input, &output)

	if err := prompter.fillPullConfig(&cfg); err != nil {
		t.Fatalf("fillPullConfig() error = %v", err)
	}
	if strings.Contains(output.String(), "SSH user") {
		t.Fatalf("prompted for configured SSH user:\n%s", output.String())
	}
	if cfg.User != "deploy" {
		t.Fatalf("SSH user changed unexpectedly: %q", cfg.User)
	}
}

func TestFillPushConfigDoesNotPromptForConfiguredRequiredValues(t *testing.T) {
	t.Parallel()
	cfg := Config{
		PushUser:       "release",
		PushHost:       "staging.example.com",
		PushRemotePath: "/var/www/html",
	}
	input := strings.NewReader("\n\n\n\n\nn\n")
	output := bytes.Buffer{}
	prompter := newPrompter(input, &output)

	if err := prompter.fillPushConfig(&cfg); err != nil {
		t.Fatalf("fillPushConfig() error = %v", err)
	}
	if strings.Contains(output.String(), "Push SSH user") {
		t.Fatalf("prompted for configured push SSH user:\n%s", output.String())
	}
	if cfg.PushUser != "release" {
		t.Fatalf("push SSH user changed unexpectedly: %q", cfg.PushUser)
	}
}
