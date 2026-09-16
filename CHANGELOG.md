# Changelog

All notable changes to `wp-ssh-bridge` are documented in this file.

## [Unreleased]

### Added

- Add wp-env (`@wordpress/env`) support as a third runtime mode. The CLI detects a wp-env project from `.wp-env.json` or `.wp-env.override.json`, reads `wp-env status --json` for the local URL and install path, and runs local WP-CLI through `wp-env run cli`. Database dumps are staged inside the tree wp-env mounts at `/var/www/html` so the import runs against the container database instead of an unreachable host connection.
- Warn before a file pull when `.wp-env.json` declares `plugins`, `themes`, or `mappings`. Those bind mounts shadow the host WordPress tree, so files pulled into them never reach WordPress.

### Changed

- Rename the `migrate` command to `clone`. The target database settings move with it: the config keys `migrate_db_host`, `migrate_db_name`, `migrate_db_user`, `migrate_db_password`, and `migrate_db_prefix` become `clone_db_*`, and the `WP_SSH_MIGRATE_DB_*` environment variables become `WP_SSH_CLONE_DB_*`. The old command name, keys, and variables are no longer accepted; running `migrate` prints a hint pointing to `clone`. Update saved `.wp-ssh.yaml` files and automation.
- Preserve the local `wp-config.php` during pulls in wp-env mode, as in standalone mode. wp-env generates it with working local database credentials and rewrites it on every start, so its URL constants are left untouched.
- Reject `migrate` in wp-env projects, matching the existing DDEV restriction. The import would otherwise go to the container database instead of the injected target credentials.
- Resolve the project root for `plugins remove` in wp-env projects instead of failing with a DDEV-only lookup error.
- Gate runtime detection behind filesystem markers so `ddev describe` no longer runs in projects without `.ddev/config.yaml`, and route every path and WP-CLI helper through the mode detection resolved, so a pinned integration is honored consistently.
- Reject `push` in wp-env projects. The local WordPress tree is wp-env-managed and its mounted plugin, theme, and upload directories are empty on the host, so `rsync --delete` would erase them on the target.
- Require `.wp-env.json` or `.wp-env.override.json` to enter wp-env mode. A project-local `node_modules/.bin/wp-env` is present in any repo listing `@wordpress/env` as a dev dependency and no longer flips the runtime.
- Resolve the wp-env WordPress root from the generated `docker-compose.yml` so a `core` entry in `.wp-env.json` is honored instead of assuming `<installPath>/WordPress`.
- Fail instead of falling back to standalone mode when a wp-env project's environment cannot be probed, which previously pointed the local WordPress root at the repository itself.
- Skip blocked-plugin cleanup when `.wp-env.json` declares mounts, since `wp plugin delete` runs inside the container where those mounts are the user's source tree.
- Remove the staged database dump after a successful import and write a deny-all `.htaccess` beside it; in wp-env mode the scratch directory is inside the tree served over HTTP.
- Stop persisting a one-shot `--integration` flag or `WP_SSH_INTEGRATION` value into the project config file. Only `init --integration` writes it.
- Limit the `integration` pin lookup to the project that owns a config file, so an unrelated ancestor `.wp-ssh.yaml` no longer pins nested projects.
- Prompt for missing pull/push values in every non-DDEV mode, not only standalone.
- Exclude wp-env bind-mounted plugin, theme, and mapping paths from file pulls, derived from the generated `docker-compose.yml`. Pulled files there would be shadowed by the mounts, and `rsync --delete` against a live mountpoint can fail.
- Stop wp-env detection at the first directory that is a project of its own, so an ancestor `.wp-env.json` cannot hijack a nested DDEV or standalone project.
- Resolve wp-env status from subdirectories and explicit `--project-root` values by walking up to the project root, and bound the `wp-env status --json` probe with a timeout.
- Register `--integration` on `plugins remove` and `domains` as well, and name the DDEV alternative in the wp-env-unreachable error when the repository also carries a DDEV config.
- Add an `integration` config key, `--integration` flag, and `WP_SSH_INTEGRATION` environment variable to pin the runtime to `ddev`, `wp-env`, or `standalone`. A pin is strict and fails rather than falling back to standalone mode, which would sync into a different local WordPress root.

## [0.5.0] - 2026-07-12

### Added

- Add `migrate --clean-target` to remove pre-existing target content before syncing. The rsync transport uses `--delete`; the scp/tar transport empties the resolved WordPress directory while preserving operational files and refusing unsafe filesystem-root or home-directory targets.

### Changed

- Make migrations additive by default on both rsync and scp/tar transports. Normal non-migration pulls continue to mirror the source with rsync `--delete`.
- Use DDEV's resolved `DDEV_TLD` when deriving `additional_hostnames`, allowing custom project TLDs as well as `ddev.site`.

### Fixed

- Prevent protocol-less multisite domain mappings from rewriting a newly generated local hostname a second time. A mapping such as `acme-group.de` to `acme-group.de.ddev.site` now updates bare domains and complete URLs exactly once ([#7]).

[0.5.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.4.1...v0.5.0
[#7]: https://github.com/Citation-Media/wp-ssh-bridge/issues/7
