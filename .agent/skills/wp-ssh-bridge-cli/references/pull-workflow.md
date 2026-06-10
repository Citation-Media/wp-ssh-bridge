# Pull Workflow

Use this reference when the user wants to pull WordPress from a remote SSH host into local.

## Safety

Pull replaces the local database and syncs files from the pull source. Ask the user to confirm the source host and path when the command is not already clear.

Required pull values:

```text
pull_user or --user or WP_SSH_PULL_USER
pull_host or --host or WP_SSH_PULL_HOST
pull_remote_path or --remote-path or WP_SSH_PULL_REMOTE_PATH
```

## Recommended DDEV Pull

```bash
wp-ssh-bridge pull --silent
```

With one-shot args:

```bash
wp-ssh-bridge pull --silent \
  --user deploy \
  --host production.example.com \
  --remote-path /home/production/public_html
```

## Native DDEV Provider Pull

Use only when the user wants DDEV native lifecycle output:

```bash
ddev pull wp-ssh -y
```

Native DDEV pull does not accept `wp-ssh-bridge` flags. For a one-off SSH user override:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

For a persistent SSH user, set:

```yaml
pull_user: "deploy"
```

For per-developer SSH users, keep `pull_user` out of shared config and use:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

## Useful Pull Variants

Files only:

```bash
wp-ssh-bridge pull --silent --skip-db
```

Database only:

```bash
wp-ssh-bridge pull --silent --skip-files
```

Download database but do not import it:

```bash
wp-ssh-bridge pull --silent --skip-import
```

Include uploads/media:

```bash
wp-ssh-bridge pull --silent --clone-images
```

Skip URL replacement:

```bash
wp-ssh-bridge pull --silent --skip-search-replace
```

## Manual Pull Choices

- Set `--clone-images` only when uploads/media should be copied too.
- Set `--skip-db` when only files should be pulled.
- Set `--skip-files` when only the database should be pulled.
- Set `--skip-import` when the database dump should be downloaded but not imported.
- Set `--skip-search-replace` when local URLs should not be changed.
- Set `plugin_remove_file` only for project-specific blocked plugins beyond the embedded defaults.

## Post-Pull Verification

Use the checks that match the runtime:

```bash
ddev wp option get home
ddev wp plugin list
```

or standalone:

```bash
wp option get home --path=/path/to/wordpress
wp plugin list --path=/path/to/wordpress
```

If the user reports noisy output, prefer the direct wrapper (`wp-ssh-bridge pull --silent`) over `ddev pull wp-ssh`.
