package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandalonePullKeepsSilentRunValuesOneShot(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wp-ssh.yaml")
	writeFile(t, configPath, "pull_destination: \"prod\"\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	cfg := Config{Destination: "deploy@other.example.com", CloneDBPassword: "sekret"}
	if err := adapter.PreparePull(app, cfg, configOptions{Silent: true}); err != nil {
		t.Fatalf("PreparePull() error = %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pull_destination: \"prod\"\n" {
		t.Fatalf("a --silent run must not rewrite the config:\n%s", got)
	}
}

func TestStandalonePromptedPullNeverPersistsThePassword(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".wp-ssh.yaml")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	cfg := defaultConfig()
	cfg.Destination = "deploy@example.com"
	cfg.CloneDBPassword = "from-environment"
	if err := adapter.PreparePull(app, cfg, configOptions{}); err != nil {
		t.Fatalf("PreparePull() error = %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "deploy@example.com") || strings.Contains(string(got), "from-environment") {
		t.Fatalf("prompted values should persist without the password:\n%s", got)
	}
}
