---
name: wp-ssh-bridge-cli
description: Guide for using the wp-ssh-bridge CLI to pull or push WordPress databases and files over SSH. Use this whenever the user asks how to configure, run, automate, or troubleshoot wp-ssh-bridge, DDEV pull/push provider mode, standalone WordPress syncs, SSH username/host/path options, args/env/config usage, plugin cleanup, or safe WordPress pull and push workflows.
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
   - Any project-specific blocked-plugin list.
4. Confirm which configuration mode the user wants:
   - Args mode for one-shot commands.
   - Env mode for CI, shells, or temporary overrides.
   - Config mode for versioned project defaults.
5. For push operations, explicitly verify the target host, remote path, and URL. Push overwrites the target database and files.

Do not ask the user to choose DDEV mode versus standalone mode unless they are explicitly asking about mode behavior. The CLI detects that automatically. When working inside this repository, prefer checking the current `README.md`, `VERSION`, and `wp-ssh-bridge --help` output before giving version-specific commands.

## Install Or Update wp-ssh-bridge

Run the skill script from the repository root when `wp-ssh-bridge` is missing or when updating the local binary. The script always installs the latest GitHub release for the current OS and CPU architecture.

```bash title="Install the latest wp-ssh-bridge release"
.agent/skills/wp-ssh-bridge-cli/scripts/install-wp-ssh-bridge.sh dev/wordpress-default
```

The first argument can be a DDEV project root or any destination folder. When the folder contains `.ddev/config.yaml`, the binary is installed to `.ddev/bin/wp-ssh-bridge`; otherwise it is installed directly into the provided folder.

## Router

Read only one workflow reference unless the user explicitly compares DDEV and standalone usage:

| User intent | Reference |
| --- | --- |
| DDEV projects, generated provider files, `ddev pull`, `ddev push`, or DDEV debugging | `references/ddev.md` |
| Standalone usage, direct CLI commands, args/env/config mode, or non-DDEV troubleshooting | `references/general-usage.md` |

## Default Recommendations

- For DDEV projects, always guide the user through the DDEV provider workflow: run `wp-ssh-bridge init` from the DDEV project root first, then run `ddev pull <provider> -y` or, after explicit push confirmation, `ddev push <provider> -y`. The default provider is `wp-ssh` unless the user chose another provider name during setup.
- Use `wp-ssh-bridge pull --silent` and `wp-ssh-bridge push --silent` only for standalone workflows or when the user explicitly asks to bypass DDEV's native provider lifecycle.
- Prefer config mode for repeatable project setup, args mode for one-off overrides, and env mode for automation or secrets-adjacent values.
- Keep project-local paths relative in config files so DDEV config can be versioned.
- Do not recommend copying private key material into config or env variables. SSH should use normal OpenSSH behavior, such as `~/.ssh/config`, loaded keys, or direct identity configuration outside this CLI.
- With native `ddev pull wp-ssh`, pass one-off target overrides inline with DDEV's `--environment=WP_SSH_*=...` flag; do not suggest `--user` after `ddev pull wp-ssh`.
- Do not instruct the AI to run lower-level provider callbacks, hand-written rsync commands, direct WP-CLI repair commands, or manual file edits as the normal DDEV workflow. If DDEV setup or pull fails, show the exact error, ask the user to confirm debugging, and suggest the smallest next debugging step.

## Response Shape

When answering usage questions, include:

1. The manual value being set and why the CLI cannot infer it.
2. The selected configuration mode: args, env, or config.
3. The exact command or file snippet.
4. A short safety note for destructive operations.
5. A verification command or expected successful output.

For troubleshooting, lead with the most likely cause, then show the smallest command or config change that fixes it.
