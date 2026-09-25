# WP SSH Bridge

`wp-ssh-bridge` is a Go CLI for pulling and pushing WordPress databases and full application files through SSH-only environments. It runs as a standalone host-side binary and uses a generated DDEV provider layer only when it detects a DDEV project.

The CLI detects DDEV mode by finding `.ddev/config.yaml` above the current directory and confirming it with `ddev describe -j`. If that succeeds, `pull` and `push` refresh generated provider files and run the same direct Go pipeline used by standalone mode, using `ddev wp` only for local WP-CLI operations. The generated provider remains available for explicit `ddev pull` and `ddev push` usage.

If DDEV detection fails and the project has a `.wp-env.json` or `.wp-env.override.json`, the CLI reads `wp-env status --json` and runs in wp-env mode. A `node_modules/.bin/wp-env` alone is not a marker — many DDEV and standalone repos carry `@wordpress/env` as a dev dependency. See [wp-env mode](#wp-env-mode). Otherwise it runs in standalone mode against the local host.

## Documentation

Full documentation, including per-command guides, configuration reference, and troubleshooting, is published at **https://wp-ssh-bridge.citation.media**.

This repository is a monorepo. The Go CLI lives at the root; the documentation pages live in [`docs/`](docs/) and the Blume site that renders them in [`packages/documentation/`](packages/documentation/). The release and deploy pipeline is described in [`AGENTS.md`](AGENTS.md).

```bash
npm install
npm run docs:dev
```

## Install

The install script needs no access to this repository. It picks the right build for the machine, verifies the SHA-256 checksum, and installs into `.ddev/bin/` inside a DDEV project or the current directory otherwise:

```bash
curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
```

Use `--dir <path>` for another destination, `--global` for `/usr/local/bin`, or `--version <tag>` to pin a release.

## Use Per Project

Download the release artifact into each project and run it from there. This keeps the CLI scoped to the project and lets generated DDEV provider files point at that project-local binary.

```bash
mkdir -p .ddev/bin
rm -f .ddev/bin/wp-ssh-bridge_*_darwin_arm64.tar.gz
gh release download \
  --repo Citation-Media/wp-ssh-bridge \
  --pattern 'wp-ssh-bridge_*_darwin_arm64.tar.gz' \
  --dir .ddev/bin \
  --clobber
tar -C .ddev/bin -xzf .ddev/bin/wp-ssh-bridge_*_darwin_arm64.tar.gz
rm .ddev/bin/wp-ssh-bridge_*_darwin_arm64.tar.gz

./.ddev/bin/wp-ssh-bridge init
```

Global installation is optional convenience, not required:

```bash
rm -f wp-ssh-bridge_*_darwin_arm64.tar.gz
gh release download \
  --repo Citation-Media/wp-ssh-bridge \
  --pattern 'wp-ssh-bridge_*_darwin_arm64.tar.gz' \
  --clobber
tar -xzf wp-ssh-bridge_*_darwin_arm64.tar.gz
install wp-ssh-bridge /usr/local/bin/wp-ssh-bridge
```

You can also build directly from the private GitHub repository with Go:

```bash
git config --global url."git@github.com:".insteadOf "https://github.com/"
GOPRIVATE=github.com/Citation-Media go install github.com/Citation-Media/wp-ssh-bridge/cmd/wp-ssh-bridge@latest
```

## Configure A Project

Run this from a DDEV WordPress project to create provider files:

```bash
wp-ssh-bridge init
```

The interactive setup asks for the pull source, optional push target, local WordPress path, media behavior, and search-replace behavior. In DDEV mode it writes `.ddev/wp-ssh.yaml` and provider files. In wp-env and standalone mode it writes `.wp-ssh.yaml` and does not create DDEV provider files.

When it runs inside a DDEV project, it reads `ddev describe -j` and `.ddev/config.yaml` to default the provider name, local URL, docroot, and temp directories before writing config.

Generated DDEV files avoid machine-local absolute paths for versioned project files. Project-local binary paths are written as relative `./...` commands, and local project paths such as `local_wp_path` and `plugin_remove_file` are stored relative to the project root when possible.

Silent setup is available for repeatable project bootstrap:

```bash
wp-ssh-bridge init --silent \
  --destination deploy@example.com \
  --remote-path /home/example/public_html \
  --push-destination deploy@staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

`--destination` takes `user@host[:port]`, an `ssh://` URL, or an alias from `~/.ssh/config`; the split `--user`, `--host`, and `--port` flags remain for configs that use them. Password managers such as 1Password and Bitwarden work as ordinary SSH agents; jump hosts and pinned keys go into `~/.ssh/config`. See https://wp-ssh-bridge.citation.media/docs/ssh-access.

```bash
# The same setup with the split address form
wp-ssh-bridge init --silent \
  --user deploy \
  --host example.com \
  --remote-path /home/example/public_html
```

This creates:

```text
.ddev/wp-ssh.yaml
.ddev/providers/wp-ssh.yaml
.ddev/config.wp-ssh.yaml
```

Outside DDEV, this creates:

```text
.wp-ssh.yaml
```

The generated provider delegates to the host binary with `service: host`, so no shell scripts are installed into the project. SSH uses the same OpenSSH behavior as your terminal, including `~/.ssh/config`, keychain-loaded identities, and direct identity files.

The blocked-plugin defaults are embedded in the CLI. Set `plugin_remove_file` or `WP_SSH_PULL_PLUGIN_REMOVE_FILE` only when you want to add a project-specific plugin block list.

## Pull

Use the direct CLI wrapper. In DDEV mode this installs/refreshes provider files and runs the pull directly with SSH, rsync, and `ddev wp` for local WP-CLI work. Outside DDEV it uses local WP-CLI.

```bash
wp-ssh-bridge pull --silent
```

Or use DDEV after initialization when you specifically want DDEV's native pull lifecycle output:

```bash
ddev pull wp-ssh -y
```

For per-developer SSH users with native DDEV pull, commit a `~/.ssh/config` alias as the destination (`pull_destination: "prod"`) and let each developer own the `Host prod` block. To override the source for one run, pass it inline:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_DESTINATION=deploy@staging.example.com -y
```

You can also override the full pull source inline:

```bash
ddev pull wp-ssh \
  --environment=WP_SSH_PULL_DESTINATION=deploy@example.com,WP_SSH_PULL_REMOTE_PATH=/home/example/public_html \
  -y
```

One-shot overrides are supported:

```bash
wp-ssh-bridge pull --silent --destination deploy@example.com --remote-path /home/example/public_html
```

### Clone

Use `clone` when pulling a WordPress site as a standalone copy, for example when migrating it to a new host, instead of a local development copy. Clone mode still exports/imports the database, syncs files, and runs configured URL search-replace, but it skips DDEV/dev rewrites and blocked-plugin cleanup.

Clone is standalone-only: it copies a live site host-to-host into a plain target directory. `clone` exits with an error when run against a DDEV or wp-env project root — use the normal pull for local onboarding.

```bash
wp-ssh-bridge clone --silent \
  --destination deploy@source.example.com \
  --remote-path /home/source/public_html \
  --db-host db.example.com \
  --db-name target_db \
  --db-user target_user \
  --db-password target_password
```

Clone pulls copy `wp-config.php`, write the supplied target DB constants into it before database import, keep blocked plugins, and include uploads/media even when `clone_images` is false. `--db-prefix` is optional. There is no `push --clone`; cloning to a remote target needs a separate target `wp-config.php` rewrite design.

## Push

Use the direct CLI wrapper. In DDEV mode this installs/refreshes provider files and runs the push directly with SSH, rsync, and `ddev wp` for local WP-CLI work. Outside DDEV it uses local WP-CLI.

```bash
wp-ssh-bridge push --silent
```

Or use DDEV after initialization when you specifically want DDEV's native push lifecycle output:

```bash
ddev push wp-ssh -y
```

One-shot push target overrides are supported. For `push`, the concise `--destination`, `--user`, `--host`, `--port`, `--remote-path`, and `--remote-tmp-dir` flags also apply to the push target.

```bash
wp-ssh-bridge push --silent \
  --destination deploy@staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Push uploads/imports the local database with WP-CLI and rsyncs the full local WordPress app to the target. It excludes `wp-config.php`, `wp-config-ddev.php`, `.ddev/`, `.git/`, and the CLI scratch directory (`.wp-ssh/`) so local-only state and the downloaded database dump are never pushed.

## wp-env Mode

[`@wordpress/env`](https://developer.wordpress.org/block-editor/reference-guides/packages/packages-env/) keeps WordPress and MySQL inside Docker. The database is reachable only from the container network and the host MySQL port is randomized on every start, so local WP-CLI runs through `wp-env run cli` instead of a host `wp`.

Run setup and pull from the wp-env project root:

```bash
wp-ssh-bridge init
```

```bash
wp-ssh-bridge pull --silent
```

The environment must be started first (`wp-env start`); preflight fails with an actionable message when it is stopped.

What differs from standalone mode:

- Local WP-CLI runs as `wp-env run cli wp --path=/var/www/html`. wp-env writes its progress banners to stderr, so WP-CLI output stays parseable.
- The local WordPress root and URL are read from `wp-env status --json` on demand. The install path contains a machine-specific hash, so it is never written into `.wp-ssh.yaml`, which stays safe to commit.
- The database dump is written into `<install path>/WordPress/.wp-ssh/.downloads/` because that tree is what wp-env mounts at `/var/www/html`. The import then runs in the container against the mapped path.
- `wp-config.php` is preserved, not pulled. wp-env generates it with working local database credentials and rewrites it on every start, so the CLI leaves its URL constants alone.
- The experimental Playground runtime (`wp-env start --runtime=playground`) has no `wp-env run` command, so preflight rejects it. Use the Docker runtime.
- `clone` is rejected in wp-env projects for the same reason as in DDEV projects: the import would go to the container database instead of the clone target.
- `push` is rejected in wp-env projects. The local tree is a wp-env-managed core install, uploads are excluded from pulls by default, and every mounted `plugins`/`themes`/`mappings` path is empty on the host — pushing it with `rsync --delete` would erase those files on the target. Push from a standalone checkout instead.
- Blocked-plugin cleanup is skipped when `.wp-env.json` declares mounts, because `wp plugin delete` runs inside the container where those mounts are your working tree.
- The staged database dump is removed after a successful import and a deny-all `.htaccess` is written beside it, because the scratch directory is inside the tree wp-env serves over HTTP.

If `.wp-env.json` declares `plugins`, `themes`, or `mappings`, wp-env bind-mounts those source directories over `wp-content`. File pulls exclude those mounted paths automatically — they would be shadowed by the mounts, and `rsync --delete` against a live mountpoint can fail — and the CLI warns before pulling. Use `--skip-files` to sync only the database:

```bash
wp-ssh-bridge pull --silent --skip-files
```

### Persisting Configuration

`wp-ssh-bridge init` writes `.wp-ssh.yaml` at the wp-env project root. This is the wp-env equivalent of `.ddev/wp-ssh.yaml` and uses the same precedence: config file, then environment variables, then CLI flags. Because the wp-env install path and URL are resolved at run time, the file holds only portable values and can be committed:

```yaml
pull_destination: "deploy@production.example.com"
pull_remote_path: "/home/production/public_html"
clone_images: false
```

With that in place, `wp-ssh-bridge pull --silent` needs no arguments. Set `local_wp_path` or `local_url` only to override the values wp-env reports.

### Pinning The Integration

Runtime detection is automatic, but it can be pinned when a project is ambiguous — for example a repository that carries both `.ddev/config.yaml` and `.wp-env.json` — or when a silent fallback to standalone mode would be wrong:

```yaml
integration: "wp-env"
```

The same value is available as `--integration` and `WP_SSH_INTEGRATION`, in the usual precedence order (flag, environment, config file). Valid values are `ddev`, `wp-env`, and `standalone`.

A pin is strict. If it names a runtime that cannot be resolved, the CLI fails instead of quietly falling back to standalone mode and syncing into a different local WordPress root:

```text
Error: integration is pinned to wp-env, but no .wp-env.json was found above the working directory
```

Pinning is about determinism, not speed. `integration: standalone` does skip both probes (roughly 0.6s to 0.03s per invocation), but `integration: wp-env` is no faster than auto-detection, because `wp-env status --json` is still needed for the install path and to confirm the environment is running.

### Faster Invocations

The CLI resolves wp-env through a project-local `node_modules/.bin/wp-env` when present, then `wp-env` on `PATH`, then `npx --yes @wordpress/env`. The `npx` fallback resolves the package on every run and roughly doubles startup time, so add wp-env as a dev dependency:

```bash
npm install --save-dev @wordpress/env
```

Runtime detection is gated on filesystem markers, so no `ddev describe` or `wp-env status` subprocess runs for a project that cannot be that kind of project. In a wp-env project the remaining startup cost is almost entirely `wp-env status --json` itself, which is Node.js start-up rather than anything the CLI controls.

### Running Pull Automatically

wp-env has no pull/push lifecycle to hook into the way DDEV does, so no provider files are generated. It does support [`lifecycleScripts`](https://developer.wordpress.org/block-editor/reference-guides/packages/packages-env/) — `afterStart`, `afterReset`, `afterCleanup`, and `afterDestroy` — which can run the CLI for you:

```json
{
  "lifecycleScripts": {
    "afterStart": "wp-ssh-bridge pull --silent --skip-files"
  }
}
```

Use this deliberately. `afterStart` runs on every start, not only on a fresh environment, so a full pull on each `wp-env start` is usually the wrong trade. Prefer `afterReset`, or keep the pull an explicit command.

## WP-CLI Compatibility

Pull and push preflight checks verify WP-CLI before database or file changes start. The CLI checks the local host in standalone mode, the DDEV web container in DDEV mode, the wp-env `cli` container in wp-env mode, the pull source, and the push target when configured.

If remote `wp` is missing or unusable, the CLI downloads `wp-cli.phar` to the target's configured temporary directory as `wp-ssh-bridge-wp-cli.phar`, marks it executable, and tests it. If the fallback still cannot run directly or through `php`, the operation exits with a fatal error.

In standalone mode, a missing or unusable local `wp` is handled the same way by downloading a managed fallback phar into the project downloads directory.

### Netcup MariaDB client compatibility

Some Netcup/Plesk hosts report MariaDB through `mysql --version` but expose only the legacy `mysql` and `mysqldump` executable names. Current WP-CLI then looks for `mariadb` and `mariadb-dump`, which would otherwise make `wp db export` fail.

When this layout is found during database preflight, the CLI reports it and creates temporary remote aliases only for the database operation:

```text
✓ Pull source MariaDB client compatibility enabled (temporary mysql/mysqldump aliases)
```

The temporary aliases are removed on success and by the remote cleanup trap on failure. Remote database transfer files are also removed after download/import and are retried during error cleanup. The SCP/tar file fallback streams its archive over SSH, so it does not leave a remote tar file behind.

### Disabled PHP functions

Many shared hosts disable PHP's `exec()` and similar functions, often also on the command line. WP-CLI's `wp db export` calls `exec()` directly, so on these hosts the export dies with exit status 255 and no message. The workaround below is verified on Hostinger.

Pull preflight reads WP-CLI's PHP binary, `php.ini`, and entry script from `wp cli info`, or from the `#!` line of the `wp` script when that fails, and asks that PHP with plain `php -r` whether it disables `exec`, `proc_open`, `proc_close`, or `escapeshellarg`. No code runs inside WP-CLI, so a `wp-cli.yml` that disables `eval` does not affect the check. When a function is disabled, only the export runs with those functions removed from the host's list:

```text
✓ Pull source PHP function compatibility enabled (exec allowed for WP-CLI db export only)
```

The override is a `php -d disable_functions=…` startup option of that one process. No file is written, and the host's other disabled functions and every other PHP process are unchanged. Preflight stops the pull when the host blocks the override or the CLI cannot determine WP-CLI's PHP. Details: https://wp-ssh-bridge.citation.media/docs/troubleshooting/disabled-php-functions.

## Configuration

Project config lives in `.ddev/wp-ssh.yaml` in DDEV mode and `.wp-ssh.yaml` in wp-env and standalone mode.

Configuration can come from three places, in this order:

1. YAML config file.
2. Environment variables.
3. Direct CLI flags.

Direct flags are intended for one-shot usage:

```bash
wp-ssh-bridge pull --silent \
  --destination deploy@example.com \
  --remote-path /home/example/public_html
```

Use `--config-file` or `WP_SSH_CONFIG_FILE` to select another YAML file. Relative paths are resolved from the detected project root.

```bash
wp-ssh-bridge pull --silent --config-file .ddev/wp-ssh.production.yaml
```

Generated config files only persist values that differ from the CLI/runtime defaults. For example, default values such as `provider: "wp-ssh"`, `pull_remote_tmp_dir: "/tmp"`, `push_remote_tmp_dir: "/tmp"`, `clone_images: false`, and `skip_search_replace: false` are omitted.

| Key | Environment Override | Purpose |
| --- | --- | --- |
| Config path | `WP_SSH_CONFIG_FILE` | Optional YAML config file path. Relative paths resolve from the project root. |
| `pull_user` | `WP_SSH_PULL_USER` | Pull source SSH user. |
| `pull_host` | `WP_SSH_PULL_HOST` | Pull source SSH host. |
| `pull_port` | `WP_SSH_PULL_PORT` | Pull source SSH port. |
| `pull_remote_path` | `WP_SSH_PULL_REMOTE_PATH` | Pull source WordPress root containing `wp-config.php`. |
| `pull_remote_tmp_dir` | `WP_SSH_PULL_REMOTE_TMP_DIR` | Pull source temporary directory for DB exports. Defaults to `/tmp`. |
| `push_user` | `WP_SSH_PUSH_USER` | Push target SSH user. |
| `push_host` | `WP_SSH_PUSH_HOST` | Push target SSH host. |
| `push_port` | `WP_SSH_PUSH_PORT` | Push target SSH port. |
| `push_remote_path` | `WP_SSH_PUSH_REMOTE_PATH` | Push target WordPress root. |
| `push_remote_tmp_dir` | `WP_SSH_PUSH_REMOTE_TMP_DIR` | Push target temporary directory. Defaults to `/tmp`. |
| `push_url` | `WP_SSH_PUSH_URL` | Public target URL used for post-push search-replace. If omitted, the CLI captures the remote URL before DB import when possible. |
| `local_wp_path` | `WP_SSH_LOCAL_WP_PATH` or `WP_SSH_PULL_LOCAL_WP_PATH` | Local WordPress root relative to the DDEV project. |
| `clone_images` | `WP_SSH_PULL_CLONE_IMAGES` | Include `wp-content/uploads`. Defaults to `false`. |
| `plugin_remove_file` | `WP_SSH_PULL_PLUGIN_REMOVE_FILE` | Optional path to an additional project-specific blocked-plugin list. |
| `local_url` | `WP_SSH_PULL_LOCAL_URL` | Local URL for post-pull search-replace. |
| `pull_domain_replacements` | - | Optional pull-time old-to-new domain/URL replacements. Prefer protocol-less domains for multisite mappings. |
| `push_domain_replacements` | - | Optional push-time old-to-new domain/URL replacements. Usually the inverse of pull replacements. |
| `skip_search_replace` | `WP_SSH_PULL_SKIP_SEARCH_REPLACE` or `WP_SSH_PUSH_SKIP_SEARCH_REPLACE` | Skip post-pull and post-push URL replacement. |
| `clone_db_host` | `WP_SSH_CLONE_DB_HOST` | Target DB host written to `wp-config.php` during `clone`. |
| `clone_db_name` | `WP_SSH_CLONE_DB_NAME` | Target DB name written to `wp-config.php` during `clone`. |
| `clone_db_user` | `WP_SSH_CLONE_DB_USER` | Target DB user written to `wp-config.php` during `clone`. |
| `clone_db_password` | `WP_SSH_CLONE_DB_PASSWORD` | Target DB password written to `wp-config.php` during `clone`. |
| `clone_db_prefix` | `WP_SSH_CLONE_DB_PREFIX` | Optional target table prefix written to `wp-config.php` during `clone`. |

Add mappings to an already configured project with the CLI:

```bash
wp-ssh-bridge domains add --old example.com --new example.ddev.site
```

By default this writes both directions:

- `pull_domain_replacements`: `example.com` -> `example.ddev.site`
- `push_domain_replacements`: `example.ddev.site` -> `example.com`

Use `--direction pull` or `--direction push` when only one side should be changed.

Example multisite domain mapping:

```yaml
pull_domain_replacements:
  - old: "example.com"
    new: "example.ddev.site"
  - old: "shop.example.com"
    new: "shop.ddev.site"
push_domain_replacements:
  - old: "example.ddev.site"
    new: "example.com"
  - old: "shop.ddev.site"
    new: "shop.example.com"
```

Use protocol-less domains for multisite replacements whenever possible. WordPress multisite network tables and constants such as `DOMAIN_CURRENT_SITE` store hosts without `http://` or `https://`, and protocol-less mappings avoid missing those values. A protocol-less mapping is applied once to both bare domains and the hostname inside complete URLs, including when the local hostname contains the production hostname (for example `example.com.ddev.site`). Full URLs are still supported when path-aware replacements are needed.

In DDEV mode, provider install/pull/push derives local hosts from `pull_domain_replacements[].new` and `push_domain_replacements[].old`, then persists them in `.ddev/config.yaml` as `additional_hostnames`. It uses DDEV's resolved `DDEV_TLD` when available, so custom project TLDs are handled as well as the default `ddev.site`. DDEV also exposes every routed project FQDN in `DDEV_HOSTNAME`; the explicit replacement mappings remain the source of truth for deciding which production host maps to which local host. The search-replace behavior itself is core CLI behavior and also works outside DDEV.

Post-pull also updates hardcoded URL constants in `wp-config.php` so production values do not override the imported database:

```php
define('WP_HOME', ...);
define('WP_SITEURL', ...);
define('DOMAIN_CURRENT_SITE', ...);
```

In DDEV mode these constants are rewritten to use DDEV-provided environment variables, preferring `DDEV_PRIMARY_URL_WITHOUT_PORT` and falling back to `DDEV_PRIMARY_URL`. Outside DDEV they are rewritten as literal values from `local_url`, the first `pull_domain_replacements[].new` URL, or the detected local URL.

## Provider Generation

Regenerate provider files after updating the CLI:

```bash
wp-ssh-bridge provider install
```

Provider generation is DDEV-only. wp-env and standalone mode use the same Go implementation directly and do not need provider YAML.

Print generated YAML without writing files:

```bash
wp-ssh-bridge provider generate --kind all
```

## Behavior

`wp-ssh-bridge` keeps feature parity with the original provider:

- Verifies local SSH key authentication and shares one SSH connection per login for each CLI process, so an approving SSH agent asks once per direct `pull` or `push`. Each DDEV provider step runs as its own process and authenticates once.
- Verifies WP-CLI compatibility locally and on configured pull/push remotes before relying on WP-CLI operations.
- Exports the upstream database with remote WP-CLI and downloads `.ddev/.downloads/db.sql.gz`.
- Rsyncs the upstream WordPress root into the local WordPress root.
- Excludes `.git`, `.ddev`, DDEV config, `*.log` files at any depth, cache/backup folders, blocked plugins, and uploads unless `clone_images` is enabled. File pulls use plain rsync `--delete`; WordPress-tree log files are hidden sender-side so they are not copied and are deleted locally without deleting local-only project state such as DDEV's own logs.
- Sanitizes `wp-config.php` for DDEV-managed DB settings.
- Runs URL search-replace through `ddev wp`, including configured multisite domain mappings, protocol and host-only replacement pairs, per-blog search-replace, and multisite `site` and `blogs` domain tables.
- Removes only blocked local-only plugins reported by `wp plugin list`, using WP-CLI deactivate/delete commands.
- Pushes the local database to a separate SSH target with remote WP-CLI import.
- Pushes the full local WordPress app while excluding `wp-config.php`, `wp-config-ddev.php`, `.ddev/`, `.git/`, and the CLI scratch directory (`.wp-ssh/`).
- Runs post-push URL search-replace on the remote target with WP-CLI, including multisite `site` and `blogs` domain tables.

## Release Versioning

Version tags are the release source of truth:

```bash
git tag v0.3.4
git push origin v0.3.4
```

The `release` workflow tests the project, builds Linux and macOS artifacts for `amd64` and `arm64`, stamps `wp-ssh-bridge version` with the tag, publishes archives, and uploads SHA-256 checksums.

## Development

```bash
go test ./...
go build ./cmd/wp-ssh-bridge
```
