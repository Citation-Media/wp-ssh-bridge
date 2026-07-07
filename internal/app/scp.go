package app

import "context"

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
