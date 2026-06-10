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

## Router

Read only the relevant reference files:

| User intent | Reference |
| --- | --- |
| New setup, install, or first run | `references/quickstart.md` |
| Args vs env vs config, precedence, or exact keys | `references/configuration-modes.md` |
| DDEV projects, generated provider files, `ddev pull wp-ssh`, or clean DDEV output | `references/ddev-workflows.md` |
| Non-DDEV WordPress projects or local WP-CLI usage | `references/standalone-workflows.md` |
| Pulling production/staging into local | `references/pull-workflow.md` |
| Pushing local to staging/remote | `references/push-workflow.md` |
| Errors, noisy output, SSH username, WP-CLI, plugins, or path problems | `references/troubleshooting.md` |

## Default Recommendations

- Prefer `wp-ssh-bridge pull --silent` and `wp-ssh-bridge push --silent` for clean direct CLI output.
- Use `ddev pull wp-ssh -y` or `ddev push wp-ssh -y` only when the user specifically wants DDEV's native provider lifecycle output.
- Prefer config mode for repeatable project setup, args mode for one-off overrides, and env mode for automation or secrets-adjacent values.
- Keep project-local paths relative in config files so DDEV config can be versioned.
- Do not recommend copying private key material into config or env variables. SSH should use normal OpenSSH behavior, such as `~/.ssh/config`, loaded keys, or direct identity configuration outside this CLI.
- With native `ddev pull wp-ssh`, pass one-off target overrides inline with DDEV's `--environment=WP_SSH_*=...` flag; do not suggest `--user` after `ddev pull wp-ssh`.

## Response Shape

When answering usage questions, include:

1. The manual value being set and why the CLI cannot infer it.
2. The selected configuration mode: args, env, or config.
3. The exact command or file snippet.
4. A short safety note for destructive operations.
5. A verification command or expected successful output.

For troubleshooting, lead with the most likely cause, then show the smallest command or config change that fixes it.
