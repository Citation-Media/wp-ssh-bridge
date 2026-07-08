package app

import (
	"bytes"
	"context"
	"io"
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

// sshTarget returns the user@host target accepted by ssh and rsync.
func sshTarget(target RemoteTarget) string {
	return target.User + "@" + target.Host
}

// sshArgs returns safe argv entries for non-interactive SSH key authentication.
func sshArgs(target RemoteTarget) []string {
	args := []string{}
	if target.Port != "" {
		args = append(args, "-p", target.Port)
	}
	args = append(args, sshOptions...)
	return args
}

// sshCommandString returns the rsync -e value for SSH transport.
func sshCommandString(target RemoteTarget) string {
	args := []string{"ssh"}
	if target.Port != "" {
		args = append(args, "-p", target.Port)
	}
	args = append(args, sshOptions...)
	return strings.Join(args, " ")
}

// runSSH executes a remote command through ssh without a local shell.
func (a *App) runSSH(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) error {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
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

func (a *App) runSSHQuietSuccess(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) error {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
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
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
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
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	return a.runSSHWithWriters(ctx, projectRoot, args, io.Discard, io.Discard)
}

func (a *App) runSSHWithWriters(ctx context.Context, projectRoot string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", args...)
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
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	stderr := bytes.Buffer{}
	output, err := a.outputSSHWithStderr(ctx, projectRoot, args, &stderr)
	if err != nil {
		return output, commandOutputError{err: err, stderr: stderr.String()}
	}
	return output, nil
}

func (a *App) outputSSHSilent(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	return a.outputSSHWithStderr(ctx, projectRoot, args, io.Discard)
}

func (a *App) outputSSHWithStderr(ctx context.Context, projectRoot string, args []string, stderr io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Dir = projectRoot
	cmd.Stderr = stderr
	cmd.WaitDelay = sshWaitDelay
	output, err := cmd.Output()
	return string(output), err
}

// shellQuote quotes values for the unavoidable remote shell used by SSH servers.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
