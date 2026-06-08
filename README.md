---
title: DDEV WP SSH Pull
description: Private DDEV add-on for pulling WordPress databases and files from SSH-only upstream environments.
---

## DDEV WP SSH Pull

`ddev-wp-ssh` installs a pull-only DDEV provider named `wp-ssh` for WordPress projects. It uses SSH key authentication, WP-CLI, and rsync to pull an upstream database and files into a local DDEV environment.

The add-on does not install push commands.

## Install

Install the private add-on from a versioned GitHub release. The `GITHUB_TOKEN` environment variable lets DDEV download the private release tarball.

```bash title="Install the private add-on"
GITHUB_TOKEN="$(gh auth token)" ddev add-on get Citation-Media/ddev-wp-ssh --version v0.1.1
ddev restart
```

Install a specific release by changing the version flag.

```bash title="Install a specific private release"
GITHUB_TOKEN="$(gh auth token)" ddev add-on get Citation-Media/ddev-wp-ssh --version v0.1.0
ddev restart
```

For local add-on development, install from a checkout path with `ddev add-on get /path/to/ddev-wp-ssh`.

## Configure

Create a project-specific config file for upstream connection settings.

```yaml title=".ddev/config.wp-ssh.local.yaml"
web_environment:
  - WP_SSH_PULL_HOST=example.com
  - WP_SSH_PULL_REMOTE_PATH=/home/example/public_html
  - WP_SSH_PULL_CLONE_IMAGES=false
```

Pass the SSH user at runtime.

```bash title="Pull from upstream"
ddev auth ssh
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

Pull media for a single run by overriding `WP_SSH_PULL_CLONE_IMAGES`.

```bash title="Pull database, files, and media"
ddev pull wp-ssh --environment="WP_SSH_PULL_USER=deploy,WP_SSH_PULL_CLONE_IMAGES=true" -y
```

## Configuration Variables

| Variable | Purpose |
| --- | --- |
| `WP_SSH_PULL_USER` | Remote SSH user. Pass with `ddev pull --environment`. |
| `WP_SSH_PULL_HOST` | Remote SSH host. |
| `WP_SSH_PULL_REMOTE_PATH` | Remote WordPress root containing `wp-config.php`, `wp-content/`, `wp-admin/`, and `wp-includes/`. |
| `WP_SSH_PULL_CLONE_IMAGES` | Set to `true` to download `wp-content/uploads`. Defaults to `false`. |
| `WP_SSH_PULL_LOCAL_WP_PATH` | Optional local WordPress root relative to `/var/www/html`. |
| `WP_SSH_PULL_PORT` | Optional SSH port override. |
| `WP_SSH_PULL_REMOTE_TMP_DIR` | Optional remote temporary directory for database exports. Defaults to `/tmp`. |
| `WP_SSH_PULL_PLUGIN_REMOVE_FILE` | Optional plugin removal list path. |
| `WP_SSH_PULL_LOCAL_URL` | Optional local URL override for post-pull search-replace. Defaults to `DDEV_PRIMARY_URL_WITHOUT_PORT`, then `DDEV_PRIMARY_URL`. |
| `WP_SSH_PULL_SKIP_SEARCH_REPLACE` | Set to `true` to skip post-pull URL replacement. |

## Pull Behavior

The provider exports the upstream database with WP-CLI, downloads it to `.ddev/.downloads/db.sql.gz`, and lets DDEV import it with the normal pull flow.

The provider rsyncs the upstream WordPress root into the local WordPress root. It excludes `.git/`, `.ddev/`, DDEV config files, common WordPress cache and backup folders, blocked plugin paths, and `wp-content/uploads/` unless `WP_SSH_PULL_CLONE_IMAGES=true`.

After DDEV imports the database and files, the add-on sanitizes `wp-config.php`, replaces upstream URLs with the local DDEV URL, and removes blocked plugins listed in `.ddev/commands/wp-plugins-removal.txt`.

## Plugin Removal

The add-on seeds `.ddev/commands/wp-plugins-removal.txt` when the file does not exist. This file is intentionally editable and is not overwritten on add-on updates.

Run plugin removal independently with:

```bash title="Remove blocked plugins"
ddev remove-blocked-plugins
```

## Development

Install the add-on from a local checkout for testing.

```bash title="Install from local checkout"
ddev add-on get /path/to/ddev-wp-ssh
ddev restart
```

Run local checks from the repository root.

```bash title="Run checks"
bash -n pull/wp-ssh-pull.sh
bash -n commands/web/wp-plugins-removal
ruby -e 'require "yaml"; ARGV.each { |path| YAML.load_file(path); puts "parsed #{path}" }' install.yaml config.wp-ssh.yaml providers/wp-ssh.yaml
```
