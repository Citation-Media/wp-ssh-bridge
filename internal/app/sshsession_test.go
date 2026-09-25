package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func installLoggingFakeSSH(t *testing.T, dir string, body string) string {
	t.Helper()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\n"+body)
	return logPath
}

func readSSHLog(t *testing.T, logPath string) []string {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(contents)), "\n")
}

func controlPathArg(args []string) string {
	for _, arg := range args {
		if value, ok := strings.CutPrefix(arg, "ControlPath="); ok {
			return value
		}
	}
	return ""
}

func TestSSHArgsStayDirectWithoutSessionSharing(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, "exit 1\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com"}

	if err := app.openSSHSession(context.Background(), dir, target); err != nil {
		t.Fatalf("openSSHSession() without sharing error = %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("openSSHSession() without sharing should not start ssh, got err: %v", err)
	}
	if path := controlPathArg(app.sshArgv(target)); path != "" {
		t.Fatalf("sshArgs() without sharing should not use a control socket, got %q", path)
	}
}

func TestOpenSSHSessionSharesOneConnectionUntilClosed(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, "exit 0\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.sshSessions = &sshSessionPool{}
	target := RemoteTarget{User: "deploy", Host: "example.com", Port: "2222", RemotePath: "/var/www/html"}
	sameLogin := RemoteTarget{User: "deploy", Host: "example.com", Port: "2222", RemotePath: "/srv/other"}

	for _, open := range []RemoteTarget{target, sameLogin, target} {
		if err := app.openSSHSession(context.Background(), dir, open); err != nil {
			t.Fatalf("openSSHSession() error = %v", err)
		}
	}
	commands := readSSHLog(t, logPath)
	if len(commands) != 1 {
		t.Fatalf("one login should start exactly one shared connection, got:\n%s", strings.Join(commands, "\n"))
	}
	for _, want := range []string{"-p 2222", "BatchMode=yes", "ControlMaster=yes", "ControlPersist=10m", "-N -f", "deploy@example.com"} {
		if !strings.Contains(commands[0], want) {
			t.Fatalf("shared connection command missing %q:\n%s", want, commands[0])
		}
	}

	controlPath := controlPathArg(app.sshArgv(sameLogin))
	if !strings.HasPrefix(controlPath, "/tmp/wpssh-") {
		t.Fatalf("sshArgs() should route through the shared socket, got %q", controlPath)
	}
	if !strings.Contains(strings.Join(app.sshArgv(target), " "), "ControlMaster=no") {
		t.Fatalf("commands must not start connections of their own: %v", app.sshArgv(target))
	}
	if !strings.Contains(app.sshCommandString(target), "ControlPath="+controlPath) {
		t.Fatalf("rsync -e command should use the shared socket: %s", app.sshCommandString(target))
	}

	app.closeSSHSessions()
	commands = readSSHLog(t, logPath)
	closeCommand := commands[len(commands)-1]
	for _, want := range []string{"-p 2222", "ControlPath=" + controlPath, "-O exit", "deploy@example.com"} {
		if !strings.Contains(closeCommand, want) {
			t.Fatalf("close command missing %q:\n%s", want, closeCommand)
		}
	}
	if _, err := os.Stat(filepath.Dir(controlPath)); !os.IsNotExist(err) {
		t.Fatalf("closing should remove the socket directory, got err: %v", err)
	}
	if path := controlPathArg(app.sshArgv(target)); path != "" {
		t.Fatalf("closed sessions should not be used, got %q", path)
	}
	if err := app.openSSHSession(context.Background(), dir, target); err != nil {
		t.Fatalf("openSSHSession() after close error = %v", err)
	}
	if got := len(readSSHLog(t, logPath)); got != len(commands) {
		t.Fatalf("openSSHSession() after close should not start ssh again, log has %d lines, want %d", got, len(commands))
	}
}

func TestOpenSSHSessionOpensOneConnectionPerLogin(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, "exit 0\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.sshSessions = &sshSessionPool{}
	defer app.closeSSHSessions()
	source := RemoteTarget{User: "deploy", Host: "source.example.com"}
	target := RemoteTarget{User: "deploy", Host: "target.example.com"}

	for _, open := range []RemoteTarget{source, target} {
		if err := app.openSSHSession(context.Background(), dir, open); err != nil {
			t.Fatalf("openSSHSession() error = %v", err)
		}
	}
	if got := len(readSSHLog(t, logPath)); got != 2 {
		t.Fatalf("two logins should start two shared connections, got %d", got)
	}
	if controlPathArg(app.sshArgv(source)) == controlPathArg(app.sshArgv(target)) {
		t.Fatal("different logins must not share a control socket")
	}
}

func TestOpenSSHSessionFailureReportsSSHError(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, "printf 'Permission denied (publickey).\\n' >&2\nexit 255\n")
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &stderr)
	app.sshSessions = &sshSessionPool{}
	target := RemoteTarget{User: "deploy", Host: "example.com"}

	if err := app.openSSHSession(context.Background(), dir, target); err == nil {
		t.Fatal("openSSHSession() succeeded despite failed authentication")
	}
	if !strings.Contains(stderr.String(), "remote │ Permission denied (publickey).") {
		t.Fatalf("missing SSH failure diagnostics:\n%s", stderr.String())
	}
	if path := controlPathArg(app.sshArgv(target)); path != "" {
		t.Fatalf("failed session should not be used, got %q", path)
	}
	app.closeSSHSessions()
	for _, command := range readSSHLog(t, logPath) {
		if strings.Contains(command, "-O exit") {
			t.Fatalf("failed session should not be closed through its socket:\n%s", command)
		}
	}
}

func TestRunClosesSharedSSHSessionWhenPreflightFails(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, `case "$*" in
  *ControlMaster=yes*|*'-O exit'*) exit 0 ;;
  *WP_SSH_REMOTE_PATH*) printf 'Fatal: remote WordPress path does not exist\n' >&2; exit 1 ;;
esac
exit 0
`)
	t.Chdir(dir)

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	code := Run([]string{
		"pull", "--integration", "standalone", "--silent", "--yes", "--skip-files",
		"--user", "deploy", "--host", "example.com", "--remote-path", "/var/www/html",
	}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Run() succeeded despite a failing preflight check\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	commands := readSSHLog(t, logPath)
	if len(commands) < 3 {
		t.Fatalf("expected shared connection, failing check, and close commands:\n%s", strings.Join(commands, "\n"))
	}
	if !strings.Contains(commands[0], "ControlMaster=yes") {
		t.Fatalf("preflight should open the shared connection first:\n%s", commands[0])
	}
	controlPath := controlPathArg(strings.Fields(commands[0]))
	if !strings.Contains(commands[1], "ControlPath="+controlPath) || !strings.Contains(commands[1], "WP_SSH_REMOTE_PATH") {
		t.Fatalf("environment check should reuse the shared connection:\n%s", commands[1])
	}
	last := commands[len(commands)-1]
	if !strings.Contains(last, "-O exit") || !strings.Contains(last, "ControlPath="+controlPath) {
		t.Fatalf("failed preflight should close the shared connection last:\n%s", strings.Join(commands, "\n"))
	}
	if _, err := os.Stat(filepath.Dir(controlPath)); !os.IsNotExist(err) {
		t.Fatalf("socket directory should be removed after the run, got err: %v", err)
	}
}

func TestRunClosesSharedSSHSessionOnSignal(t *testing.T) {
	if os.Getenv("WP_SSH_SIGNAL_HELPER") == "1" {
		os.Exit(Run(strings.Fields(os.Getenv("WP_SSH_SIGNAL_ARGS")), strings.NewReader(""), io.Discard, io.Discard))
	}
	if runtime.GOOS == "windows" {
		t.Skip("signal delivery to a child process is Unix-only")
	}
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, `case "$*" in
  *WP_SSH_REMOTE_PATH*) exec sleep 5 ;;
esac
exit 0
`)

	// Run the CLI in a child process so the signal ends that process, not the test binary.
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunClosesSharedSSHSessionOnSignal$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"WP_SSH_SIGNAL_HELPER=1",
		"WP_SSH_SIGNAL_ARGS=pull --integration standalone --silent --yes --skip-files --user deploy --host example.com --remote-path /var/www/html",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if contents, _ := os.ReadFile(logPath); strings.Contains(string(contents), "WP_SSH_REMOTE_PATH") {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the CLI never reached the environment check over the shared connection")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	if code := cmd.ProcessState.ExitCode(); code != -1 && code != 128+int(syscall.SIGTERM) {
		t.Fatalf("interrupted CLI exited with %d, want death by SIGTERM or status %d", code, 128+int(syscall.SIGTERM))
	}
	commands := readSSHLog(t, logPath)
	controlPath := controlPathArg(strings.Fields(commands[0]))
	last := commands[len(commands)-1]
	if !strings.Contains(commands[0], "ControlMaster=yes") || !strings.Contains(last, "-O exit") || !strings.Contains(last, "ControlPath="+controlPath) {
		t.Fatalf("an interrupted run should close the shared connection it opened:\n%s", strings.Join(commands, "\n"))
	}
	if _, err := os.Stat(filepath.Dir(controlPath)); !os.IsNotExist(err) {
		t.Fatalf("an interrupted run should remove the socket directory, got err: %v", err)
	}
}
