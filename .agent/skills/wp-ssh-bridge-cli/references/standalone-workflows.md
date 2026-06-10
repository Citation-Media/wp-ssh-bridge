# Standalone Workflows

Use this reference when the project is not running through DDEV.

## Manual Values

When users are not using DDEV, focus on the values they still need to provide:

- Pull SSH user, host, optional port, and remote WordPress absolute path.
- Push SSH user, host, optional port, remote WordPress absolute path, and target URL.
- Local WordPress path when the command is not run from the WordPress root.
- Optional media, URL replacement, and plugin block-list choices.

Standalone project config is `.wp-ssh.yaml`.

## Setup

From the local WordPress project root:

```bash
wp-ssh-bridge init
```

Non-interactive setup:

```bash
wp-ssh-bridge init --silent \
  --user deploy \
  --host production.example.com \
  --remote-path /home/production/public_html \
  --local-wp-path .
```

Use `--project-root /path/to/project` when running from outside the project:

```bash
wp-ssh-bridge pull --silent --project-root /path/to/project
```

## Pull

```bash
wp-ssh-bridge pull --silent
```

One-shot pull:

```bash
wp-ssh-bridge pull --silent \
  --user deploy \
  --host production.example.com \
  --remote-path /home/production/public_html \
  --local-wp-path .
```

## Push

```bash
wp-ssh-bridge push --silent \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

For push, the concise `--user`, `--host`, and `--remote-path` flags can also target the push destination:

```bash
wp-ssh-bridge push --silent \
  --user deploy \
  --host staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

## Standalone Checks

Before recommending a standalone command, verify:

1. The local WordPress root exists.
2. `wp-config.php` is present in the selected local WordPress root.
3. SSH key auth works to the remote host.
4. The remote WordPress path is absolute and points at a WordPress root.
5. The user understands pull replaces local data and push replaces remote target data.
