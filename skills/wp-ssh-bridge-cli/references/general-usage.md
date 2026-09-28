# General Usage

Use this reference for standalone WordPress projects or when the user explicitly asks to run `wp-ssh-bridge` directly instead of through DDEV.

## Standard Standalone Workflow

1. Confirm whether the user is pulling into local WordPress or pushing local WordPress to a remote target.
2. Confirm the SSH values the CLI cannot infer.
3. Run setup from the local WordPress project root. Without `--silent` the command prompts for every value, so an agent passes them as flags:

```bash
wp-ssh-bridge init --silent \
  --destination deploy@production.example.com \
  --remote-path /home/production/public_html
```

   Push values may be added on the same command with `--push-destination`, `--push-remote-path`, and `--push-url`, or later by editing the config. Missing push values are not an error at init; they are only required by `push`.

4. Pull from the configured source:

```bash
wp-ssh-bridge pull --silent
```

5. Push only after the user confirms the push destination, remote path, and target URL. A standalone push has no confirmation step, so check the push values in the config first:

```bash
wp-ssh-bridge push --silent
```

Pull replaces the local database and syncs files from the pull source. Push overwrites the configured remote target database and syncs local files to that target.

## Required Values

Pull needs:

```text
pull_destination or --destination or WP_SSH_PULL_DESTINATION
pull_remote_path or --remote-path or WP_SSH_PULL_REMOTE_PATH
```

Push needs:

```text
push_destination or --push-destination or WP_SSH_PUSH_DESTINATION
push_remote_path or --push-remote-path or WP_SSH_PUSH_REMOTE_PATH
```

`push_url` (`--push-url`, `WP_SSH_PUSH_URL`) is optional but strongly recommended, and required for a first push into an empty target, which otherwise stops with `the push target URL is unknown because it could not be read from the target`. A push domain mapping or `--skip-search-replace` also resolves that.

A destination is `user@host[:port]`, an `ssh://user@host:port` URL, or an alias from `~/.ssh/config`. IPv6 literals are refused because macOS' rsync cannot pass them on; put the address in a `Host` block and use the alias. Without a user it takes the one from `~/.ssh/config`, or the local username. The split form still exists — `pull_user`/`pull_host`/`pull_port`, `--user`/`--host`/`--port`, `WP_SSH_PULL_USER`/`_HOST`/`_PORT`, and the `push_` equivalents — but a destination beside any of them in the same place (file, environment, or flags) is rejected, so use one form per target. A destination from the environment or a flag replaces the split keys of the config file, so `WP_SSH_PULL_DESTINATION` overrides any project. The reverse is rejected: split values from the environment or flags over a config that uses a destination fail with `the destination already carries the user, host, and port`. On `push`, `--destination` is an alias for `--push-destination`, like the other short flags.

Remote WordPress paths must be absolute and point to a WordPress root containing `wp-config.php`.

## Config Mode

Standalone config lives at:

```text
.wp-ssh.yaml
```

Use config mode for repeatable project defaults:

```yaml
pull_destination: "deploy@production.example.com"
pull_remote_path: "/home/production/public_html"

push_destination: "deploy@staging.example.com:2222"
push_remote_path: "/home/staging/public_html"
push_url: "https://staging.example.com"

local_wp_path: "."
clone_images: false
skip_search_replace: false

# Optional. Pins the runtime instead of detecting it: ddev, wp-env, or standalone.
# Also available as --integration and WP_SSH_INTEGRATION. A pin is strict and fails
# rather than silently falling back, so use it when a repo is ambiguous. A one-shot
# --integration flag on pull/push is not persisted; only `init --integration` writes
# this key.
integration: "standalone"
```

Less common keys, all optional:

```yaml
pull_port: "2222"                       # split-form port when not using a destination; push_port for the target
pull_remote_tmp_dir: "/tmp"             # where the dump is written on the source host;
push_remote_tmp_dir: "/tmp"             # push_remote_tmp_dir for the target. Change when /tmp is small or noexec
local_url: "http://example.local"       # local URL for search-replace; only to override what DDEV/wp-env detect
plugin_remove_file: ".wp-ssh-plugins.txt"  # extra plugin slugs to remove on pull, one per line
provider: "wp-ssh"                      # DDEV provider name; DDEV only
```

Config is the base layer. Environment variables override config. CLI flags override both. `init` writes only values that differ from the defaults, so a small file is normal. An interactive standalone or wp-env pull, push, or clone also saves the values confirmed at its prompts. A `--silent` run never writes flag or environment values to the file, and `clone_db_password` is never written from the environment or a flag.

Use another config file when needed, or set `WP_SSH_CONFIG_FILE`:

```bash
wp-ssh-bridge pull --silent --config-file .wp-ssh.production.yaml
```

## Several Sites In One Repository

A monorepo needs no special support. Each site folder is its own project with its own config, and the project root is the directory the command runs in:

```text
repo/
├─ sites/
│  ├─ alpha/
│  │  ├─ .wp-ssh.yaml        pull source for alpha, local_wp_path: web
│  │  └─ web/
│  └─ beta/
│     ├─ .wp-ssh.yaml        pull source for beta, local_wp_path: public
│     └─ public/
└─ package.json
```

Run `init --silent` and `pull` inside each folder, or drive them from the repository root with `--project-root`:

```bash
wp-ssh-bridge init --silent --project-root sites/alpha --destination deploy@alpha.example.com --remote-path /var/www/alpha
wp-ssh-bridge pull --silent --project-root sites/alpha
```

`--config-file` is resolved from that project root, which is how one site keeps a staging source beside production:

```bash
wp-ssh-bridge pull --silent --project-root sites/alpha --config-file .wp-ssh.staging.yaml
```

`local_wp_path` is relative to the project root. Leave it out when the folder itself is the WordPress root.

## Domain Mappings

For multisite or custom domains, use the CLI to update the domain mappings (`pull_domain_replacements`, `push_domain_replacements`) of an initialized project instead of editing YAML:

```bash
wp-ssh-bridge domains add --old example.com --new example.local
```

By default this writes both directions:

```yaml
pull_domain_replacements:
  - old: "example.com"
    new: "example.local"
push_domain_replacements:
  - old: "example.local"
    new: "example.com"
```

Use `--direction pull` or `--direction push` only when the user explicitly wants a one-sided mapping.

Prefer protocol-less domains for WordPress multisite because `wp_site`, `wp_blogs`, and `DOMAIN_CURRENT_SITE` store host-only values. A protocol-less mapping replaces the hostname once in full URLs and bare domain values, even when the new host contains the old one (`example.com` to `example.com.ddev.site`). Use full URLs only for path-aware or scheme-specific replacements. Domain mappings have no environment variable.

List configured mappings with:

```bash
wp-ssh-bridge domains list
```

## Env Mode

Use env mode for shell, CI, or temporary overrides:

```bash
export WP_SSH_PULL_DESTINATION=deploy@production.example.com
export WP_SSH_PULL_REMOTE_PATH=/home/production/public_html
```

```bash
export WP_SSH_PUSH_DESTINATION=deploy@staging.example.com
export WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html
export WP_SSH_PUSH_URL=https://staging.example.com
```

Every config key except the domain mappings has an environment variable. The local WordPress root is `WP_SSH_LOCAL_WP_PATH` (`WP_SSH_PULL_LOCAL_WP_PATH` also works). The pull-side ones are `WP_SSH_PULL_DESTINATION`, `WP_SSH_PULL_USER`, `WP_SSH_PULL_HOST`, `WP_SSH_PULL_PORT`, `WP_SSH_PULL_REMOTE_PATH`, `WP_SSH_PULL_REMOTE_TMP_DIR`, `WP_SSH_PULL_LOCAL_URL`, `WP_SSH_PULL_CLONE_IMAGES`, `WP_SSH_PULL_SKIP_SEARCH_REPLACE`, and `WP_SSH_PULL_PLUGIN_REMOVE_FILE`; the push side is `WP_SSH_PUSH_DESTINATION`, `WP_SSH_PUSH_USER`, `WP_SSH_PUSH_HOST`, `WP_SSH_PUSH_PORT`, `WP_SSH_PUSH_REMOTE_PATH`, `WP_SSH_PUSH_REMOTE_TMP_DIR`, `WP_SSH_PUSH_URL`, and `WP_SSH_PUSH_SKIP_SEARCH_REPLACE`. `WP_SSH_DB_RESET` sets the table cleanup after a pull or clone, and `WP_SSH_SKIP_CACHE_REBUILD` skips the page builder CSS rebuild. `WP_SSH_CONFIG_FILE`, `WP_SSH_INTEGRATION`, and `WP_SSH_PROVIDER` select the config file, runtime, and DDEV provider. Clone targets use `WP_SSH_CLONE_DB_*`.

Do not put private key contents in environment variables. SSH should use normal OpenSSH files, SSH config, or an agent. Only `clone_db_password` is a real secret; keep non-secret values in the config file. To inject variables from a secrets manager for one run, wrap the command: `op run --env-file=wp-ssh.env -- wp-ssh-bridge pull --silent` (1Password), `bws run --project-id <id> -- wp-ssh-bridge pull --silent` (Bitwarden Secrets Manager; name the secrets exactly like the `WP_SSH_*` variables and never add `--no-inherit-env`, which drops the SSH agent socket), `pass-cli run --env-file=wp-ssh.env -- wp-ssh-bridge pull --silent` (Proton Pass). Details: https://wp-ssh-bridge.citation.media/docs/secrets-managers.

## Args Mode

Use args mode for one-shot commands:

```bash
wp-ssh-bridge pull --silent \
  --destination deploy@production.example.com \
  --remote-path /home/production/public_html
```

```bash
wp-ssh-bridge push --silent \
  --destination deploy@staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Push also supports explicit push flags:

```bash
wp-ssh-bridge push --silent \
  --push-destination deploy@staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

## Operation Flags

Use these only when the user needs a partial or special operation:

```text
--skip-db                pull/push files only
--skip-files             pull/push database only
--skip-import            pull only; download DB without importing
--clone-images           include wp-content/uploads during pull
--skip-search-replace    skip URL replacement
--skip-maintenance-mode  do not enable WordPress maintenance mode around the import
--skip-cache-rebuild     skip page builder CSS rebuild (config skip_cache_rebuild, env WP_SSH_SKIP_CACHE_REBUILD)
--db-reset <mode>        pull and clone: tables to remove after the import when the dump lacks them: database, installation, or none (config db_reset, env WP_SSH_DB_RESET)
--force-scp              use the tar-over-SSH transport even when rsync exists
--yes, -y                skip the confirmation prompt of a direct DDEV run
--silent                 no prompts; take every value from config, environment, and flags
                         (also skips the DDEV direct confirmation)
```

Flags that select where things are, accepted by init, pull, clone, and push:

```text
--project-root <dir>        run against another project folder instead of the cwd
--config-file <path>        config file, resolved from the project root
--integration <mode>        pin ddev, wp-env, or standalone for this run
--destination, --push-destination
                            the whole SSH address; replaces --user/--host/--port
--port, --push-port         split-form SSH ports when not using a destination
--remote-tmp-dir <dir>      temp dir on the source host; --push-remote-tmp-dir for the target
--local-wp-path <dir>       local WordPress root, relative to the project root or absolute
--local-url <url>           local URL for search-replace, overrides the detected one
--plugin-remove-file <path> project plugin block list
```

For clones (host-to-host site migrations), use `references/clone.md` instead of this general workflow.

## Verification

Use the checks that match the local WordPress path:

```bash
wp option get home --path=/path/to/wordpress
wp plugin list --path=/path/to/wordpress
```

## Troubleshooting

When a command fails, show the exact error and suggest the smallest next check. Do not invent lower-level repair steps before the user confirms debugging.

Common checks:

```bash
wp-ssh-bridge version
wp-ssh-bridge pull --silent --config-file .wp-ssh.yaml
```

After a DB pull, clone, or push the CLI rebuilds Elementor, Bricks, and Beaver Builder CSS when their WP-CLI command exists. A `Could not rebuild ... CSS` warning does not mean the operation failed; the data transfer already finished. Show the warning details and suggest running the builder command manually on the affected site, for example `wp elementor flush-css`, which loads plugins and surfaces the real error.

For missing required values, provide the smallest full command with `--destination` and `--remote-path` for pull, or the push equivalents for push. For `Permission denied` with a password-manager agent, check `ssh -G <destination> | grep -i identityagent` and `ssh-add -L` before anything else.

Table prefix: a pull rewrites the local `$table_prefix` in `wp-config.php` to the pull source prefix when they differ and the imported `<prefix>options` table exists. Tables are not renamed. If the site shows the WordPress installer after a pull, or the CLI warns that `wp-config.php` has no `$table_prefix` assignment or that another file overrides it, compare both sides and set the prefix where the local config defines it:

```bash
wp config get table_prefix --path=/path/to/wordpress
```

Table cleanup after the import (`db_reset`), installation-only exports, and the push stop on a differing target `$table_prefix` are described in "Default Recommendations" in `SKILL.md`. Leftover tables after a pull mean `db_reset` is `none`, or, in standalone mode, the local `$table_prefix` could not be read (the CLI warns).
