---
name: wp-ssh-bridge-cli
description: Guide for installing, setting up, and using the wp-ssh-bridge CLI to pull, clone, or push WordPress databases and files over SSH. Use this whenever the user asks to install wp-ssh-bridge, set up or initialize a project for it, write its config, or run, automate, or troubleshoot wp-ssh-bridge, DDEV pull/push provider mode, standalone WordPress syncs, wp-env (@wordpress/env) local environments, clone mode for host-to-host site migrations, SSH username/host/path options, args/env/config usage, monorepos with several sites, plugin cleanup, or safe WordPress pull, clone, and push workflows.
---

# wp-ssh-bridge CLI

Use this skill to guide users through the manual decisions around `wp-ssh-bridge`, and to set projects up on their behalf. Keep answers practical: show the exact value, config key, env var, or command the user must provide. Avoid explaining internal behavior that the CLI already handles automatically unless the user is troubleshooting that behavior.

## Manual Inputs To Check

1. Confirm whether the user is pulling into local WordPress or pushing to a remote target.
2. Confirm the SSH target values the CLI cannot infer:
   - Pull: the SSH destination (`user@host[:port]`, an `ssh://` URL, or a `~/.ssh/config` alias) and the remote WordPress absolute path.
   - Push: the push SSH destination, remote WordPress absolute path, and preferably `push_url`.
   - Prefer one destination over separate user, host, and port values. The CLI rejects a destination beside the split values, so do not mix the two forms for the same target.
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
5. For push operations, explicitly verify the target host, remote path, and URL. Push overwrites the target database and files.

Do not ask the user to choose DDEV, wp-env, or standalone mode unless they are explicitly asking about mode behavior. The CLI detects that automatically. When working inside this repository, prefer checking the current `README.md`, `VERSION`, and `wp-ssh-bridge --help` output before giving version-specific commands.

## Install Or Update wp-ssh-bridge

Pick one route per project and stay with it. In DDEV projects the generated provider files call the binary by the path of whichever install ran `init`, so switching routes later means rerunning `wp-ssh-bridge provider install`.

**npm, for projects that already have a `package.json`.** The package is versioned with the project, needs no GitHub access, and downloads the matching release on install:

```bash
npm install --save-dev @citation-media/wp-ssh-bridge
npx wp-ssh-bridge version
```

Every command in this skill then runs as `npx wp-ssh-bridge <command>`. Every machine that runs `ddev pull` needs `npm install` first, because the provider files point at the package's binary inside `node_modules`.

**The install script, for everything else.** It verifies the SHA-256 checksum and installs to `.ddev/bin/wp-ssh-bridge` inside a DDEV project or into the current directory otherwise. Run it from the project root:

```bash
curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
```

Use `--dir <path>` for another destination, `--global` for `/usr/local/bin`, and `--version <tag>` to pin a release. In a DDEV project, run the binary as `./.ddev/bin/wp-ssh-bridge`.

When the user has repository access and prefers GitHub releases directly, the skill script does the same through `gh`:

```bash
skills/wp-ssh-bridge-cli/scripts/install-wp-ssh-bridge.sh dev/wordpress-default
```

Its first argument can be a DDEV project root or any destination folder, with the same `.ddev/bin` behavior.

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

- For DDEV projects, always guide the user through the DDEV provider workflow: run `wp-ssh-bridge init` from the DDEV project root first, then run `ddev pull <provider> -y` or, after explicit push confirmation, `ddev push <provider> -y`. The default provider is `wp-ssh` unless the user chose another provider name during setup.
- Use `wp-ssh-bridge pull --silent` for wp-env and standalone workflows, and `wp-ssh-bridge push --silent` for standalone workflows or when the user explicitly asks to bypass DDEV's native provider lifecycle.
- Never recommend push in a wp-env project: the CLI rejects it because the local tree is wp-env-managed and mounted plugin/theme/upload paths are empty on the host. Do not suggest `--integration standalone` to bypass that rejection — it would push those empty directories over the target. Read `references/wp-env.md`.
- For wp-env projects, the environment must be started (`wp-env start`) before pull, and the Docker runtime is required. Read `references/wp-env.md` before recommending commands.
- Runtime detection is automatic. Recommend the `integration` config key, `--integration` flag, or `WP_SSH_INTEGRATION` only to resolve ambiguity (a repo with both DDEV and wp-env config) or to make a wrong-mode fallback fail loudly. It is not a speed optimization except for `standalone`.
- Prefer config mode for repeatable project setup, args mode for one-off overrides, and env mode for automation or secrets-adjacent values.
- Keep project-local paths relative in config files so DDEV config can be versioned.
- In a monorepo, each site folder carries its own config. Use `--project-root <folder>` to drive a site from the repository root and `--config-file <name>` for a second environment of the same site, such as `.wp-ssh.staging.yaml`.
- For multisite/custom domains, prefer `wp-ssh-bridge domains add --old production.example.com --new local.ddev.site` over manual YAML edits. This writes `pull_domain_replacements` and inverse `push_domain_replacements` by default.
- Prefer protocol-less domains in `pull_domain_replacements` and `push_domain_replacements` for multisite. Use full URLs only for path-aware or scheme-specific replacement.
- Protocol-less mappings replace the hostname once in both full URLs and bare multisite domain values, including targets that contain the source hostname such as `example.com.ddev.site`. DDEV's resolved `DDEV_TLD` is used for custom TLDs; `DDEV_HOSTNAME` lists all routed FQDNs, but explicit domain mappings determine production-to-local pairing.
- Do not recommend copying private key material into config or env variables. SSH should use normal OpenSSH behavior, such as `~/.ssh/config`, loaded keys, or direct identity configuration outside this CLI. 1Password, Bitwarden, and Proton Pass work as ordinary SSH agents and need nothing from the CLI; their setup is the user's, not something to walk through. Bastions, proxy commands, and a pinned key (`IdentityFile <public key>` plus `IdentitiesOnly yes`, the fix for `Too many authentication failures`) belong in a `~/.ssh/config` `Host` block used as the destination; the CLI deliberately has no option for them. Full guidance: https://wp-ssh-bridge.citation.media/docs/ssh-access.
- For per-developer SSH users, recommend a committed `~/.ssh/config` alias as the destination rather than inline overrides; each developer then owns the `Host` block.
- When values must come from a secrets manager, use env mode with its run command: `op run --env-file=wp-ssh.env -- wp-ssh-bridge pull --silent` (secret references `op://vault/item/field`); `bws run --project-id <id> -- wp-ssh-bridge pull --silent` for Bitwarden Secrets Manager, with secrets named exactly like the `WP_SSH_*` variables and never `--no-inherit-env`, which drops the SSH agent socket; `pass-cli run --env-file=wp-ssh.env -- wp-ssh-bridge pull --silent` for Proton Pass (references `pass://vault/item/field`). Only `clone_db_password` is a real secret; keep non-secret config in the file. Details: https://wp-ssh-bridge.citation.media/docs/secrets-managers.
- With native `ddev pull wp-ssh`, pass one-off target overrides inline with DDEV's `--environment=WP_SSH_PULL_DESTINATION=...` flag; do not suggest `--destination` or `--user` after `ddev pull wp-ssh`.
- For clone-style pulls (host-to-host site migrations), read `references/clone.md` before recommending commands. Recommend `wp-ssh-bridge clone`; do not recommend `pull --clone`, `push --clone`, or the former `migrate` command name.
- A pull deliberately drops operational plugins: backup and migration tools, SMTP and mail senders, the security scanner, remote-management agents, and cloud image optimizers. They are excluded from the file sync and deleted locally afterwards, and the pull output lists what it removed. This is expected, not a failure; the full list is at https://wp-ssh-bridge.citation.media/docs/troubleshooting/blocked-plugins. A project extends the list with `plugin_remove_file` pointing at a text file of one slug per line, `#` comments allowed; the built-in entries cannot be switched off.
- A pull wraps the local database import in WordPress maintenance mode, and a push does the same on the target for the whole run; both lift it again even on failure. Recommend `--skip-maintenance-mode` only when the user asks for it or WP-CLI cannot toggle it on that side.
- The CLI runs preflight checks before pull/push work: local `ssh`, DDEV when applicable, local WP-CLI when DB work needs it, local path readability/writeability, SSH access, remote path access, remote temp writeability, and remote WP-CLI when DB work needs it.
- Netcup/Plesk hosts may report MariaDB through `mysql --version` while exposing only `mysql` and `mysqldump`. The CLI reports `MariaDB client compatibility enabled`, creates temporary remote `mariadb`/`mariadb-dump` aliases for database work, and removes aliases plus remote database transfer files on success or error. A failure mentioning missing `mariadb-dump` indicates an older CLI that needs updating or a host missing `mysqldump` too.
- File pulls use plain rsync `--delete` for stale synced paths and a sender-side `*.log` hide rule so WordPress-tree log files at any depth are not copied and are deleted locally, while local-only `.ddev/` including DDEV logs, `.git/`, config files, uploads when media cloning is off, and blocked-plugin paths are preserved for later CLI cleanup.
- When `rsync` is unavailable locally or on the remote host, the CLI automatically falls back to a tar-pipe-over-SSH transport for file and database transfers. Use `--force-scp` to force this fallback even when rsync is available. The scp/tar transport does not remove stale files (no `--delete` equivalent); the CLI emits a warning when it uses this path.
- Clones are additive by default on both transports (pre-existing target content is kept). For `clone` only, `--clean-target` removes pre-existing target content before syncing so it does not survive: on rsync it adds `--delete`, on scp/tar it empties the target WordPress directory first. It is destructive and rejected on `pull`/`push`; see `references/clone.md`. Normal (non-clone) pulls still mirror the source with rsync `--delete`.
- Do not instruct the AI to run lower-level provider callbacks, hand-written rsync commands, direct WP-CLI repair commands, or manual file edits as the normal DDEV workflow. If DDEV setup or pull fails, show the exact error, ask the user to confirm debugging, and suggest the smallest next debugging step.

## Response Shape

When answering usage questions, include:

1. The manual value being set and why the CLI cannot infer it.
2. The selected configuration mode: args, env, or config.
3. The exact command or file snippet.
4. A short safety note for destructive operations.
5. A verification command or expected successful output.

For troubleshooting, lead with the most likely cause, then show the smallest command or config change that fixes it.
