package app

import (
	"context"
	"errors"
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
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Dir = projectRoot
	cmd.Stdout = a.Stdout
	cmd.Stderr = a.Stderr
	return cmd.Run()
}

// outputSSH executes a remote command and captures stdout for URL and table probes.
func (a *App) outputSSH(ctx context.Context, projectRoot string, target RemoteTarget, remoteCommand string) (string, error) {
	args := append(sshArgs(target), sshTarget(target), remoteCommand)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Dir = projectRoot
	cmd.Stderr = a.Stderr
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

// requireSSHAgent checks key availability in the process that runs SSH.
func (a *App) requireSSHAgent(ctx context.Context, projectRoot string) error {
	cmd := exec.CommandContext(ctx, "ssh-add", "-l")
	cmd.Dir = projectRoot
	if err := cmd.Run(); err != nil {
		if inDDEVContainer() {
			return errors.New("no SSH key is available inside DDEV; run `ddev auth ssh` from the host before pulling from upstream")
		}
		message := "no local SSH key is loaded; run `ssh-add` before pulling from upstream"
		if hasDDEVConfig(projectRoot) {
			message += "; this is a DDEV project, so also make the key available to DDEV with `ddev auth ssh`"
		}
		return errors.New(message)
	}
	return nil
}
