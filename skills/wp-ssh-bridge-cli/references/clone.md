# Clone Usage

Use this reference when the user wants to copy or migrate a WordPress site between hosts, asks about `wp-ssh-bridge clone`, needs target DB credentials written into `wp-config.php`, or asks whether server-to-server transfer is appropriate.

## When To Use Clone Mode

Use `wp-ssh-bridge clone` when the target should remain a live-ready WordPress installation, not a local development copy. Typical cases:

- staging to production-style target
- production to staging without DDEV/dev rewrites
- host A to a local target that will later become host B
- a one-off site migration where plugins, uploads, and `wp-config.php` should stay present

Do not use clone mode for ordinary local onboarding into DDEV. Use the normal DDEV or standalone pull workflow for that.

## Behavior

A clone:

- exports and imports the database like a normal pull
- syncs files from the source
- copies `wp-config.php`
- writes target DB constants into `wp-config.php` before the database import
- includes uploads/media even when `clone_images` is false
- keeps blocked plugins and does not run blocked-plugin cleanup
- skips DDEV/dev `wp-config.php` rewrites, including `WP_ENVIRONMENT_TYPE=development` and `wp-config-ddev.php`
- still runs configured URL search-replace unless `--skip-search-replace` is set
- rebuilds page builder CSS (Elementor, Bricks, Beaver Builder) on the target unless `--skip-cache-rebuild` is set; failures are warnings only
- is additive for files by default: pre-existing files on the target are kept unless `--clean-target` is set (see "Emptying The Target" below)
- replaces the previous installation's tables (see "Replacing An Existing Installation" below)

Clone mode is a top-level `clone` command. Do not recommend `pull --clone`, `push --clone`, or `ddev pull <provider> --clone`.

## Required Values

Source SSH values:

```text
--destination or pull_destination or WP_SSH_PULL_DESTINATION
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

Without a target URL (`local_url`, `WP_SSH_PULL_LOCAL_URL`, or `--local-url`) and without a pull domain mapping, the clone keeps the source URL in the database and `wp-config.php` and prints `Keeping the source site URL`; that is expected for a same-domain migration. With a target URL or mapping, the database is rewritten and `WP_HOME`, `WP_SITEURL`, and `DOMAIN_CURRENT_SITE` get the same replacements only if the copied `wp-config.php` already defines them; a clone never adds them. A cloned site that redirects to `localhost` was made by an older CLI: update it and fix or remove the `WP_HOME`/`WP_SITEURL` defines.

## Direct Command

Run clone from a working directory outside the web root and point `--local-wp-path` at the target WordPress root: the full database dump is staged in the scratch directory `.wp-ssh/` of the directory you run it from. Use args mode for one-off clones and keep the password in the environment:

```bash
export WP_SSH_CLONE_DB_PASSWORD='…'
wp-ssh-bridge clone --silent \
  --destination deploy@source.example.com \
  --remote-path /home/source/public_html \
  --local-wp-path /var/www/target \
  --db-host db.example.com \
  --db-name target_db \
  --db-user target_user
```

Without `--db-prefix`, the target `$table_prefix` follows the source. Add `--db-prefix wp_` only when the target should use another prefix; the imported tables, the `user_roles` option of every site, and prefix-derived user meta keys are renamed to it. It stops before the import when the source database already holds tables under that prefix.

## Replacing An Existing Installation

After the import, the clone removes the tables of the installation the target's previous `wp-config.php` described, when that config used the same database. `db_reset` (`--db-reset`, `WP_SSH_DB_RESET`) changes this: `installation` is the default, `database` removes every table the dump lacks in the whole target database (only for a database of its own), `none` removes nothing and reports the kept tables. Confirm the target database (and a backup) first. Tables under other prefixes and other WordPress installations sharing the database stay; the clone never resets the whole database. It stops before the import if it would overwrite any other table. Errors before the import (`the target database already contains …`, `wp-config.php resolves the database to …`, `cannot rename …`) mean nothing was imported: the target database holds another site's table, the copied config defines the database elsewhere, or the source already uses the `--db-prefix` prefix.

## Emptying The Target (`--clean-target`)

By default a clone is additive: files are added or updated, but content already on the target (for example a web host's default `index.html` or a starter theme) stays in place. This holds on both transports — the rsync transport does not pass `--delete` for clones, and the tar-over-SSH transport has no `--delete` equivalent. (A normal, non-clone `pull` still mirrors the source with rsync `--delete`.)

Pass `--clean-target` (clone only) to remove pre-existing target content before files are synced, so the cloned site starts from a clean tree on either transport:

- rsync transport: the CLI adds `--delete`, so files not present on the source are removed (configured excludes and operational paths are preserved).
- tar-over-SSH transport: the CLI empties the target WordPress directory before extracting, since rsync `--delete` has no tar equivalent.

Safety:

- It is destructive and cannot be undone. Recommend it only when the user explicitly wants pre-existing target content removed, and confirm the resolved target path first.
- On the tar-over-SSH transport it preserves only operational entries — `.git`, `.ddev`, `.wp-ssh`, `wp-config-ddev.php`, `.wp-ssh.yaml`, and the `wp-ssh-bridge` binary — and refuses to run against a filesystem root or the home directory. On rsync, the configured `--exclude` paths are preserved.
- It is rejected on `pull` and `push`; it exists only on `clone`.
- The import runs without maintenance mode, because the target site is being replaced; the CLI prints `Skipping local maintenance mode: --clean-target replaces the target site`.

A clone into an empty directory or a new database also runs the import without maintenance mode, since `wp core is-installed` finds no site to protect; the CLI prints `Skipping local maintenance mode: no installed WordPress site in the local WordPress root yet`. Neither needs `--skip-maintenance-mode`.

## DDEV And wp-env Projects

In a DDEV or wp-env project root, `wp-ssh-bridge clone` exits with `clone does not support DDEV projects; …` or `clone does not support wp-env projects; …`, because the import would land in the container database instead of the injected target credentials. Run it against a plain target directory.

If the user wants a DDEV development copy, use the normal DDEV pull workflow in `references/ddev.md`.

## Config Mode

Use config mode for repeatable clone defaults:

```yaml
pull_destination: "deploy@source.example.com"
pull_remote_path: "/home/source/public_html"

clone_db_host: "db.example.com"
clone_db_name: "target_db"
clone_db_user: "target_user"
clone_db_prefix: "wp_"

pull_domain_replacements:
  - old: "source.example.com"
    new: "target.example.com"
```

Keep the password out of the file and run it with:

```bash
export WP_SSH_CLONE_DB_PASSWORD='…'
wp-ssh-bridge clone --silent --local-wp-path /var/www/target
```

## Server-To-Server Transfer

The CLI always runs on the machine that receives the site. Running `clone` on the new server is supported and is the preferred migration when that server allows outbound SSH: connect with `ssh -A` so it uses the local agent, install the CLI outside the web root, and run `clone` from a working directory outside the web root with `--local-wp-path <web root>`, because the CLI stages the full database dump in its working directory. When the new server cannot reach the old one, clone into a local directory with a local database and then `push` to the new host with `--push-url`, after creating the target `wp-config.php` with the same table prefix. There is no mode in which the old host sends directly to the new one; do not suggest one. Full guide: https://wp-ssh-bridge.citation.media/docs/migrate-a-site.

## Command Name Errors

The command was previously called `migrate`, with `migrate_db_*` config keys and `WP_SSH_MIGRATE_DB_*` environment variables.

- `unknown command "clone"`: the installed binary predates the rename. Update it (see "Install Or Update wp-ssh-bridge" in `SKILL.md`).
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

The target is a plain WordPress installation, so verify with a direct `wp` against the target path (not `ddev wp`).

If URL replacement was skipped or misconfigured, check the configured `pull_domain_replacements` and rerun only after confirming the intended source and target domains.
