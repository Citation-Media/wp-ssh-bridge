package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sshWaitDelay bounds how long Cmd.Wait blocks on inherited output pipes after the ssh
// process itself has exited. Without it, an ssh-spawned helper that keeps stderr open —
// an OpenSSH ControlMaster/ControlPersist mux or a ProxyCommand child — holds the stderr
// copy pipe open and hangs Wait indefinitely.
const sshWaitDelay = 10 * time.Second

// tarPipeFromRemote streams a remote tar archive directly into a local directory.
// It runs "ssh host <remoteCmd>" and pipes stdout into "tar -xzf - -C localDest".
//
// Symlink note: unlike the rsync transport (which runs with --safe-links), tar has no
// equivalent and recreates symlinks whose targets are absolute or escape the tree
// verbatim. GNU tar's default extraction still refuses to write *through* an escaping
// symlink, so this is a low-severity parity gap, not a path-traversal write; keep rsync
// available when strict --safe-links parity matters.
func (a *App) tarPipeFromRemote(ctx context.Context, projectRoot string, target RemoteTarget, remoteCmd string, localDest string) error {
	// Use an OS pipe and hand each end to a child process. Once the parent closes its
	// copies, tar alone holds the read end and ssh alone the write end: if the local
	// extract exits early — the destination is missing or the disk is full — closing the
	// read end makes ssh's next write fail and ssh exits, instead of blocking forever on
	// a full pipe with no reader (which would deadlock and hang the command).
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	defer pw.Close()

	sshArgList := append(sshArgs(target), sshTarget(target), remoteCmd)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgList...)
	sshCmd.Dir = projectRoot
	sshCmd.Stdout = pw
	sshCmd.WaitDelay = sshWaitDelay

	var sshStderr bytes.Buffer
	sshCmd.Stderr = &sshStderr

	tarCmd := exec.CommandContext(ctx, "tar", "-xzf", "-", "-C", localDest)
	tarCmd.Dir = projectRoot
	tarCmd.Stdin = pr

	var tarStderr bytes.Buffer
	tarCmd.Stderr = &tarStderr

	if err := sshCmd.Start(); err != nil {
		return err
	}
	if err := tarCmd.Start(); err != nil {
		_ = sshCmd.Process.Kill()
		_ = sshCmd.Wait()
		return err
	}

	// The children hold their own dups of the pipe ends; close the parent copies so
	// tar's exit is the only thing keeping the read end open and ssh's the write end.
	_ = pw.Close()
	_ = pr.Close()

	tarErr := tarCmd.Wait()
	sshErr := sshCmd.Wait()

	// The local extract is the consumer and the authority on integrity: gzip verifies its
	// trailer and tar its end-of-archive marker, so a clean extract means the whole archive
	// arrived. Report its failure first — otherwise a local failure (missing destination,
	// full disk) surfaces only as ssh's resulting broken pipe and points the user at the
	// wrong machine. When the stream was truncated by a remote/ssh failure the extract
	// fails with "unexpected EOF", so append the remote error rather than guess the cause.
	if tarErr != nil {
		failure := fmt.Errorf("local extract: %w", commandOutputError{err: tarErr, stderr: tarStderr.String()})
		if sshErr != nil {
			failure = fmt.Errorf("%w (remote tar: %s)", failure, commandOutputError{err: sshErr, stderr: sshStderr.String()}.Error())
		}
		return failure
	}
	// tar extracted a complete archive, so the transfer succeeded. A remote "file changed
	// as we read it" warning (exit 1, which the rsync transport tolerates too) is not a
	// failure; surface only genuine fatal remote errors.
	if sshErr != nil && !tarProducerWarning(sshErr) {
		return fmt.Errorf("remote tar: %w", commandOutputError{err: sshErr, stderr: sshStderr.String()})
	}
	return nil
}

// tarPipeToRemote streams a local tar archive directly into a remote shell command over SSH.
// It runs "tar <localTarArgs>" locally and pipes stdout into "ssh host <remoteCmd>".
func (a *App) tarPipeToRemote(ctx context.Context, projectRoot string, target RemoteTarget, localTarArgs []string, remoteCmd string) error {
	// Use an OS pipe (not io.Pipe) and hand each end to a child process. Once the parent
	// closes its copies, ssh alone holds the read end: if ssh exits early — e.g. the remote
	// extract fails because the target disk is full — the read end closes and tar's next
	// write gets EPIPE and tar exits, instead of blocking forever on a pipe with no reader
	// (which would deadlock and hang the command).
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	defer pw.Close()

	tarCmd := exec.CommandContext(ctx, "tar", localTarArgs...)
	tarCmd.Dir = projectRoot
	tarCmd.Stdout = pw

	var tarStderr bytes.Buffer
	tarCmd.Stderr = &tarStderr

	sshArgList := append(sshArgs(target), sshTarget(target), remoteCmd)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgList...)
	sshCmd.Dir = projectRoot
	sshCmd.Stdin = pr
	sshCmd.WaitDelay = sshWaitDelay

	var sshStderr bytes.Buffer
	sshCmd.Stderr = &sshStderr

	if err := sshCmd.Start(); err != nil {
		return err
	}
	if err := tarCmd.Start(); err != nil {
		_ = sshCmd.Process.Kill()
		_ = sshCmd.Wait()
		return err
	}

	// The children now hold their own dups of the pipe ends; close the parent copies so
	// ssh's exit is the only thing keeping the read end open.
	_ = pw.Close()
	_ = pr.Close()

	tarErr := tarCmd.Wait()
	sshErr := sshCmd.Wait()

	// Prefer the remote extract error: it is the consumer here, so when it fails the local
	// tar error is usually just the resulting broken pipe, which is less informative. Append
	// a genuine local failure as context (a broken pipe or a "file changed" warning is not).
	if sshErr != nil {
		failure := fmt.Errorf("remote extract: %w", commandOutputError{err: sshErr, stderr: sshStderr.String()})
		if tarErr != nil && !tarProducerWarning(tarErr) && !isBrokenPipe(tarErr) {
			failure = fmt.Errorf("%w (local tar: %s)", failure, commandOutputError{err: tarErr, stderr: tarStderr.String()}.Error())
		}
		return failure
	}
	// The remote extract succeeded, so a local "file changed as we read it" warning (exit 1,
	// which the rsync transport tolerates too) is not a failure.
	if tarErr != nil && !tarProducerWarning(tarErr) {
		return fmt.Errorf("local tar: %w", commandOutputError{err: tarErr, stderr: tarStderr.String()})
	}
	return nil
}

// tarProducerWarning reports whether a tar producer exited with a non-fatal warning.
// GNU/bsd tar use exit status 1 for "some files differ / changed as we read it": a
// complete, valid archive was still produced. The rsync transport tolerates the
// equivalent condition (exit 24 via isRsyncVanishedError), so a tar transfer whose other
// end completed cleanly should not fail the whole pull/push on this warning.
func tarProducerWarning(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// isBrokenPipe reports whether err is a process killed by SIGPIPE, i.e. the expected
// consequence of the other end of the pipe closing first rather than an independent
// failure worth reporting.
func isBrokenPipe(err error) bool {
	return err != nil && strings.Contains(err.Error(), "broken pipe")
}

// buildTarExcludeArgs converts raw exclude patterns (as returned by buildRsyncExcludes)
// into tar --exclude= arguments, stripping trailing slashes that are rsync-specific.
func buildTarExcludeArgs(patterns []string) []string {
	args := make([]string, 0, len(patterns))
	for _, p := range patterns {
		args = append(args, "--exclude="+strings.TrimSuffix(p, "/"))
	}
	return args
}
