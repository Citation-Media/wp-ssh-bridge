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
