# Changelog

All notable changes to `wp-ssh-bridge` are documented in this file.

## [Unreleased]

### Added

- Document how to migrate a site: running `clone` on the new server, which connects to the old one with a forwarded SSH agent, or cloning through your own machine and pushing to the new host. See [Migrate a site](https://wp-ssh-bridge.citation.media/docs/migrate-a-site).

### Fixed

- Stop a push into a target without an installed site before anything is uploaded when no target URL is known. The target URL is read from the site the import replaces, so a first push into an empty directory found none, imported anyway, and left the local URLs in the target database. The push now asks for `push_url`, `WP_SSH_PUSH_URL`, or `--push-url` unless a push domain mapping or `--skip-search-replace` covers the URLs.

- Keep a cloned site's URLs unless a target is named. A clone fell back to a development host and wrote `WP_HOME` and `WP_SITEURL` as `https://localhost` into the target's `wp-config.php`, also with a domain mapping without a protocol, so the migrated site redirected to `localhost`. Now a clone without `local_url`, `WP_SSH_PULL_LOCAL_URL`, `--local-url`, or a pull domain mapping keeps the source URL and says so. With a target, the database is rewritten and the URL constants get the same replacements, but only where the copied `wp-config.php` already defines them, so a site that keeps its URLs in the database gets no new constants and a `WP_SITEURL` in a subdirectory keeps its path.

## [0.8.1] - 2026-09-25

### Fixed

- Clone into an empty target. Maintenance mode needs an installed WordPress site, so a clone into an empty directory or a new database stopped with `Enabling local maintenance mode failed` before the import. The CLI now checks with `wp core is-installed` and imports without maintenance mode when no site is installed yet, and `clone --clean-target` skips it outright because the previous site is being replaced. The same check covers a fresh project's first pull and the first push into an empty target directory.

## [0.8.0] - 2026-09-25

### Added

- Share one SSH connection per login for the whole run. The first preflight check opens an OpenSSH control master, and every later check, WP-CLI command, and transfer reuses it, so an SSH agent that asks for approval, such as 1Password's, asks once per login instead of once per command. The connection closes when the command finishes, fails, or is interrupted, and a leftover one closes itself after ten idle minutes. DDEV provider steps run as separate processes and authenticate once each.

### Fixed

- Pull database exports from hosts that disable PHP's `exec()` for the command line, a common shared-hosting hardening, verified on Hostinger. WP-CLI's `wp db export` calls `exec()` directly, so on these hosts it died with exit status 255 and no message. Preflight now detects which of the functions the export needs are disabled and runs only the export with them removed from the host's `disable_functions` list, through a `php -d` startup option of that one process. Nothing on the host changes, and every other disabled function stays disabled. The check never runs code inside WP-CLI, so a `wp-cli.yml` that disables `eval` does not affect it. Preflight stops with a message naming the functions when the host blocks the override. See [Disabled PHP functions](https://wp-ssh-bridge.citation.media/docs/troubleshooting/disabled-php-functions).

## [0.7.0] - 2026-09-22

### Added

- Accept one SSH destination per target instead of separate user, host, and port values: `pull_destination` and `push_destination`, `WP_SSH_PULL_DESTINATION` and `WP_SSH_PUSH_DESTINATION`, `--destination` and `--push-destination`. A destination is `user@host[:port]`, an `ssh://` URL, or an alias from `~/.ssh/config`, and `init` prints what `ssh -G` resolves it to. An alias needs no user in the config, because `~/.ssh/config` supplies it, and it is also the way to reach an IPv6 host: macOS' built-in rsync cannot pass an IPv6 literal on, so a destination refuses one. On `push`, `--destination` aliases the push target like the other short flags.
- Keep the split `pull_user`, `pull_host`, and `pull_port` keys working. A destination and split values in the same file, environment, or command line are rejected rather than merged, so a stale `pull_user` can never override an alias. A destination from the environment or a flag replaces the split keys of the config file, so `WP_SSH_PULL_DESTINATION` works as an override on existing projects.
- Document SSH access on its own page: the address forms, how aliases resolve, what the CLI deliberately leaves to OpenSSH and your SSH agent, and what to check when a connection fails. The agents of 1Password, Bitwarden, and Proton Pass work unchanged; the CLI ships no vendor code for any of them.
- Document secrets managers: which values belong in one, and how `op run` and `pass-cli run` with a file of secret references, or `bws run` with a Secrets Manager project, inject them as `WP_SSH_*` variables for one run, locally and under DDEV.

### Changed

- Stream the database dump through `ssh` itself when rsync is unavailable, instead of a separate `scp` process. One program fewer to require, and the same options on every connection; the `--force-scp` flag and the "scp/tar" transport name are unchanged.
- Ask for one SSH destination first during interactive `init`, and only fall back to the separate user, host, and port prompts when it is left empty.

### Upgrading

- Older CLI versions stop with an unknown-key error on a config that uses `pull_destination` or `push_destination`. Update the CLI in every checkout, including the project-local binary DDEV calls, before committing such a config.

## [0.6.0] - 2026-09-17

### Added

- Block the widely used remote management plugins during pulls: ManageWP, MainWP, InfiniteWP, and WP Umbrella. They report the site to an external dashboard and accept instructions back, so a development copy shows up alongside the real ones and can be updated or backed up from there by mistake.
- Block the widely used image optimization plugins during pulls: Smush, Imagify, EWWW, ShortPixel, Optimole, TinyPNG, Robin, and reSmush.it. They optimize through a third-party service with the production account's credentials, so on a development copy they spend the client's quota on images nobody will see. Optimizers that convert locally are deliberately left alone.
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

[0.8.1]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.5.3...v0.6.0
[0.5.0]: https://github.com/Citation-Media/wp-ssh-bridge/compare/v0.4.1...v0.5.0
[#7]: https://github.com/Citation-Media/wp-ssh-bridge/issues/7
