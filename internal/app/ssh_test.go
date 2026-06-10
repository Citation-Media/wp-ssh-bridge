package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunSSHQuietSuccessSuppressesSuccessfulOutput(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, `#!/bin/sh
printf 'stdout should stay hidden\n'
printf 'stderr should stay hidden\n' >&2
exit 0
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.runStep("Logging into pull source over SSH", "Logged into pull source over SSH", func() error {
		return app.runSSHQuietSuccess(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com"}, "true")
	})
	if err != nil {
		t.Fatalf("runSSHQuietSuccess() error = %v", err)
	}

	output := stdout.String() + stderr.String()
	if strings.Contains(output, "stdout should stay hidden") || strings.Contains(output, "stderr should stay hidden") {
		t.Fatalf("successful SSH probe leaked remote output:\n%s", output)
	}
	if strings.Contains(output, "• Logging into pull source over SSH") {
		t.Fatalf("step start output should stay hidden:\n%s", output)
	}
	if !strings.Contains(output, "✓ Logged into pull source over SSH") {
		t.Fatalf("step success output missing:\n%s", output)
	}
}

func TestRunSSHQuietSuccessPrintsFailureDiagnostics(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, `#!/bin/sh
printf 'Permission denied (publickey).\n' >&2
exit 255
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.runSSHQuietSuccess(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com"}, "true")
	if err == nil {
		t.Fatal("runSSHQuietSuccess() succeeded unexpectedly")
	}
	if !strings.Contains(stderr.String(), "remote │ Permission denied (publickey).") {
		t.Fatalf("missing prefixed SSH failure diagnostics:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestRunSSHWithFilteredWarningsDropsSuccessfulStdout(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, `#!/bin/sh
printf 'Success: Exported to /tmp/db.sql\n'
printf 'Warning: runtime warning\n' >&2
exit 0
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	if err := app.runSSHWithFilteredWarnings(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com"}, "wp db export"); err != nil {
		t.Fatalf("runSSHWithFilteredWarnings() error = %v", err)
	}
	if strings.Contains(stdout.String(), "Success: Exported") || strings.Contains(stderr.String(), "Success: Exported") {
		t.Fatalf("remote stdout leaked into CLI output:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "Warning: runtime warning") {
		t.Fatalf("successful warning diagnostics should stay hidden:\n%s", stderr.String())
	}
}

func TestRunSSHWithFilteredWarningsPrintsFailureStdout(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, `#!/bin/sh
printf 'Fatal details on stdout\n'
printf 'Fatal details on stderr\n' >&2
exit 1
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	err := app.runSSHWithFilteredWarnings(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com"}, "wp db export")
	if err == nil {
		t.Fatal("runSSHWithFilteredWarnings() succeeded unexpectedly")
	}
	if !strings.Contains(stdout.String(), "remote │ Fatal details on stdout") {
		t.Fatalf("missing captured stdout diagnostics:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "remote │ Fatal details on stderr") {
		t.Fatalf("missing captured stderr diagnostics:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func installFakeSSH(t *testing.T, dir string, script string) {
	t.Helper()
	installFakeCommand(t, dir, "ssh", script)
}

func installFakeCommand(t *testing.T, dir string, name string, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake command is Unix-only")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
