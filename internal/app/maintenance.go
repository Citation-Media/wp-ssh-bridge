package app

import "context"

// enableRemoteMaintenanceMode puts the remote WordPress site into maintenance mode via
// WP-CLI and reports whether it did. A target without an installed site, such as an
// empty directory before the first push, has no visitors to protect, and WP-CLI cannot
// activate maintenance mode there, so it is skipped instead of failing the push.
func (a *App) enableRemoteMaintenanceMode(ctx context.Context, projectRoot string, target RemoteTarget) (bool, error) {
	if err := a.runRemoteWPSilent(ctx, projectRoot, target, "core", "is-installed"); err != nil {
		a.UI.Info("Skipping remote maintenance mode: no installed WordPress site on the push target yet")
		return false, nil
	}
	return true, a.runStep("Enabling remote maintenance mode", "Remote maintenance mode enabled", func() error {
		return a.runRemoteWPWithFilteredWarnings(ctx, projectRoot, target, "maintenance-mode", "activate")
	})
}

// disableRemoteMaintenanceMode removes maintenance mode from the remote WordPress site.
// Errors are suppressed so this is safe to call from a defer.
func (a *App) disableRemoteMaintenanceMode(ctx context.Context, projectRoot string, target RemoteTarget) {
	if err := a.runRemoteWPSilent(ctx, projectRoot, target, "maintenance-mode", "deactivate"); err != nil {
		a.UI.Warning("Could not disable remote maintenance mode: %s", err)
		return
	}
	a.UI.Success("Remote maintenance mode disabled")
}

// enableLocalMaintenanceMode puts the local WordPress site into maintenance mode via
// WP-CLI and reports whether it did. A destination without an installed site, such as a
// clone into an empty directory or a new database, or a fresh project's first pull, has
// nothing to protect, and WP-CLI cannot activate maintenance mode there, so it is
// skipped instead of failing the import.
func (a *App) enableLocalMaintenanceMode(ctx context.Context, projectRoot string, cfg Config) (bool, error) {
	if err := a.runWPSilent(ctx, projectRoot, cfg, "core", "is-installed"); err != nil {
		a.UI.Info("Skipping local maintenance mode: no installed WordPress site at the destination yet")
		return false, nil
	}
	return true, a.runStep("Enabling local maintenance mode", "Local maintenance mode enabled", func() error {
		return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "maintenance-mode", "activate")
	})
}

// disableLocalMaintenanceMode removes maintenance mode from the local WordPress site.
// Errors are suppressed so this is safe to call from a defer.
func (a *App) disableLocalMaintenanceMode(ctx context.Context, projectRoot string, cfg Config) {
	if err := a.runWPSilent(ctx, projectRoot, cfg, "maintenance-mode", "deactivate"); err != nil {
		a.UI.Warning("Could not disable local maintenance mode: %s", err)
		return
	}
	a.UI.Success("Local maintenance mode disabled")
}
