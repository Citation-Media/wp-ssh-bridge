package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pageBuilderFakeWP registers Elementor, reports Bricks as missing, and fails Beaver Builder.
const pageBuilderFakeWP = `case "$*" in
*elementor*) exit 0 ;;
*bricks*) echo "Error: 'bricks' is not a registered wp command. See 'wp help' for available commands." >&2; exit 1 ;;
*beaver*) echo "Error: Beaver Builder exploded." >&2; exit 1 ;;
*) exit 1 ;;
esac
`

func TestRebuildLocalPageBuilderCachesLoadsPluginsAndReportsFailures(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "wp-args.txt")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(argsFile)+`
`+pageBuilderFakeWP)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	app.rebuildLocalPageBuilderCaches(context.Background(), dir, Config{LocalWPPath: "."})

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(argsBytes)), "\n") {
		isBuilderCommand := strings.Contains(line, "elementor") || strings.Contains(line, "bricks") || strings.Contains(line, "beaver")
		if isBuilderCommand && strings.Contains(line, "--skip-plugins") {
			t.Errorf("page builder command must load plugins: %s", line)
		}
	}
	if !strings.Contains(string(argsBytes), "elementor flush-css") {
		t.Errorf("missing Elementor command:\n%s", argsBytes)
	}
	if !strings.Contains(stdout.String(), "Local Elementor CSS rebuilt") {
		t.Errorf("missing Elementor success:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "Bricks") {
		t.Errorf("unregistered Bricks command should be skipped silently:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "Could not rebuild local Beaver Builder CSS") || !strings.Contains(stderr.String(), "Beaver Builder exploded") {
		t.Errorf("missing Beaver Builder warning with details:\n%s", stderr.String())
	}
}

func TestRebuildRemotePageBuilderCachesUsesNetworkFlagOnMultisite(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ssh-args.txt")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(argsFile)+`
case "$*" in
*"'core' 'is-installed' '--network'"*) exit 0 ;;
esac
`+pageBuilderFakeWP)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{PushUser: "deploy", PushHost: "example.com", PushRemotePath: "/var/www/html"}
	app.rebuildRemotePageBuilderCaches(context.Background(), dir, cfg)

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argsBytes), "'--allow-root' 'elementor' 'flush-css' '--network'") {
		t.Errorf("missing multisite Elementor command without skip flags:\n%s", argsBytes)
	}
	if !strings.Contains(stdout.String(), "Remote Elementor CSS rebuilt") {
		t.Errorf("missing Elementor success:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Could not rebuild remote Beaver Builder CSS") {
		t.Errorf("missing Beaver Builder warning:\n%s", stderr.String())
	}
}

func TestRebuildPageBuilderCachesHonorsSkipCacheRebuild(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "wp-args.txt")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(argsFile)+`
`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	app.rebuildLocalPageBuilderCaches(context.Background(), dir, Config{LocalWPPath: ".", SkipCacheRebuild: true})

	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatalf("expected no WP-CLI calls with SkipCacheRebuild, stat err = %v", err)
	}
}

func TestSkipCacheRebuildConfigEnvAndFlag(t *testing.T) {
	cfg := Config{}
	cfg.applyEnv([]string{"WP_SSH_SKIP_CACHE_REBUILD=true"})
	if !cfg.SkipCacheRebuild {
		t.Error("expected WP_SSH_SKIP_CACHE_REBUILD to set SkipCacheRebuild")
	}

	opts, err := parseConfigCommand("pull", []string{"--skip-cache-rebuild"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfigCommand() error = %v", err)
	}
	if !opts.apply(Config{}).SkipCacheRebuild {
		t.Error("expected --skip-cache-rebuild to set SkipCacheRebuild")
	}

	path := filepath.Join(t.TempDir(), ".wp-ssh.yaml")
	if err := os.WriteFile(path, []byte("skip_cache_rebuild: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile() error = %v", err)
	}
	if !loaded.SkipCacheRebuild {
		t.Error("expected skip_cache_rebuild config key to set SkipCacheRebuild")
	}
}
