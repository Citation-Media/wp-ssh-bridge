package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteMariaDBCompatibilityCheckCommand(t *testing.T) {
	t.Parallel()
	command := remoteMariaDBCompatibilityCheckCommand()
	for _, want := range []string{
		"mysql --version",
		"grep -qi 'MariaDB'",
		"! command -v mariadb",
		"command -v mysqldump",
		"! command -v mariadb-dump",
		"printf 'required\\n'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("compatibility check missing %q:\n%s", want, command)
		}
	}
}

func TestRemoteMariaDBCompatibilityCommandsCreateAndRemoveAliases(t *testing.T) {
	t.Parallel()
	setup, cleanup := remoteMariaDBCompatibilityCommands("/var/tmp", "request-123", true)
	for _, want := range []string{
		"WP_SSH_MARIADB_COMPAT_DIR='/var/tmp/.wp-ssh-bridge-mariadb-request-123'",
		"WP_SSH_MARIADB_COMPAT_CREATED=0",
		"mkdir -m 700 \"$WP_SSH_MARIADB_COMPAT_DIR\"",
		"if ! command -v mariadb >/dev/null 2>&1; then",
		"ln -sf \"$(command -v mysql)\" \"$WP_SSH_MARIADB_COMPAT_DIR/mariadb\"",
		"if ! command -v mariadb-dump >/dev/null 2>&1 && command -v mysqldump >/dev/null 2>&1; then",
		"ln -sf \"$(command -v mysqldump)\" \"$WP_SSH_MARIADB_COMPAT_DIR/mariadb-dump\"",
		"PATH=\"$WP_SSH_MARIADB_COMPAT_DIR:$PATH\"; export PATH",
	} {
		if !strings.Contains(setup, want) {
			t.Fatalf("compatibility setup missing %q:\n%s", want, setup)
		}
	}
	for _, want := range []string{
		"if [ \"${WP_SSH_MARIADB_COMPAT_CREATED:-0}\" = 1 ]; then",
		"rm -f '/var/tmp/.wp-ssh-bridge-mariadb-request-123/mariadb' '/var/tmp/.wp-ssh-bridge-mariadb-request-123/mariadb-dump' || true",
		"rmdir '/var/tmp/.wp-ssh-bridge-mariadb-request-123' 2>/dev/null || true",
	} {
		if !strings.Contains(cleanup, want) {
			t.Fatalf("compatibility cleanup missing %q:\n%s", want, cleanup)
		}
	}
}

func TestRemoteMariaDBCompatibilityCommandsSkipAliasesWhenNotRequired(t *testing.T) {
	t.Parallel()
	setup, cleanup := remoteMariaDBCompatibilityCommands("/var/tmp", "request-123", false)
	if setup != "" || cleanup != ":" {
		t.Fatalf("disabled compatibility commands = (%q, %q), want (\"\", \":\")", setup, cleanup)
	}
}

func TestDetectRemoteMariaDBCompatibility(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, "#!/bin/sh\nprintf 'required\\n'\n")

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	required, err := app.detectRemoteMariaDBCompatibility(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com"})
	if err != nil {
		t.Fatalf("detectRemoteMariaDBCompatibility() error = %v", err)
	}
	if !required {
		t.Fatal("detectRemoteMariaDBCompatibility() = false, want true")
	}
}

func TestPreflightRecordsRemoteMariaDBCompatibility(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, "#!/bin/sh\nprintf 'required\\n'\n")

	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	err := app.runPreflight(context.Background(), preflightPlan{
		ProjectRoot: dir,
		Remotes: []preflightRemoteNeeds{{
			Label:                         "pull source",
			Target:                        target,
			NeedMariaDBCompatibilityCheck: true,
		}},
	}, Config{})
	if err != nil {
		t.Fatalf("runPreflight() error = %v", err)
	}
	if !app.needsRemoteMariaDBCompatibility(target) {
		t.Fatal("preflight should record that the target requires compatibility aliases")
	}
	if !strings.Contains(stdout.String(), "Pull source MariaDB client compatibility enabled") {
		t.Fatalf("preflight should report enabled compatibility:\n%s", stdout.String())
	}
}

func TestRemoteMariaDBCompatibilityIsScopedToTarget(t *testing.T) {
	t.Parallel()
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	app.setRemoteMariaDBCompatibility(target, true)
	if !app.needsRemoteMariaDBCompatibility(target) {
		t.Fatal("configured target should require compatibility aliases")
	}
	if app.needsRemoteMariaDBCompatibility(RemoteTarget{User: "deploy", Host: "other.example.com", RemotePath: "/var/www/html"}) {
		t.Fatal("different target should not require compatibility aliases")
	}
}

func TestDBPullRemovesMariaDBCompatibilityAliasesAfterExport(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\n")
	installFakeCommand(t, dir, "scp", "#!/bin/sh\nexit 0\n")

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html", RemoteTmpDir: "/var/tmp"}
	app.setRemoteMariaDBCompatibility(target, true)
	err := app.dbPull(context.Background(), dir, Config{
		User:         target.User,
		Host:         target.Host,
		RemotePath:   target.RemotePath,
		RemoteTmpDir: target.RemoteTmpDir,
	}, true)
	if err != nil {
		t.Fatalf("dbPull() error = %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	command := string(log)
	for _, want := range []string{
		"trap cleanup INT TERM HUP EXIT",
		"WP_SSH_MARIADB_COMPAT_DIR='/var/tmp/.wp-ssh-bridge-mariadb-",
		"ln -sf \"$(command -v mysql)\"",
		"ln -sf \"$(command -v mysqldump)\"",
		"rmdir '/var/tmp/.wp-ssh-bridge-mariadb-",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("database export command missing %q:\n%s", want, command)
		}
	}
	if strings.LastIndex(command, "rmdir '/var/tmp/.wp-ssh-bridge-mariadb-") > strings.LastIndex(command, "trap - EXIT") {
		t.Fatalf("compatibility aliases should be removed before disabling the cleanup trap:\n%s", command)
	}
}

func TestDBPullCleansRemoteAndPartialLocalDumpAfterDownloadFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\n")
	installFakeCommand(t, dir, "scp", `#!/bin/sh
for arg do
  last=$arg
done
printf 'partial dump' > "$last"
exit 1
`)

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	err := app.dbPull(context.Background(), dir, Config{
		User:       "deploy",
		Host:       "example.com",
		RemotePath: "/var/www/html",
	}, true)
	if err == nil {
		t.Fatal("dbPull() succeeded despite a failed database download")
	}

	if _, err := os.Stat(filepath.Join(downloadsDir(dir), "db.sql.gz")); !os.IsNotExist(err) {
		t.Fatalf("partial local dump should be removed, got err: %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(commands) < 2 {
		t.Fatalf("expected export and deferred cleanup SSH commands:\n%s", log)
	}
	cleanup := commands[len(commands)-1]
	if !strings.Contains(cleanup, "rm -f '/tmp/ddev-") || strings.Contains(cleanup, "wp_ssh_wp") {
		t.Fatalf("last SSH command should clean the remote dump after download failure:\n%s", cleanup)
	}
}
