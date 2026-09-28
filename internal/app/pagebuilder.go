package app

import (
	"context"
	"strings"
)

// pageBuilderCache describes the WP-CLI command a page builder registers to rebuild its generated CSS.
type pageBuilderCache struct {
	name    string
	command []string
	network string
}

// pageBuilderCaches lists page builders whose generated CSS goes stale after a database
// transfer. Builders without a WP-CLI command for this are not listed.
var pageBuilderCaches = []pageBuilderCache{
	{name: "Elementor", command: []string{"elementor", "flush-css"}, network: "--network"},
	{name: "Bricks", command: []string{"bricks", "regenerate_assets"}},
	{name: "Beaver Builder", command: []string{"beaver", "clearcache"}, network: "--network"},
}

// rebuildLocalPageBuilderCaches rebuilds page builder CSS in the local WordPress site.
func (a *App) rebuildLocalPageBuilderCaches(ctx context.Context, projectRoot string, cfg Config) {
	if cfg.SkipCacheRebuild {
		return
	}
	a.rebuildPageBuilderCaches("local", a.isMultisite(ctx, projectRoot, cfg), func(args ...string) error {
		name, fullArgs := localWPCommandWithFlags(projectRoot, cfg, nil, args...)
		_, err := a.outputExternal(ctx, projectRoot, name, fullArgs...)
		return err
	})
}

// rebuildRemotePageBuilderCaches rebuilds page builder CSS on the push target.
func (a *App) rebuildRemotePageBuilderCaches(ctx context.Context, projectRoot string, cfg Config) {
	if cfg.SkipCacheRebuild {
		return
	}
	target := cfg.pushTarget()
	a.rebuildPageBuilderCaches("remote", a.remoteIsMultisite(ctx, projectRoot, target), func(args ...string) error {
		_, err := a.outputSSH(ctx, projectRoot, target, remoteWPCommandWithFlags(target, nil, args...))
		return err
	})
}

// rebuildPageBuilderCaches runs each page builder command with plugins and themes loaded.
// Builders whose command is not registered are skipped. Failures are reported as warnings
// and never abort the surrounding pull, clone, or push.
func (a *App) rebuildPageBuilderCaches(label string, multisite bool, run func(args ...string) error) {
	for _, builder := range pageBuilderCaches {
		args := append([]string{}, builder.command...)
		if multisite && builder.network != "" {
			args = append(args, builder.network)
		}
		err := run(args...)
		if err != nil && isWPCLIUnknownCommand(err) {
			continue
		}
		if err != nil {
			a.UI.Warning("Could not rebuild %s %s CSS: %s", label, builder.name, err)
			continue
		}
		a.UI.Success("%s %s CSS rebuilt", sentenceCase(label), builder.name)
	}
}

// isWPCLIUnknownCommand detects WP-CLI's error for commands or subcommands no plugin or theme registered.
func isWPCLIUnknownCommand(err error) bool {
	return strings.Contains(err.Error(), "is not a registered")
}
