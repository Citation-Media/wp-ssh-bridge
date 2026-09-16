# Clone Usage

Use this reference when the user wants to copy or migrate a WordPress site between hosts, asks about `wp-ssh-bridge clone`, needs target DB credentials written into `wp-config.php`, or asks whether server-to-server transfer is appropriate.

## When To Use Clone Mode

Use `wp-ssh-bridge clone` when the target should remain a real site-like WordPress install, not a local development copy. Typical cases:

- staging to production-style target
- production to staging without DDEV/dev rewrites
- host A to a local target that will later become host B
- a one-off site migration where plugins, uploads, and `wp-config.php` should stay present

Do not use clone mode for ordinary local onboarding into DDEV. Use the normal DDEV or standalone pull workflow for that.

## Behavior

Clone pulls:

- export and import the database like a normal pull
- sync files from the pull source
- copy `wp-config.php`
- write target DB constants into `wp-config.php` before database import
- include uploads/media even when `clone_images` is false
- keep blocked plugins and do not run blocked-plugin cleanup
- skip DDEV/dev `wp-config.php` rewrites, including `WP_ENVIRONMENT_TYPE=development` and `wp-config-ddev.php`
- still run configured URL search-replace unless `--skip-search-replace` is set
- be additive by default: pre-existing files on the target are kept unless `--clean-target` is set (see "Emptying The Target" below)

Clone mode is a top-level `clone` command. Do not recommend `pull --clone` or `push --clone`.

## Required Values

Source SSH values:

```text
--user or pull_user or WP_SSH_PULL_USER
--host or pull_host or WP_SSH_PULL_HOST
--remote-path or pull_remote_path or WP_SSH_PULL_REMOTE_PATH
```

Target DB values:

```text
--db-host or clone_db_host or WP_SSH_CLONE_DB_HOST
--db-name or clone_db_name or WP_SSH_CLONE_DB_NAME
--db-user or clone_db_user or WP_SSH_CLONE_DB_USER
--db-password or clone_db_password or WP_SSH_CLONE_DB_PASSWORD
--db-prefix or clone_db_prefix or WP_SSH_CLONE_DB_PREFIX (optional)
```

Use configured `pull_domain_replacements` or `--skip-search-replace` according to the migration plan. If the target URL differs from the source URL, configure replacements before running the clone.

## Direct Command

Use args mode for one-off clones:

```bash
wp-ssh-bridge clone --silent \
  --user deploy \
  --host source.example.com \
  --remote-path /home/source/public_html \
  --db-host db.example.com \
  --db-name target_db \
  --db-user target_user \
  --db-password target_password
```

Add `--db-prefix wp_` only when the target table prefix should differ from the copied source `wp-config.php`.

## Emptying The Target (`--clean-target`)

By default a clone is additive: files are added or updated, but content already on the target (for example a web host's default `index.html` or a starter theme) stays in place. This holds on both transports — the rsync transport does not pass `--delete` for clones, and the scp/tar transport has no `--delete` equivalent. (A normal, non-clone `pull` still mirrors the source with rsync `--delete`.)

Pass `--clean-target` (clone only) to remove pre-existing target content before files are synced, so the cloned site starts from a clean tree on either transport:

- rsync transport: the CLI adds `--delete`, so files not present on the source are removed (configured excludes and operational paths are preserved).
- scp/tar transport: the CLI empties the target WordPress directory before extracting, since rsync `--delete` has no tar equivalent.

Safety:

- It is destructive and cannot be undone. Recommend it only when the user explicitly wants pre-existing target content removed, and confirm the resolved target path first.
- On the scp/tar transport it preserves only operational entries — `.git`, `.ddev`, `.wp-ssh`, `wp-config-ddev.php`, `.wp-ssh.yaml`, and the `wp-ssh-bridge` binary — and refuses to run against a filesystem root or the home directory. On rsync, the configured `--exclude` paths are preserved.
- It is rejected on `pull` and `push`; it exists only on `clone`.

## DDEV And wp-env Projects

`wp-ssh-bridge clone` does not support DDEV or wp-env projects and exits with an error when the project root is either. A clone is a live host-to-host copy into a standalone target, so it must run against a plain (non-DDEV) destination directory. Routing the database import through `ddev wp` or `wp-env run cli` would import into the local container database instead of the injected target credentials.

If the user wants a DDEV development copy, use the normal DDEV pull workflow in `references/ddev.md`. Reserve `clone` for standalone target directories. Do not recommend native `ddev pull <provider> --clone`; clone is intentionally exposed only as the standalone `wp-ssh-bridge clone` command.

## Config Mode

Use config mode for repeatable clone defaults:

```yaml
pull_user: "deploy"
pull_host: "source.example.com"
pull_remote_path: "/home/source/public_html"

clone_db_host: "db.example.com"
clone_db_name: "target_db"
clone_db_user: "target_user"
clone_db_password: "target_password"
clone_db_prefix: "wp_"

pull_domain_replacements:
  - old: "source.example.com"
    new: "target.example.com"
```

Run it with:

```bash
wp-ssh-bridge clone --silent
```

Prefer environment variables for password values when the config file is versioned.

## Server-To-Server Transfer

Do not present server-to-server transfer as the default clone path. The implemented clone path is local-orchestrated: source to host to target working tree.

Server-to-server can be faster, but it changes the trust and support model:

- the source server needs SSH reachability to the target server
- the source server needs target credentials or agent-forwarded access
- target host keys must be trusted from the source server
- many shared hosts block outbound SSH or have limited transfer tooling
- failures are harder to diagnose because both remote hosts are active participants

Recommend the local-orchestrated `clone` path unless the user explicitly accepts those requirements. Treat server-to-server as a future explicit workflow, not a mode that silently changes transport.

## Command Name Errors

The command was previously called `migrate`, with `migrate_db_*` config keys and `WP_SSH_MIGRATE_DB_*` environment variables.

- `unknown command "clone"`: the installed binary predates the rename. Update it with the skill install script.
- An error saying `migrate` was renamed to `clone`: the caller uses the old name. Run `wp-ssh-bridge clone` with the same flags.
- `unknown config key "migrate_db_host"` (or another `migrate_db_*` key): the config file still uses the old keys. Rename them to `clone_db_*`. Old `WP_SSH_MIGRATE_DB_*` variables are silently ignored instead, so `clone` reports the target DB credentials as missing until they are renamed to `WP_SSH_CLONE_DB_*`.

## Verification

After the clone, verify the target values that should have changed:

```bash
wp config get DB_HOST --path=/path/to/wordpress
wp config get DB_NAME --path=/path/to/wordpress
wp option get home --path=/path/to/wordpress
wp plugin list --path=/path/to/wordpress
```

The target is a standalone WordPress install, so verify with a direct `wp` against the target path (not `ddev wp`).

If URL replacement was skipped or misconfigured, check the configured `pull_domain_replacements` and rerun only after confirming the intended source and target domains.
