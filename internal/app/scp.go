package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// scpArgs returns command-line arguments for non-interactive scp.
// scp uses -P (uppercase) for port, unlike ssh which uses lowercase -p.
func scpArgs(target RemoteTarget) []string {
	args := []string{}
	if target.Port != "" {
		args = append(args, "-P", target.Port)
	}
	args = append(args, sshOptions...)
	return args
}

// needsScpTransport returns true when rsync is unavailable locally or on the remote,
// or when the caller has explicitly forced the scp/tar transport with --force-scp.
func (a *App) needsScpTransport(ctx context.Context, projectRoot string, target RemoteTarget, force bool) bool {
	if force {
		a.UI.Info("Using scp/tar transport (--force-scp)")
		return true
	}
	if !commandExists("rsync") {
		a.UI.Warning("Local rsync not available — falling back to scp/tar transfer")
		return true
	}
	if err := a.runSSHSilent(ctx, projectRoot, target, "command -v rsync >/dev/null 2>&1"); err != nil {
		a.UI.Warning("Remote rsync not available — falling back to scp/tar transfer")
		return true
	}
	return false
}

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
		return err
	}

	// ssh.Wait closes StdoutPipe, signalling EOF to tar.
	sshErr := sshCmd.Wait()
	tarErr := tarCmd.Wait()

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
	tarCmd := exec.CommandContext(ctx, "tar", localTarArgs...)
	tarCmd.Dir = projectRoot

	sshArgList := append(sshArgs(target), sshTarget(target), remoteCmd)
	sshCmd := exec.CommandContext(ctx, "ssh", sshArgList...)
	sshCmd.Dir = projectRoot

	var sshStderr bytes.Buffer
	sshCmd.Stderr = &sshStderr

	pr, pw := io.Pipe()
	tarCmd.Stdout = pw
	sshCmd.Stdin = pr

	if err := tarCmd.Start(); err != nil {
		return err
	}
	if err := sshCmd.Start(); err != nil {
		_ = tarCmd.Process.Kill()
		return err
	}

	// tar.Wait signals that all data has been written; close the write end so SSH gets EOF.
	tarErr := tarCmd.Wait()
	_ = pw.Close()
	sshErr := sshCmd.Wait()

	if tarErr != nil {
		return fmt.Errorf("local tar: %w", tarErr)
	}
	if sshErr != nil {
		if details := strings.TrimSpace(sshStderr.String()); details != "" {
			return fmt.Errorf("remote extract: %w: %s", sshErr, details)
		}
		return fmt.Errorf("remote extract: %w", sshErr)
	}
	return nil
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
