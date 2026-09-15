# wp-env Usage

Use this reference for projects managed by `@wordpress/env` (`wp-env`).

The CLI enters wp-env mode when DDEV detection fails and the project root has `.wp-env.json` or `.wp-env.override.json`. A `node_modules/.bin/wp-env` alone is not a marker. It then reads `wp-env status --json` for the local URL and WordPress install path.

## Standard Workflow

1. Make sure the environment is running:

```bash
npx wp-env start
```

2. Configure from the wp-env project root:

```bash
wp-ssh-bridge init
```

3. Pull from the configured source:

```bash
wp-ssh-bridge pull --silent
```

`.wp-ssh.yaml` is written at the project root. It contains no wp-env install paths, so it is safe to commit.

## Persisting Configuration

`.wp-ssh.yaml` at the wp-env project root is the equivalent of `.ddev/wp-ssh.yaml`, with the same precedence: config file, then env vars, then flags. It holds only portable values, so it can be committed:

```yaml
pull_user: "deploy"
pull_host: "production.example.com"
pull_remote_path: "/home/production/public_html"
clone_images: false
```

After that, `wp-ssh-bridge pull --silent` needs no arguments. Recommend `local_wp_path` or `local_url` only to override what wp-env reports.

To speed up repeated runs, recommend installing wp-env as a dev dependency. The `npx` fallback resolves the package on every invocation and roughly doubles startup time:

```bash
npm install --save-dev @wordpress/env
```

wp-env has no pull/push provider lifecycle, so no provider files are generated. For automation, `.wp-env.json` supports `lifecycleScripts` (`afterStart`, `afterReset`, `afterCleanup`, `afterDestroy`):

```json
{
  "lifecycleScripts": {
    "afterStart": "wp-ssh-bridge pull --silent --skip-files"
  }
}
```

Warn the user that `afterStart` runs on every start, not only fresh environments, so a full pull there is usually wrong. Prefer `afterReset` or an explicit command.

## Pinning The Integration

Detection is automatic. Recommend pinning only when a project is ambiguous (both `.ddev/config.yaml` and `.wp-env.json` present) or when a silent fallback to standalone mode would sync into the wrong local WordPress root:

```yaml
integration: "wp-env"
```

Also available as `--integration` and `WP_SSH_INTEGRATION`, in that precedence order. Values: `ddev`, `wp-env`, `standalone`. A pin is strict and errors rather than falling back.

## What The CLI Does Differently

- Local WP-CLI runs as `wp-env run cli wp --path=/var/www/html`. A host `wp` cannot be used: wp-env's `DB_HOST` resolves only inside the Docker network, and the published MySQL port is randomized on every start.
- The local WordPress root and URL come from `wp-env status --json` at run time, so they are never stored in config.
- `wp-config.php` is preserved rather than pulled. wp-env generates it with working local credentials and rewrites it on every start.

## Mounted Plugin And Theme Directories

If `.wp-env.json` declares `plugins`, `themes`, or `mappings`, wp-env bind-mounts those host directories over `wp-content`. File pulls exclude those mounted paths automatically (they would be shadowed by the mounts, and deleting a live mountpoint can fail), and the CLI warns before pulling.

Recommend a database-only sync when the user only wants production content:

```bash
wp-ssh-bridge pull --silent --skip-files
```

## Common Failures

| Message | Fix |
| --- | --- |
| `wp-env environment is "stopped"` | Run `wp-env start`, then retry. |
| `wp-env is using the "playground" runtime` | The Playground runtime has no `wp-env run`. Restart with `wp-env start --runtime=docker`. |
| `wp-env status failed` | Run from the wp-env project root, and make sure `wp-env` is installed (project dependency, on `PATH`, or reachable via `npx`). |
| `this is a wp-env project but wp-env status --json did not succeed` | The project has `.wp-env.json` but wp-env could not be reached. Start it or install it. For pull only, `--integration standalone` treats the directory as a plain WordPress root; never use that override to reach `push` — it bypasses the wp-env push rejection and would push empty mounted directories over the target. |
| `local WordPress path ... is outside the wp-env tree` | `local_wp_path` points outside the mounted tree, so files and WP-CLI would target different installs. Clear it. |

## Not Supported

`wp-ssh-bridge push` exits with an error in wp-env projects. The local WordPress tree is managed by wp-env: uploads are excluded from pulls by default, and every `plugins`/`themes`/`mappings` path is a Docker bind mount that is empty on the host, so pushing it with `rsync --delete` would erase those files on the target. Tell the user to push from a standalone checkout of the site instead.

Blocked-plugin cleanup is skipped automatically when `.wp-env.json` declares mounts, because `wp plugin delete` runs inside the container where those mounts are the user's own source tree.

`wp-ssh-bridge migrate` exits with an error in wp-env projects. Migration injects target DB credentials and writes to a plain directory, but wp-env mode routes the import through the container database. Use the normal pull for local onboarding, and run `migrate` against a standalone target directory.

## Verification

```bash
npx wp-env run cli wp option get home
```

```bash
npx wp-env run cli wp post list --post_type=post --posts_per_page=5
```
