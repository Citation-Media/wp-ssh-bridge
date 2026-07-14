package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPushRsyncExcludes(t *testing.T) {
	t.Parallel()
	excludes := strings.Join(buildPushRsyncExcludes(), "\n")
	for _, want := range []string{
		".git/",
		".ddev/",
		".wp-ssh/",
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

func TestDBPushCleansRemoteDumpAfterUploadFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(downloadsDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadsDir(dir), "db.sql.gz"), []byte("dump"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\n")
	installFakeCommand(t, dir, "rsync", "#!/bin/sh\nexit 1\n")

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	err := app.dbPush(context.Background(), dir, Config{
		PushUser:       "deploy",
		PushHost:       "example.com",
		PushRemotePath: "/var/www/html",
		PushURL:        "https://example.com",
	}, false)
	if err == nil {
		t.Fatal("dbPush() succeeded despite a failed database upload")
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "rm -f '/tmp/ddev-") {
		t.Fatalf("push should remove the remote dump after upload failure:\n%s", log)
	}
}

func TestPostPushWarnsWhenPushURLMissing(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "wp", `#!/bin/sh
exit 1
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.postPush(context.Background(), dir, Config{
		PushUser:       "deploy",
		PushHost:       "staging.example.com",
		PushRemotePath: "/var/www/html",
		LocalURL:       "https://local.test",
	})
	if err != nil {
		t.Fatalf("postPush() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "Skipping push URL replacement because no configured push replacement pairs exist and the local or push target URL could not be detected. Set push_url or WP_SSH_PUSH_URL.") {
		t.Fatalf("missing push URL replacement warning:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestPostPushUsesConfiguredPushDomainReplacements(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
exit 0
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.postPush(context.Background(), dir, Config{
		PushUser:       "deploy",
		PushHost:       "staging.example.com",
		PushRemotePath: "/var/www/html",
		PushDomainReplacements: []DomainReplacement{
			{Old: "example.ddev.site", New: "example.com"},
		},
	})
	if err != nil {
		t.Fatalf("postPush() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "'search-replace' 'example.ddev.site' 'example.com'") {
		t.Fatalf("ssh log missing configured push replacement:\n%s", log)
	}
	if !strings.Contains(log, "'--no-report'") {
		t.Fatalf("ssh log missing low-memory search-replace flag:\n%s", log)
	}
}

func TestRemoteWPCommandQuotesArguments(t *testing.T) {
	t.Parallel()
	target := RemoteTarget{RemotePath: "/home/site/public html", RemoteTmpDir: "/var/tmp"}
	command := remoteWPCommand(target, "search-replace", "https://local.test", "https://example.com")
	for _, want := range []string{
		"cd '/home/site/public html';",
		"WP_SSH_WP_CLI_PHAR='/var/tmp/wp-ssh-bridge-wp-cli.phar';",
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
