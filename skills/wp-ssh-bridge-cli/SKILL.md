---
name: wp-ssh-bridge-cli
description: Guide for using the wp-ssh-bridge CLI to pull, clone, or push WordPress databases and files over SSH. Use this whenever the user asks how to configure, run, automate, or troubleshoot wp-ssh-bridge, DDEV pull/push provider mode, standalone WordPress syncs, wp-env (@wordpress/env) local environments, clone mode for host-to-host site migrations, SSH username/host/path options, args/env/config usage, plugin cleanup, or safe WordPress pull, clone, and push workflows.
---

# wp-ssh-bridge CLI

Use this skill to guide users through the manual decisions around `wp-ssh-bridge`. Keep answers practical: show the exact value, config key, env var, or command the user must provide. Avoid explaining internal behavior that the CLI already handles automatically unless the user is troubleshooting that behavior.

## Manual Inputs To Check

1. Confirm whether the user is pulling into local WordPress or pushing to a remote target.
2. Confirm the SSH target values the CLI cannot infer:
   - Pull: SSH user, host, optional port, and remote WordPress absolute path.
   - Push: push SSH user, host, optional port, remote WordPress absolute path, and preferably `push_url`.
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

Prefer the public install script. It needs no GitHub access, verifies the SHA-256 checksum, and installs to `.ddev/bin/wp-ssh-bridge` inside a DDEV project or into the current directory otherwise. Run it from the project root:

```bash title="Install the latest wp-ssh-bridge release"
curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
```

Use `--dir <path>` for another destination, `--global` for `/usr/local/bin`, and `--version <tag>` to pin a release.

When the user has repository access and prefers GitHub releases directly, the skill script does the same through `gh`:

```bash
skills/wp-ssh-bridge-cli/scripts/install-wp-ssh-bridge.sh dev/wordpress-default
```

Its first argument can be a DDEV project root or any destination folder, with the same `.ddev/bin` behavior.

Full documentation, including per-command guides and troubleshooting, is at https://wp-ssh-bridge.citation.media. Agents can query it through its MCP server at `https://wp-ssh-bridge.citation.media/mcp`.

## Router

Read the most specific workflow reference first. Read only one reference unless the user explicitly compares workflows or the first reference says another one is needed:

| User intent | Reference |
| --- | --- |
| Cloning a site to a standalone host (site migration), `wp-ssh-bridge clone`, target DB credential injection, or host-to-host copy questions | `references/clone.md` |
| DDEV projects, generated provider files, `ddev pull`, `ddev push`, or DDEV debugging | `references/ddev.md` |
| wp-env / `@wordpress/env` projects, `.wp-env.json`, or `wp-env run cli` troubleshooting | `references/wp-env.md` |
| Standalone usage, direct CLI commands, args/env/config mode, or non-DDEV troubleshooting | `references/general-usage.md` |

## Default Recommendations

- For DDEV projects, always guide the user through the DDEV provider workflow: run `wp-ssh-bridge init` from the DDEV project root first, then run `ddev pull <provider> -y` or, after explicit push confirmation, `ddev push <provider> -y`. The default provider is `wp-ssh` unless the user chose another provider name during setup.
- Use `wp-ssh-bridge pull --silent` for wp-env and standalone workflows, and `wp-ssh-bridge push --silent` for standalone workflows or when the user explicitly asks to bypass DDEV's native provider lifecycle.
- Never recommend push in a wp-env project: the CLI rejects it because the local tree is wp-env-managed and mounted plugin/theme/upload paths are empty on the host. Do not suggest `--integration standalone` to bypass that rejection — it would push those empty directories over the target. Read `references/wp-env.md`.
- For wp-env projects, the environment must be started (`wp-env start`) before pull, and the Docker runtime is required. Read `references/wp-env.md` before recommending commands.
- Runtime detection is automatic. Recommend the `integration` config key, `--integration` flag, or `WP_SSH_INTEGRATION` only to resolve ambiguity (a repo with both DDEV and wp-env config) or to make a wrong-mode fallback fail loudly. It is not a speed optimization except for `standalone`.
- Prefer config mode for repeatable project setup, args mode for one-off overrides, and env mode for automation or secrets-adjacent values.
- Keep project-local paths relative in config files so DDEV config can be versioned.
- For multisite/custom domains, prefer `wp-ssh-bridge domains add --old production.example.com --new local.ddev.site` over manual YAML edits. This writes `pull_domain_replacements` and inverse `push_domain_replacements` by default.
- Prefer protocol-less domains in `pull_domain_replacements` and `push_domain_replacements` for multisite. Use full URLs only for path-aware or scheme-specific replacement.
- Protocol-less mappings replace the hostname once in both full URLs and bare multisite domain values, including targets that contain the source hostname such as `example.com.ddev.site`. DDEV's resolved `DDEV_TLD` is used for custom TLDs; `DDEV_HOSTNAME` lists all routed FQDNs, but explicit domain mappings determine production-to-local pairing.
- Do not recommend copying private key material into config or env variables. SSH should use normal OpenSSH behavior, such as `~/.ssh/config`, loaded keys, or direct identity configuration outside this CLI.
- With native `ddev pull wp-ssh`, pass one-off target overrides inline with DDEV's `--environment=WP_SSH_*=...` flag; do not suggest `--user` after `ddev pull wp-ssh`.
- For clone-style pulls (host-to-host site migrations), read `references/clone.md` before recommending commands. Recommend `wp-ssh-bridge clone`; do not recommend `pull --clone`, `push --clone`, or the former `migrate` command name.
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
