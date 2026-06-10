# Quickstart

Use this reference when the user is installing or configuring `wp-ssh-bridge` for the first time.

## Requirements

- SSH key authentication from the local machine to the remote WordPress host.
- `rsync` available locally and on the remote host.
- A WordPress root on the remote host that contains `wp-config.php`.
- WP-CLI on the remote host, or remote `php` plus `curl` or `wget` so the CLI can install the managed `wp-cli.phar` fallback.

## Per-Project Install

Prefer installing the binary into the project so generated DDEV YAML can point at a project-local relative path.

```bash
mkdir -p .ddev/bin
gh release download v0.3.0 \
  --repo Citation-Media/wp-ssh-bridge \
  --pattern 'wp-ssh-bridge_v0.3.0_darwin_arm64.tar.gz' \
  --dir .ddev/bin
tar -C .ddev/bin -xzf .ddev/bin/wp-ssh-bridge_v0.3.0_darwin_arm64.tar.gz
rm .ddev/bin/wp-ssh-bridge_v0.3.0_darwin_arm64.tar.gz
```

Use the current release version from `VERSION` or the GitHub releases page when these examples age.

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
