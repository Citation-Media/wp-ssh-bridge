package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// tarPipeFromRemote streams a remote tar archive directly into a local directory.
// It runs "ssh host <remoteCmd>" and pipes stdout into "tar -xzf - -C localDest".
func (a *App) tarPipeFromRemote(ctx context.Context, projectRoot string, target RemoteTarget, remoteCmd string, localDest string) error {
	sshArgList := append(sshArgs(target), sshTarget(target), remoteCmd)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgList...)
	sshCmd.Dir = projectRoot

	var sshStderr bytes.Buffer
	sshCmd.Stderr = &sshStderr

	sshOut, err := sshCmd.StdoutPipe()
	if err != nil {
		return err
	}

	tarCmd := exec.CommandContext(ctx, "tar", "-xzf", "-", "-C", localDest)
	tarCmd.Dir = projectRoot
	tarCmd.Stdin = sshOut

	if err := sshCmd.Start(); err != nil {
		return err
	}
	if err := tarCmd.Start(); err != nil {
		_ = sshCmd.Process.Kill()
		_ = sshCmd.Wait()
		return err
	}

	// Wait for tar (the consumer) first. If tar exits early — e.g. the local
	// extract fails because the destination is missing or the disk is full —
	// closing the read end makes ssh stop writing and exit, instead of blocking
	// forever on a full pipe with no reader (which would deadlock ssh's Wait and
	// hang the whole command).
	tarErr := tarCmd.Wait()
	_ = sshOut.Close()
	sshErr := sshCmd.Wait()

	if sshErr != nil {
		if details := strings.TrimSpace(sshStderr.String()); details != "" {
			return fmt.Errorf("remote tar: %w: %s", sshErr, details)
		}
		return fmt.Errorf("remote tar: %w", sshErr)
	}
	return tarErr
}

// tarPipeToRemote streams a local tar archive directly into a remote shell command over SSH.
// It runs "tar <localTarArgs>" locally and pipes stdout into "ssh host <remoteCmd>".
func (a *App) tarPipeToRemote(ctx context.Context, projectRoot string, target RemoteTarget, localTarArgs []string, remoteCmd string) error {
	// Use an OS pipe (not io.Pipe) and hand each end to a child process. Once the
	// parent closes its copies, ssh alone holds the read end: if ssh exits early —
	// e.g. the remote extract fails because the target disk is full — the read end
	// closes and tar's next write gets EPIPE and tar exits, instead of blocking
	// forever on a pipe with no reader (which would deadlock and hang the command).
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}

	tarCmd := exec.CommandContext(ctx, "tar", localTarArgs...)
	tarCmd.Dir = projectRoot
	tarCmd.Stdout = pw

	sshArgList := append(sshArgs(target), sshTarget(target), remoteCmd)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgList...)
	sshCmd.Dir = projectRoot
	sshCmd.Stdin = pr

	var sshStderr bytes.Buffer
	sshCmd.Stderr = &sshStderr

	if err := sshCmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return err
	}
	if err := tarCmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_ = sshCmd.Process.Kill()
		_ = sshCmd.Wait()
		return err
	}

	// The children now hold their own dups of the pipe ends; close the parent
	// copies so ssh's exit is the only thing keeping the read end open.
	_ = pw.Close()
	_ = pr.Close()

	tarErr := tarCmd.Wait()
	sshErr := sshCmd.Wait()

	// Prefer the remote error: when the remote extract fails, the local tar error
	// is usually just the resulting broken pipe, which is less informative.
	if sshErr != nil {
		if details := strings.TrimSpace(sshStderr.String()); details != "" {
			return fmt.Errorf("remote extract: %w: %s", sshErr, details)
		}
		return fmt.Errorf("remote extract: %w", sshErr)
	}
	if tarErr != nil {
		return fmt.Errorf("local tar: %w", tarErr)
	}
	return nil
}

// buildTarExcludeArgs converts raw exclude patterns (as returned by buildRsyncExcludes)
// into tar --exclude= arguments, stripping trailing slashes that are rsync-specific.
//
// Known limitation vs. the rsync transport: rsync runs with --safe-links, which skips
// symlinks whose targets are absolute or escape the tree. tar has no equivalent, so the
// tar transport recreates such symlinks verbatim. GNU tar's default extraction still
// refuses to write *through* an escaping symlink, so this is a low-severity parity gap,
// not a path-traversal write. Keep rsync available when strict --safe-links parity matters.
func buildTarExcludeArgs(patterns []string) []string {
	args := make([]string, 0, len(patterns))
	for _, p := range patterns {
		args = append(args, "--exclude="+strings.TrimSuffix(p, "/"))
	}
	return args
}
