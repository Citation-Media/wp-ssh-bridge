# DDEV Workflows

Use this reference when the user intentionally uses DDEV-native commands such as `ddev pull wp-ssh` or asks about DDEV provider files.

## What Users Still Provide Manually

The CLI handles DDEV detection and provider command wiring. Do not explain that by default. Focus on values DDEV cannot infer:

- Pull SSH user, host, optional port, and remote WordPress path.
- Push SSH user, host, optional port, remote WordPress path, and target URL.
- Local WordPress path only when the project docroot is not enough.
- Whether to clone uploads/media.
- Whether to skip URL search-replace.
- A project-specific blocked-plugin list when the embedded defaults are not enough.

## Recommended Direct Workflow

Prefer the direct wrapper for clean output:

```bash
wp-ssh-bridge pull --silent
```

```bash
wp-ssh-bridge push --silent
```

When the binary is installed per project, use the relative binary:

```bash
./.ddev/bin/wp-ssh-bridge pull --silent
```

The direct wrapper refreshes generated provider files, then runs the Go pipeline directly. This avoids DDEV's native provider headings while still using DDEV for local WP-CLI work.

## Native DDEV Provider Workflow

Use native DDEV provider commands only when the user explicitly wants DDEV's lifecycle output:

```bash
ddev pull wp-ssh -y
```

```bash
ddev push wp-ssh -y
```

DDEV itself prints lifecycle headings such as authentication, database import, and file import. The CLI cannot convert those DDEV-owned headings into checkmarked `wp-ssh-bridge` steps.

## One-Off User Override With Native DDEV Pull

Native `ddev pull wp-ssh` does not accept `wp-ssh-bridge` flags such as `--user`. Use DDEV's inline `--environment` flag:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

With a full one-off pull source:

```bash
ddev pull wp-ssh \
  --environment=WP_SSH_PULL_USER=deploy,WP_SSH_PULL_HOST=production.example.com,WP_SSH_PULL_REMOTE_PATH=/home/production/public_html \
  -y
```

For persistent config, edit `.ddev/wp-ssh.yaml`:

```yaml
pull_user: "deploy"
```

For teams where every developer has a different SSH user, keep `pull_user` out of the versioned `.ddev/wp-ssh.yaml` and have each developer pass it inline with `--environment=WP_SSH_PULL_USER=...`.

## Regenerate Provider Files

After updating the binary or changing provider names:

```bash
wp-ssh-bridge provider install
```

Use a custom provider name:

```bash
wp-ssh-bridge provider install --provider wp-ssh-staging
```

Print generated YAML without writing files:

```bash
wp-ssh-bridge provider generate --kind all
```

## DDEV Setup Workflow

1. Install the binary into `.ddev/bin` or ensure `wp-ssh-bridge` is on `PATH`.
2. Run `wp-ssh-bridge init`.
3. Confirm `.ddev/wp-ssh.yaml` contains the pull source and optional push target.
4. Commit `.ddev/wp-ssh.yaml`, `.ddev/providers/wp-ssh.yaml`, and `.ddev/config.wp-ssh.yaml` when the project wants versioned provider setup.
5. Keep machine-local absolute paths out of config. Use relative local paths such as `web`, `public`, or `.ddev/plugin-blocklist.txt`.
