package app

import (
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
	stdout := a.UI.PrefixedWriter("remote", false)
	stderr := a.UI.PrefixedWriter("remote", true)
	defer flushPrefixed(stdout)
	defer flushPrefixed(stderr)
	return a.runSSHWithWriters(ctx, projectRoot, args, stdout, stderr)
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
	return cmd.Run()
}

// outputSSH executes a remote command and captures stdout for URL and table probes.
func (a *App) outputSSH(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	stderr := a.UI.PrefixedWriter("remote", true)
	defer flushPrefixed(stderr)
	return a.outputSSHWithStderr(ctx, projectRoot, args, stderr)
}

func (a *App) outputSSHSilent(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	return a.outputSSHWithStderr(ctx, projectRoot, args, io.Discard)
}

func (a *App) outputSSHWithStderr(ctx context.Context, projectRoot string, args []string, stderr io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Dir = projectRoot
	cmd.Stderr = stderr
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
