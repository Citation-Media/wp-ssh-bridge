package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPushRsyncExcludes(t *testing.T) {
	t.Parallel()
	excludes := strings.Join(buildPushRsyncExcludes(), "\n")
	for _, want := range []string{
		".ddev/",
		"wp-config.php",
		"wp-config-ddev.php",
	} {
		if !strings.Contains(excludes, want) {
			t.Fatalf("push excludes missing %q:\n%s", want, excludes)
		}
	}
	if strings.Contains(excludes, "wp-content/uploads/") {
		t.Fatalf("push excludes should not skip uploads:\n%s", excludes)
	}
}

func TestPushURLCacheIsTargetSpecific(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev", ".downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := RemoteTarget{User: "deploy", Host: "staging.example.com", RemotePath: "/var/www/html"}
	if err := writePushURLCache(dir, target, "https://staging.example.com/"); err != nil {
		t.Fatalf("writePushURLCache() error = %v", err)
	}
	if got := readPushURLCache(dir, target); got != "https://staging.example.com" {
		t.Fatalf("readPushURLCache() = %q", got)
	}
	other := RemoteTarget{User: "deploy", Host: "prod.example.com", RemotePath: "/var/www/html"}
	if got := readPushURLCache(dir, other); got != "" {
		t.Fatalf("readPushURLCache() for other target = %q", got)
	}
}

func TestRemoteWPCommandQuotesArguments(t *testing.T) {
	t.Parallel()
	target := RemoteTarget{RemotePath: "/home/site/public html", RemoteTmpDir: "/var/tmp"}
	command := remoteWPCommand(target, "search-replace", "https://local.test", "https://example.com")
	for _, want := range []string{
		"cd '/home/site/public html';",
		"WP_SSH_WP_CLI_PHAR='/var/tmp/ddev-wp-ssh-wp-cli.phar';",
		"curl -fsSL -o \"$WP_SSH_WP_CLI_PHAR\"",
		"wget -q -O \"$WP_SSH_WP_CLI_PHAR\"",
		"wp_ssh_wp",
		"'search-replace'",
		"'https://local.test'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote command missing %q:\n%s", want, command)
		}
	}
}

func TestLocalWPCommandUsesDownloadedPhar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	phar := localWPCLIPharPath(dir)
	if err := os.MkdirAll(filepath.Dir(phar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phar, []byte("#!/usr/bin/env php\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	name, args := localWPCommand(dir, Config{}, "db", "prefix")
	if commandExists("php") {
		if name != "php" || len(args) == 0 || args[0] != phar {
			t.Fatalf("localWPCommand() did not use php phar fallback: name=%q args=%#v", name, args)
		}
	} else if name != phar {
		t.Fatalf("localWPCommand() direct phar name = %q, want %q", name, phar)
	}
	if !strings.Contains(strings.Join(args, " "), "db prefix") {
		t.Fatalf("localWPCommand() missing original args: name=%q args=%#v", name, args)
	}
}
