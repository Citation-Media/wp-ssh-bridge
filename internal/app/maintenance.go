package app

import "context"

// enableRemoteMaintenanceMode puts the remote WordPress site into maintenance mode via WP-CLI.
func (a *App) enableRemoteMaintenanceMode(ctx context.Context, projectRoot string, target RemoteTarget) error {
	return a.runStep("Enabling remote maintenance mode", "Remote maintenance mode enabled", func() error {
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

// enableLocalMaintenanceMode puts the local WordPress site into maintenance mode via WP-CLI.
func (a *App) enableLocalMaintenanceMode(ctx context.Context, projectRoot string, cfg Config) error {
	return a.runStep("Enabling local maintenance mode", "Local maintenance mode enabled", func() error {
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
