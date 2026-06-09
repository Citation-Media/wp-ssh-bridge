# DDEV WP SSH

`ddev-wp-ssh` is a Go CLI for pulling and pushing WordPress databases and full application files through SSH-only environments. It runs as a standalone host-side binary and uses a generated DDEV provider layer only when it detects a DDEV project.

The CLI detects DDEV mode by running `ddev describe -j` from the current directory. If that succeeds, `pull` and `push` route through `ddev pull` and `ddev push` with generated provider files. If it fails, the same commands run in standalone mode with interactive input and local WP-CLI.

## Use Per Project

Download the release artifact into each project and run it from there. This keeps the CLI scoped to the project and lets generated DDEV provider files point at that project-local binary.

```bash
mkdir -p .ddev/bin
gh release download v0.2.1 \
  --repo Citation-Media/ddev-wp-ssh \
  --pattern 'ddev-wp-ssh_v0.2.1_darwin_arm64.tar.gz' \
  --dir .ddev/bin
tar -C .ddev/bin -xzf .ddev/bin/ddev-wp-ssh_v0.2.1_darwin_arm64.tar.gz
rm .ddev/bin/ddev-wp-ssh_v0.2.1_darwin_arm64.tar.gz

./.ddev/bin/ddev-wp-ssh init
```

Global installation is optional convenience, not required:

```bash
gh release download v0.2.1 \
  --repo Citation-Media/ddev-wp-ssh \
  --pattern 'ddev-wp-ssh_v0.2.1_darwin_arm64.tar.gz'
tar -xzf ddev-wp-ssh_v0.2.1_darwin_arm64.tar.gz
install ddev-wp-ssh /usr/local/bin/ddev-wp-ssh
```

You can also build directly from the private GitHub repository with Go:

```bash
git config --global url."git@github.com:".insteadOf "https://github.com/"
GOPRIVATE=github.com/Citation-Media go install github.com/Citation-Media/ddev-wp-ssh/cmd/ddev-wp-ssh@v0.2.1
```

## Configure A Project

Run this from a DDEV WordPress project to create provider files:

```bash
ddev-wp-ssh init
```

The interactive setup asks for the pull source, optional push target, local WordPress path, media behavior, and search-replace behavior. In DDEV mode it writes `.ddev/wp-ssh.yaml` and provider files. In standalone mode it writes `.wp-ssh.yaml` and does not create DDEV provider files.

When it runs inside a DDEV project, it reads `ddev describe -j` and `.ddev/config.yaml` to default the provider name, local URL, docroot, and temp directories before writing config.

Silent setup is available for repeatable project bootstrap:

```bash
ddev-wp-ssh init --silent \
  --user deploy \
  --host example.com \
  --port 22 \
  --remote-path /home/example/public_html \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

This creates:

```text
.ddev/wp-ssh.yaml
.ddev/providers/wp-ssh.yaml
.ddev/config.wp-ssh.yaml
```

Outside DDEV, this creates:

```text
.wp-ssh.yaml
```

The generated provider delegates to the host binary with `service: host`, so no shell scripts are installed into the project. SSH uses the same OpenSSH behavior as your terminal, including `~/.ssh/config`, keychain-loaded identities, and direct identity files.

The blocked-plugin defaults are embedded in the CLI. Set `plugin_remove_file` or `WP_SSH_PULL_PLUGIN_REMOVE_FILE` only when you want to add a project-specific plugin block list.

## Pull

Use the direct CLI wrapper. In DDEV mode this installs/refreshes provider files and runs `ddev pull`; outside DDEV it runs the pull directly with SSH, rsync, and local WP-CLI.

```bash
ddev-wp-ssh pull --silent
```

Or use DDEV after initialization:

```bash
ddev pull wp-ssh -y
```

One-shot overrides are supported:

```bash
ddev-wp-ssh pull --silent --user deploy --host example.com --remote-path /home/example/public_html
```

## Push

Use the direct CLI wrapper. In DDEV mode this installs/refreshes provider files and runs `ddev push`; outside DDEV it runs the push directly with SSH, rsync, and local WP-CLI.

```bash
ddev-wp-ssh push --silent
```

Or use DDEV after initialization:

```bash
ddev push wp-ssh -y
```

One-shot push target overrides are supported. For `push`, the concise `--user`, `--host`, `--port`, `--remote-path`, and `--remote-tmp-dir` flags also apply to the push target.

```bash
ddev-wp-ssh push --silent \
  --user deploy \
  --host staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Push uploads/imports the local database with WP-CLI and rsyncs the full local WordPress app to the target. It excludes `wp-config.php`, `wp-config-ddev.php`, and `.ddev/`.

## Configuration

Project config lives in `.ddev/wp-ssh.yaml` in DDEV mode and `.wp-ssh.yaml` in standalone mode.

Configuration can come from three places, in this order:

1. YAML config file.
2. Environment variables.
3. Direct CLI flags.

Direct flags are intended for one-shot usage:

```bash
ddev-wp-ssh pull --silent \
  --user deploy \
  --host example.com \
  --remote-path /home/example/public_html
```

Use `--config-file` or `WP_SSH_CONFIG_FILE` to select another YAML file. Relative paths are resolved from the detected project root.

```bash
ddev-wp-ssh pull --silent --config-file .ddev/wp-ssh.production.yaml
```

Generated config files only persist values that differ from the CLI/runtime defaults. For example, default values such as `provider: "wp-ssh"`, `pull_remote_tmp_dir: "/tmp"`, `push_remote_tmp_dir: "/tmp"`, `clone_images: false`, and `skip_search_replace: false` are omitted.

| Key | Environment Override | Purpose |
| --- | --- | --- |
| Config path | `WP_SSH_CONFIG_FILE` | Optional YAML config file path. Relative paths resolve from the project root. |
| `pull_user` | `WP_SSH_PULL_USER` | Pull source SSH user. |
| `pull_host` | `WP_SSH_PULL_HOST` | Pull source SSH host. |
| `pull_port` | `WP_SSH_PULL_PORT` | Pull source SSH port. |
| `pull_remote_path` | `WP_SSH_PULL_REMOTE_PATH` | Pull source WordPress root containing `wp-config.php`. |
| `pull_remote_tmp_dir` | `WP_SSH_PULL_REMOTE_TMP_DIR` | Pull source temporary directory for DB exports. Defaults to `/tmp`. |
| `push_user` | `WP_SSH_PUSH_USER` | Push target SSH user. |
| `push_host` | `WP_SSH_PUSH_HOST` | Push target SSH host. |
| `push_port` | `WP_SSH_PUSH_PORT` | Push target SSH port. |
| `push_remote_path` | `WP_SSH_PUSH_REMOTE_PATH` | Push target WordPress root. |
| `push_remote_tmp_dir` | `WP_SSH_PUSH_REMOTE_TMP_DIR` | Push target temporary directory. Defaults to `/tmp`. |
| `push_url` | `WP_SSH_PUSH_URL` | Public target URL used for post-push search-replace. If omitted, the CLI captures the remote URL before DB import when possible. |
| `local_wp_path` | `WP_SSH_LOCAL_WP_PATH` or `WP_SSH_PULL_LOCAL_WP_PATH` | Local WordPress root relative to the DDEV project. |
| `clone_images` | `WP_SSH_PULL_CLONE_IMAGES` | Include `wp-content/uploads`. Defaults to `false`. |
| `plugin_remove_file` | `WP_SSH_PULL_PLUGIN_REMOVE_FILE` | Optional path to an additional project-specific blocked-plugin list. |
| `local_url` | `WP_SSH_PULL_LOCAL_URL` | Local URL for post-pull search-replace. |
| `skip_search_replace` | `WP_SSH_PULL_SKIP_SEARCH_REPLACE` or `WP_SSH_PUSH_SKIP_SEARCH_REPLACE` | Skip post-pull and post-push URL replacement. |

## Provider Generation

Regenerate provider files after updating the CLI:

```bash
ddev-wp-ssh provider install
```

Provider generation is DDEV-only. Standalone mode uses the same Go implementation directly and does not need provider YAML.

Print generated YAML without writing files:

```bash
ddev-wp-ssh provider generate --kind all
```

## Behavior

`ddev-wp-ssh` keeps feature parity with the original provider:

- Verifies local SSH key authentication.
- Exports the upstream database with remote WP-CLI and downloads `.ddev/.downloads/db.sql.gz`.
- Rsyncs the upstream WordPress root into the local WordPress root.
- Excludes `.git`, `.ddev`, DDEV config, cache/backup folders, blocked plugins, and uploads unless `clone_images` is enabled.
- Sanitizes `wp-config.php` for DDEV-managed DB settings.
- Runs URL search-replace through `ddev wp`, including multisite `site` and `blogs` domain tables.
- Removes blocked local-only plugins from the embedded default list and optional `plugin_remove_file`.
- Pushes the local database to a separate SSH target with remote WP-CLI import.
- Pushes the full local WordPress app while excluding `wp-config.php`, `wp-config-ddev.php`, and `.ddev/`.
- Runs post-push URL search-replace on the remote target with WP-CLI, including multisite `site` and `blogs` domain tables.

## Release Versioning

Version tags are the release source of truth:

```bash
git tag v0.2.1
git push origin v0.2.1
```

The `release` workflow tests the project, builds Linux and macOS artifacts for `amd64` and `arm64`, stamps `ddev-wp-ssh version` with the tag, publishes archives, and uploads SHA-256 checksums.

## Development

```bash
go test ./...
go build ./cmd/ddev-wp-ssh
```
