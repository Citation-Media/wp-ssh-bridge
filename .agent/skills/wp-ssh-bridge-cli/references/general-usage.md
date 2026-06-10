# General Usage

Use this reference for standalone WordPress projects or when the user explicitly asks to run `wp-ssh-bridge` directly instead of through DDEV.

## Standard Standalone Workflow

1. Confirm whether the user is pulling into local WordPress or pushing local WordPress to a remote target.
2. Confirm the SSH values the CLI cannot infer.
3. Run setup from the local WordPress project root:

```bash
wp-ssh-bridge init
```

4. Pull from the configured source:

```bash
wp-ssh-bridge pull --silent
```

5. Push only after confirming the target host, remote path, and target URL:

```bash
wp-ssh-bridge push --silent
```

Pull replaces the local database and syncs files from the pull source. Push overwrites the configured remote target database and syncs local files to that target.

## Required Values

Pull needs:

```text
pull_user or --user or WP_SSH_PULL_USER
pull_host or --host or WP_SSH_PULL_HOST
pull_remote_path or --remote-path or WP_SSH_PULL_REMOTE_PATH
```

Push needs:

```text
push_user or --push-user or WP_SSH_PUSH_USER
push_host or --push-host or WP_SSH_PUSH_HOST
push_remote_path or --push-remote-path or WP_SSH_PUSH_REMOTE_PATH
push_url or --push-url or WP_SSH_PUSH_URL
```

Remote WordPress paths must be absolute and point to a WordPress root containing `wp-config.php`.

## Config Mode

Standalone config lives at:

```text
.wp-ssh.yaml
```

Use config mode for repeatable project defaults:

```yaml
pull_user: "deploy"
pull_host: "production.example.com"
pull_remote_path: "/home/production/public_html"

push_user: "deploy"
push_host: "staging.example.com"
push_remote_path: "/home/staging/public_html"
push_url: "https://staging.example.com"

local_wp_path: "."
clone_images: false
skip_search_replace: false
```

Config is the base layer. Environment variables override config. CLI flags override both.

Use another config file when needed:

```bash
wp-ssh-bridge pull --silent --config-file .wp-ssh.production.yaml
```

## Env Mode

Use env mode for shell, CI, or temporary overrides:

```bash
export WP_SSH_PULL_USER=deploy
export WP_SSH_PULL_HOST=production.example.com
export WP_SSH_PULL_REMOTE_PATH=/home/production/public_html
```

```bash
export WP_SSH_PUSH_USER=deploy
export WP_SSH_PUSH_HOST=staging.example.com
export WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html
export WP_SSH_PUSH_URL=https://staging.example.com
```

Do not put private key contents in environment variables. SSH should use normal OpenSSH files, SSH config, or an agent.

## Args Mode

Use args mode for one-shot commands:

```bash
wp-ssh-bridge pull --silent \
  --user deploy \
  --host production.example.com \
  --remote-path /home/production/public_html
```

```bash
wp-ssh-bridge push --silent \
  --user deploy \
  --host staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Push also supports explicit push flags:

```bash
wp-ssh-bridge push --silent \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

## Operation Flags

Use these only when the user needs a partial or special operation:

```text
--skip-db              pull/push files only
--skip-files           pull/push database only
--skip-import          pull only; download DB without importing
--clone-images         include wp-content/uploads during pull
--skip-search-replace  skip URL replacement
--yes, -y              confirm direct operation
--silent               reduce output and skip direct confirmation
```

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

For missing required values, provide the smallest full command with `--user`, `--host`, and `--remote-path` for pull, or the push equivalents for push.
