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
	command := remoteWPCommand("/home/site/public html", "search-replace", "https://local.test", "https://example.com")
	for _, want := range []string{
		"cd '/home/site/public html';",
		"command -v wp >/dev/null;",
		"'search-replace'",
		"'https://local.test'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote command missing %q:\n%s", want, command)
		}
	}
}
