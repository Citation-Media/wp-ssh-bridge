package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
)

var sshOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "PasswordAuthentication=no",
	"-o", "KbdInteractiveAuthentication=no",
	"-o", "PubkeyAuthentication=yes",
	"-o", "PreferredAuthentications=publickey",
}

// sshTarget returns the [user@]host target accepted by ssh and rsync.
func sshTarget(target RemoteTarget) string {
	return target.address()
}

// sshArgv returns the complete ssh command line: the program, the port, the
// non-interactive options, the shared-connection options when the login has one,
// then extra — usually the destination and the remote command. Callers exec argv[0]
// with the rest.
func (a *App) sshArgv(target RemoteTarget, extra ...string) []string {
	return plainSSHArgv(target, append(a.sshSessionArgs(target), extra...)...)
}

// plainSSHArgv builds the ssh command line without the shared connection, for commands
// that do not connect (ssh -G) or that open and close the shared connection itself.
func plainSSHArgv(target RemoteTarget, extra ...string) []string {
	args := []string{"ssh"}
	if port := target.port(); port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshOptions...)
	return append(args, extra...)
}

// sshCommandString returns the rsync -e value for SSH transport.
func (a *App) sshCommandString(target RemoteTarget) string {
	return strings.Join(a.sshArgv(target), " ")
}

func (a *App) runSSHQuietSuccess(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) error {
	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	err := a.runSSHWithWriters(ctx, projectRoot, args, &stdout, &stderr)
	if err == nil {
		return nil
	}
	a.writeCapturedOutput("remote", stdout.String(), false)
	a.writeCapturedOutput("remote", stderr.String(), true)
	return err
}

func (a *App) runSSHWithFilteredWarnings(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) error {
	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	return a.runSSHArgsWithFilteredWarnings(ctx, projectRoot, args)
}

func (a *App) runSSHArgsWithFilteredWarnings(ctx context.Context, projectRoot string, args []string) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	filteredStderr := newDuplicateSummaryWriter(&stderr, isRepeatedWarningLine, "Warning: repeated similar warnings suppressed")
	err := a.runSSHWithWriters(ctx, projectRoot, args, &stdout, filteredStderr)
	flushPrefixed(filteredStderr)
	if err != nil {
		a.writeCapturedOutput("remote", stdout.String(), false)
		a.writeCapturedOutput("remote", stderr.String(), true)
	}
	return err
}

func (a *App) runSSHSilent(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) error {
	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	return a.runSSHWithWriters(ctx, projectRoot, args, io.Discard, io.Discard)
}

// runSSHWithWriters runs a full ssh argv as built by sshArgv, so argv[0] is the program.
func (a *App) runSSHWithWriters(ctx context.Context, projectRoot string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = sshWaitDelay
	return cmd.Run()
}

func (a *App) writeCapturedOutput(label string, output string, stderr bool) {
	if strings.TrimSpace(output) == "" {
		return
	}
	writer := a.UI.PrefixedWriter(label, stderr)
	_, _ = io.WriteString(writer, output)
	flushPrefixed(writer)
}

// outputSSH executes a remote command and captures stdout for URL and table probes.
func (a *App) outputSSH(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	stderr := bytes.Buffer{}
	output, err := a.outputSSHWithStderr(ctx, projectRoot, args, &stderr)
	if err != nil {
		return output, commandOutputError{err: err, stderr: stderr.String()}
	}
	return output, nil
}

func (a *App) outputSSHSilent(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := a.sshArgv(target, sshTarget(target), remoteCommand)
	return a.outputSSHWithStderr(ctx, projectRoot, args, io.Discard)
}

func (a *App) outputSSHWithStderr(ctx context.Context, projectRoot string, args []string, stderr io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	cmd.Stderr = stderr
	cmd.WaitDelay = sshWaitDelay
	output, err := cmd.Output()
	return string(output), err
}

// downloadOverSSH streams a remote file to a local path through the ssh session itself,
// so the fallback transport needs no second program and runs with the same options as
// every other connection.
func (a *App) downloadOverSSH(ctx context.Context, projectRoot string, target RemoteTarget, remotePath string, localPath string) error {
	file, err := os.Create(localPath)
	if err != nil {
		return err
	}
	args := a.sshArgv(target, sshTarget(target), "cat "+shellQuote(remotePath))
	stderr := bytes.Buffer{}
	runErr := a.runSSHWithWriters(ctx, projectRoot, args, file, &stderr)
	closeErr := file.Close()
	if runErr != nil {
		a.writeCapturedOutput("remote", stderr.String(), true)
		return runErr
	}
	return closeErr
}

// uploadOverSSH streams a local file into a remote path through the ssh session itself.
func (a *App) uploadOverSSH(ctx context.Context, projectRoot string, target RemoteTarget, localPath string, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	args := a.sshArgv(target, sshTarget(target), "cat > "+shellQuote(remotePath))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	cmd.Stdin = file
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr
	cmd.WaitDelay = sshWaitDelay
	if err := cmd.Run(); err != nil {
		a.writeCapturedOutput("remote", stderr.String(), true)
		return err
	}
	return nil
}

// shellQuote quotes values for the unavoidable remote shell used by SSH servers.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
