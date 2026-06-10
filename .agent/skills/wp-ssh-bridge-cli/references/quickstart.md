# Quickstart

Use this reference when the user is installing or configuring `wp-ssh-bridge` for the first time.

## Requirements

- SSH key authentication from the local machine to the remote WordPress host.
- `rsync` available locally and on the remote host.
- A WordPress root on the remote host that contains `wp-config.php`.
- WP-CLI on the remote host, or remote `php` plus `curl` or `wget` so the CLI can install the managed `wp-cli.phar` fallback.

## Per-Project Install

Prefer installing the binary into the project so generated DDEV YAML can point at a project-local relative path. Use the skill script when `wp-ssh-bridge` is missing or when updating the local binary.

```bash
.agent/skills/wp-ssh-bridge-cli/scripts/install-wp-ssh-bridge.sh dev/wordpress-default
```

The script always downloads the latest release. The first argument can be a DDEV project root or any destination folder. For DDEV project roots it installs to `.ddev/bin/wp-ssh-bridge`; for any other folder it installs directly into that folder.

## First DDEV Setup

Run from the DDEV project root:

```bash
./.ddev/bin/wp-ssh-bridge init
```

The interactive setup asks for pull source SSH values, optional push target values, local WordPress path, media cloning behavior, and URL replacement behavior.

Generated DDEV files:

```text
.ddev/wp-ssh.yaml
.ddev/providers/wp-ssh.yaml
.ddev/config.wp-ssh.yaml
```

Regenerate provider files after updating the binary:

```bash
./.ddev/bin/wp-ssh-bridge provider install
```

## First Standalone Setup

Run outside a DDEV project:

```bash
wp-ssh-bridge init
```

Standalone setup writes:

```text
.wp-ssh.yaml
```

## Non-Interactive Bootstrap

Use `--silent` when config should be created without prompts:

```bash
wp-ssh-bridge init --silent \
  --user deploy \
  --host example.com \
  --remote-path /home/example/public_html \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Add `--local-wp-path web` or another project-relative docroot when WordPress is not in the project root.
