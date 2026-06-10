# Push Workflow

Use this reference when the user wants to push local WordPress to a remote SSH target.

## Safety

Push overwrites the target database and syncs local WordPress files to the target. Always identify the push host, remote path, and target URL before giving or running a push command.

Required push values:

```text
push_user or --push-user or WP_SSH_PUSH_USER
push_host or --push-host or WP_SSH_PUSH_HOST
push_remote_path or --push-remote-path or WP_SSH_PUSH_REMOTE_PATH
```

`push_url` is strongly recommended so post-push URL replacement uses the intended public target URL.

## Recommended DDEV Push

```bash
wp-ssh-bridge push --silent
```

With concise one-shot args:

```bash
wp-ssh-bridge push --silent \
  --user deploy \
  --host staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

With explicit push args:

```bash
wp-ssh-bridge push --silent \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

## Native DDEV Provider Push

Use only when the user wants DDEV native lifecycle output:

```bash
ddev push wp-ssh -y
```

Native DDEV push does not accept `wp-ssh-bridge` flags. For one-off push target overrides:

```bash
ddev push wp-ssh \
  --environment=WP_SSH_PUSH_USER=deploy,WP_SSH_PUSH_HOST=staging.example.com,WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html,WP_SSH_PUSH_URL=https://staging.example.com \
  -y
```

## Useful Push Variants

Files only:

```bash
wp-ssh-bridge push --silent --skip-db
```

Database only:

```bash
wp-ssh-bridge push --silent --skip-files
```

Skip URL replacement:

```bash
wp-ssh-bridge push --silent --skip-search-replace
```

Use an alternate config:

```bash
wp-ssh-bridge push --silent --config-file .ddev/wp-ssh.staging.yaml
```

## Manual Push Choices

- Set `push_url` or `WP_SSH_PUSH_URL` so URL replacement uses the intended target URL.
- Set `--skip-db` when only files should be pushed.
- Set `--skip-files` when only the database should be pushed.
- Set `--skip-search-replace` when target URLs should not be changed.
- Confirm the target host and remote path before every push.

## Post-Push Verification

Ask the user to verify the remote site in a browser and, when SSH is available, run a remote WP-CLI check:

```bash
ssh deploy@staging.example.com 'cd /home/staging/public_html && wp option get home --allow-root'
```

If the target URL is wrong after push, set `push_url` or `WP_SSH_PUSH_URL` and rerun the database portion:

```bash
wp-ssh-bridge push --silent --skip-files --push-url https://staging.example.com
```
