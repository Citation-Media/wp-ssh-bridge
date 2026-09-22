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

func TestSSHArgvTakesThePortFromTheDestination(t *testing.T) {
	t.Parallel()
	target := RemoteTarget{Destination: "deploy@production.example.com:2222"}
	args := sshArgv(target, sshTarget(target), "true")
	want := []string{"ssh", "-p", "2222", "-o", "BatchMode=yes"}
	for i, value := range want {
		if args[i] != value {
			t.Fatalf("sshArgv()[%d] = %q, want %q in %v", i, args[i], value, args)
		}
	}
	if args[len(args)-2] != "deploy@production.example.com" || args[len(args)-1] != "true" {
		t.Fatalf("sshArgv() did not end with destination and command: %v", args)
	}
	if got := sshCommandString(target); !strings.HasPrefix(got, "ssh -p 2222 -o BatchMode=yes") {
		t.Fatalf("sshCommandString() = %q", got)
	}
}

func TestSSHArgvOmitsThePortFlagByDefault(t *testing.T) {
	t.Parallel()
	args := sshArgv(RemoteTarget{User: "deploy", Host: "example.com"})
	if args[0] != "ssh" || args[1] != "-o" {
		t.Fatalf("sshArgv() without a port = %v", args)
	}
}

func TestSSHTargetPrefersTheDestinationAddress(t *testing.T) {
	t.Parallel()
	cases := map[RemoteTarget]string{
		{Destination: "prod"}:                                     "prod",
		{Destination: "ssh://deploy@example.com:2222"}:            "deploy@example.com",
		{User: "deploy", Host: "example.com"}:                     "deploy@example.com",
		{Host: "example.com"}:                                     "example.com",
		{Destination: "deploy@example.com", User: "x", Host: "y"}: "deploy@example.com",
	}
	for target, want := range cases {
		if got := sshTarget(target); got != want {
			t.Errorf("sshTarget(%+v) = %q, want %q", target, got, want)
		}
	}
	if got := (RemoteTarget{Destination: "prod:2200", Port: "1"}).port(); got != "2200" {
		t.Errorf("port() should come from the destination, got %q", got)
	}
}

func TestDownloadOverSSHWritesRemoteStdoutToTheLocalFile(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, "#!/bin/sh\nprintf 'dump-bytes'\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	local := filepath.Join(dir, "db.sql.gz")
	target := RemoteTarget{Destination: "deploy@example.com"}
	if err := app.downloadOverSSH(context.Background(), dir, target, "/tmp/db.sql.gz", local); err != nil {
		t.Fatalf("downloadOverSSH() error = %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "dump-bytes" {
		t.Fatalf("downloaded content = %q", got)
	}
}

func TestUploadOverSSHStreamsTheLocalFileToRemoteStdin(t *testing.T) {
	dir := t.TempDir()
	captured := filepath.Join(dir, "captured")
	installFakeSSH(t, dir, "#!/bin/sh\ncat > "+shellQuote(captured)+"\nprintf '%s\\n' \"$*\" > "+shellQuote(captured+".args")+"\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	local := filepath.Join(dir, "db.sql.gz")
	if err := os.WriteFile(local, []byte("local-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := RemoteTarget{Destination: "deploy@example.com"}
	if err := app.uploadOverSSH(context.Background(), dir, target, local, "/tmp/db.sql.gz"); err != nil {
		t.Fatalf("uploadOverSSH() error = %v", err)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "local-bytes" {
		t.Fatalf("uploaded content = %q", got)
	}
	args, _ := os.ReadFile(captured + ".args")
	if !strings.Contains(string(args), "deploy@example.com cat > '/tmp/db.sql.gz'") {
		t.Fatalf("upload command = %q", args)
	}
}
