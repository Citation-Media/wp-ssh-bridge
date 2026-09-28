---
name: wp-ssh-bridge-cli
description: Guide for installing, setting up, and using the wp-ssh-bridge CLI to pull, clone, or push WordPress databases and files over SSH. Use this whenever the user asks to install wp-ssh-bridge, set up or initialize a project for it, write its config, or run, automate, or troubleshoot wp-ssh-bridge, DDEV pull/push provider mode, standalone WordPress syncs, wp-env (@wordpress/env) local environments, clone mode for host-to-host site migrations, SSH destination and remote path options, args/env/config usage, monorepos with several sites, plugin cleanup, or safe WordPress pull, clone, and push workflows.
---

# wp-ssh-bridge CLI

Use this skill to guide users through the manual decisions around `wp-ssh-bridge`, and to set projects up on their behalf. Keep answers practical: show the exact value, config key, env var, or command the user must provide. Avoid explaining internal behavior that the CLI already handles automatically unless the user is troubleshooting that behavior.

## Manual Inputs To Check

1. Confirm whether the user is pulling into local WordPress or pushing to a remote target.
2. Confirm the SSH values the CLI cannot infer:
   - Pull: the SSH destination (`user@host[:port]`, an `ssh://` URL, or a `~/.ssh/config` alias) and the remote WordPress absolute path.
   - Push: the push SSH destination, remote WordPress absolute path, and preferably `push_url`.
   - Prefer one destination over separate user, host, and port values, and never mix the two forms for the same target. A destination from `WP_SSH_PULL_DESTINATION` or `--destination` overrides a config file that uses the split keys, but split values from the environment or flags over a config that uses a destination are rejected with `the destination already carries the user, host, and port`. For IPv6 hosts, recommend a `~/.ssh/config` alias; literals are refused.
3. Confirm local values only when they are not obvious from the project:
   - Local WordPress path when WordPress is not at the default project/docroot location.
   - Whether uploads/media should be cloned during pull.
   - Whether URL search-replace should be skipped.
   - Multisite/custom domain mappings, when production domains must be rewritten locally or pushed back remotely.
   - Any project-specific blocked-plugin list.
   - For `clone`, the target DB host, name, user, password, and optional table prefix.
4. Confirm which configuration mode the user wants:
   - Args mode for one-shot commands.
   - Env mode for CI, shells, or temporary overrides.
   - Config mode for versioned project defaults.
5. For push operations, explicitly verify the push destination, remote path, and URL. Push overwrites the target database and files.

Do not ask the user to choose DDEV, wp-env, or standalone mode unless they are explicitly asking about mode behavior. The CLI detects that automatically. Before giving version-specific commands, check `wp-ssh-bridge version` and `wp-ssh-bridge --help`; beyond this skill, the docs site below is the reference.

## Install Or Update wp-ssh-bridge

Pick one route per project and stay with it. In DDEV projects the generated provider files call the binary by the path of whichever route ran `init`, so switching routes later means rerunning `wp-ssh-bridge provider install`.

**npm, for projects that already have a `package.json`.** The package is versioned with the project and downloads the matching release on install:

```bash
npm install --save-dev @citation-media/wp-ssh-bridge
npx wp-ssh-bridge version
```

Every command in this skill then runs as `npx wp-ssh-bridge <command>`. Every machine that runs `ddev pull` needs `npm install` first, because the provider files point at the package's binary inside `node_modules`.

**The install script, for everything else.** It verifies the SHA-256 checksum and installs to `.ddev/bin/wp-ssh-bridge` inside a DDEV project or into the current directory otherwise. Run it from the project root:

```bash
curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
```

Use `--dir <path>` for another folder, `--global` for `/usr/local/bin`, and `--version <tag>` to pin a release. In a DDEV project, run the binary as `./.ddev/bin/wp-ssh-bridge`.

To fetch the GitHub release through `gh` instead, run the script bundled with this skill, relative to the skill directory. Its argument is a DDEV project root or any folder, with the same `.ddev/bin` behavior:

```bash
scripts/install-wp-ssh-bridge.sh /path/to/project
```

The repository is public, so building from source needs only Go 1.24+: `go install github.com/Citation-Media/wp-ssh-bridge/cmd/wp-ssh-bridge@latest`.

Full documentation, including per-command guides and troubleshooting, is at https://wp-ssh-bridge.citation.media. When this skill and the matching reference do not cover an error or option, read https://wp-ssh-bridge.citation.media/llms.txt and follow it to the page you need; every page is also served as Markdown by appending `.md` to its path. Use the MCP server at `https://wp-ssh-bridge.citation.media/mcp` instead when it is already configured. Do not go there first; the skill and references are the curated answer.

## Set Up A Project Without Prompts

Follow this when asked to set up, initialize, or configure a project, or to get a first pull working. Plain `wp-ssh-bridge init` prompts interactively and blocks an agent; `--silent` takes every value from flags, environment, and existing config instead.

1. Collect the values the CLI cannot infer before running anything: the pull SSH destination (`user@host[:port]`, or a `~/.ssh/config` alias, which then supplies user and port itself) and the remote WordPress path. Push values are optional at init and can be added later. Ask for `local_wp_path` only when WordPress is not at the project root or, in DDEV, the docroot. Do not guess SSH targets or remote paths.
2. Run init from the project root, or point at it with `--project-root`:

```bash
wp-ssh-bridge init --silent \
  --destination deploy@production.example.com \
  --remote-path /home/production/public_html
```

   Add `--local-wp-path`, `--clone-images`, and `--push-destination` with the other `--push-*` flags as needed. `init --silent` validates the values, writes the config, and in DDEV mode generates the provider files. With an alias as destination it prints what `ssh -G` resolves it to; if that line names the wrong user or host, the fix is in `~/.ssh/config`, not in the CLI config.

3. Verify what was written before pulling. The files tell you which runtime the CLI detected:
   - DDEV: `.ddev/wp-ssh.yaml` plus `.ddev/providers/wp-ssh.yaml`.
   - wp-env and standalone: `.wp-ssh.yaml` at the project root.

   A DDEV project that ended up with `.wp-ssh.yaml` was treated as standalone because `ddev describe -j` failed. Rerun with `--integration ddev` to get the real error rather than a silent fallback, and start the project first if it asks for that.

4. Run the first pull. Uploads are excluded unless `--clone-images` is set or `clone_images: true` is in config:

```bash
ddev pull wp-ssh -y            # DDEV
wp-ssh-bridge pull --silent    # wp-env and standalone
```

5. Verify with the WP-CLI that matches the runtime: `ddev wp option get home`, `npx wp-env run cli wp option get home`, or `wp option get home --path=<local wp path>`. Then `... wp plugin list`.

6. The config contains no secrets unless `clone_db_password` is set, so commit it. Keep paths in it relative so it works for every checkout.

For CI, the same values can come from `WP_SSH_PULL_DESTINATION` and `WP_SSH_PULL_REMOTE_PATH` instead of flags. For a monorepo with several sites, see "Several sites in one repository" in `references/general-usage.md`.

## Router

Read the most specific workflow reference first. Read only one reference unless the user explicitly compares workflows or the first reference says another one is needed:

| User intent | Reference |
| --- | --- |
| Cloning a site to a standalone host (site migration), `wp-ssh-bridge clone`, target DB credential injection, or host-to-host copy questions | `references/clone.md` |
| DDEV projects, generated provider files, `ddev pull`, `ddev push`, or DDEV debugging | `references/ddev.md` |
| wp-env / `@wordpress/env` projects, `.wp-env.json`, or `wp-env run cli` troubleshooting | `references/wp-env.md` |
| Standalone usage, direct CLI commands, args/env/config mode, config keys and flags, monorepos, or non-DDEV troubleshooting | `references/general-usage.md` |

## Default Recommendations

- DDEV: run `wp-ssh-bridge init` from the DDEV project root first. For interactive use recommend native `ddev pull wp-ssh -y`: DDEV imports the dump, and each provider step authenticates separately. For scripts, one-off flags, a single SSH approval per run, or maintenance mode, run the binary directly with `wp-ssh-bridge pull --silent`. The provider is `wp-ssh` unless init set another name. Push with `ddev push wp-ssh -y` or `wp-ssh-bridge push` only after the user confirms the target. Details: `references/ddev.md`.
- wp-env and standalone: `wp-ssh-bridge pull --silent`; standalone push: `wp-ssh-bridge push --silent`.
- Push overwrites the target database and files. Only a direct run in DDEV prints the resolved source or target and asks `Continue` (`--yes` or `--silent` skip it). A standalone push has no confirmation step, so check the push values in the config (or `wp-ssh-bridge domains list`) before running it.
- After a successful import, a pull removes the local tables the pulled database does not contain: by default every such table in DDEV and wp-env, only the local installation's tables in standalone mode. `db_reset` (`--db-reset`, `WP_SSH_DB_RESET`) sets `database`, `installation`, or `none` (keep and report); a native `ddev pull` always empties the database. Exports cover only the WordPress installation's tables (on the source for pull and clone, locally for a direct push, which always exports afresh); another installation with a longer prefix and its own `options` and `users` tables is left out, and without a readable prefix or table list the whole database is exported.
- A push stops before maintenance mode and the upload with `the push target uses the table prefix …, but the local database uses …` when the target's `$table_prefix` differs from the local one; set `$table_prefix` in the target's `wp-config.php` to the local prefix, or pull from that target first. A target without WordPress yet skips the check.
- A first push into an empty target stops with `the push target URL is unknown because it could not be read from the target` until `push_url`, `WP_SSH_PUSH_URL`, `--push-url`, a push domain mapping, or `--skip-search-replace` is given.
- Never recommend push in a wp-env project; the CLI rejects it, and `--integration standalone` must not be used to bypass that. wp-env must be started (`wp-env start`) with the Docker runtime before a pull. Read `references/wp-env.md`.
- Runtime detection is automatic. Recommend the `integration` config key, `--integration` flag, or `WP_SSH_INTEGRATION` only to resolve ambiguity (a repository with both DDEV and wp-env config) or to make a wrong-mode fallback fail loudly. It is not a speed optimization except for `standalone`.
- Prefer config mode for repeatable project setup, args mode for one-off overrides, and env mode for automation. Keep project-local paths in config relative so the file can be committed.
- In a monorepo, each site folder carries its own config. Use `--project-root <folder>` to drive a site from the repository root and `--config-file <name>` for a second environment of the same site, such as `.wp-ssh.staging.yaml`.
- For multisite or custom domains, recommend `wp-ssh-bridge domains add --old production.example.com --new local.ddev.site` over manual YAML edits; it writes the domain mappings for both directions. Details: `references/general-usage.md`.
- Never put private key material in config or environment variables. SSH uses normal OpenSSH behavior: loaded keys, agents (1Password, Bitwarden, and Proton Pass work as ordinary SSH agents), and `~/.ssh/config`. Bastions, proxy commands, and a pinned key (`IdentityFile <public key>` plus `IdentitiesOnly yes`, the fix for `Too many authentication failures`) belong in a `Host` block used as the destination; the CLI has no option for them. For per-developer SSH users, recommend a committed `~/.ssh/config` alias as the destination. Full guidance: https://wp-ssh-bridge.citation.media/docs/ssh-access.
- Only `clone_db_password` is a real secret. To inject values from a secrets manager, wrap the command in its run command (env mode); see "Env Mode" in `references/general-usage.md`.
- With native `ddev pull wp-ssh`, pass one-off overrides with DDEV's `--environment=WP_SSH_PULL_DESTINATION=...`; `--destination` and other CLI flags do not work after `ddev pull wp-ssh`.
- For host-to-host site migrations, recommend `wp-ssh-bridge clone` (not `pull --clone`, `push --clone`, or the former `migrate`) and read `references/clone.md` first. A clone is additive for files (`--clean-target` empties the target files first) and by default removes the previous installation's tables the dump does not contain (`db_reset`: `installation` default, `database`, or `none`).
- A pull deliberately drops operational plugins: backup and migration tools, SMTP and mail senders, the security scanner, remote-management agents, cloud image optimizers, and the Cloudflare plugin. They are excluded from the file sync and deleted locally afterwards, and the pull output lists what it removed. This is expected, not a failure; the full list is at https://wp-ssh-bridge.citation.media/docs/troubleshooting/blocked-plugins. A project extends the list with `plugin_remove_file` pointing at a text file of one slug per line, `#` comments allowed; the built-in entries cannot be switched off.
- A direct pull and a push run in WordPress maintenance mode and lift it again even on failure; native `ddev pull` has none. `Skipping ... maintenance mode` on an empty target or with `clone --clean-target` is expected. Recommend `--skip-maintenance-mode` only when the user asks for it or WP-CLI cannot toggle it on that side. `Enabling local maintenance mode failed` with `The site you have requested is not installed` means the CLI needs updating.
- Preflight checks run before any transfer: local `ssh`, DDEV when applicable, local and remote WP-CLI when database work needs it, local path access, SSH access, the remote path, and a writable remote temp directory.
- `MariaDB client compatibility enabled` is expected on hosts that report MariaDB but only ship `mysql`/`mysqldump` (seen on Netcup/Plesk). An error about a missing `mariadb-dump` means the CLI needs updating or the host lacks `mysqldump` as well.
- Hosts that disable PHP `exec()` for the command line (verified on Hostinger) are handled automatically; preflight reports `PHP function compatibility enabled (exec allowed for WP-CLI db export only)`. A fatal error containing `does not allow re-enabling them` means the host blocks even that: ask the provider to allow the named functions for PHP CLI. `PHP function check failed ... could not determine the PHP that runs WP-CLI` means `wp cli info` fails on the host; have the user run it there. A silent exit 255 during export means the CLI needs updating. Details: https://wp-ssh-bridge.citation.media/docs/troubleshooting/disabled-php-functions.
- A direct run authenticates once per SSH login; repeated agent approval prompts within one run mean the CLI needs updating. Native DDEV provider steps authenticate once each.
- A pull mirrors the source with rsync `--delete`. Log files in the WordPress tree are not copied and are removed locally; `.ddev/`, `.git/`, config files, and uploads (when not cloned) stay.
- Without rsync on either side, files fall back to tar over SSH (messages say `scp/tar`), which cannot remove stale files and warns `scp/tar transport: stale local files not removed (no --delete equivalent)`. `--force-scp` forces it. The database dump always streams over SSH.
- Do not instruct the AI to run lower-level provider callbacks, hand-written rsync commands, direct WP-CLI repair commands, or manual file edits as the normal DDEV workflow. If DDEV setup or pull fails, show the exact error, ask the user to confirm debugging, and suggest the smallest next debugging step.

## Response Shape

When answering usage questions, include:

1. The manual value being set and why the CLI cannot infer it.
2. The selected configuration mode: args, env, or config.
3. The exact command or file snippet.
4. A short safety note for destructive operations.
5. A verification command or expected successful output.

For troubleshooting, lead with the most likely cause, then show the smallest command or config change that fixes it.
