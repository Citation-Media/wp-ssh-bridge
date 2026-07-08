# Migration Usage

Use this reference when the user wants to move a WordPress site between environments, asks about `wp-ssh-bridge migrate`, needs target DB credentials written into `wp-config.php`, or asks whether server-to-server transfer is appropriate.

## When To Use Migration Mode

Use `wp-ssh-bridge migrate` when the target should remain a real site-like WordPress install, not a local development copy. Typical cases:

- staging to production-style target
- production to staging without DDEV/dev rewrites
- host A to a local target that will later become host B
- a one-off migration where plugins, uploads, and `wp-config.php` should stay present

Do not use migration mode for ordinary local onboarding into DDEV. Use the normal DDEV or standalone pull workflow for that.

## Behavior

Migration pulls:

- export and import the database like a normal pull
- sync files from the pull source
- copy `wp-config.php`
- write target DB constants into `wp-config.php` before database import
- include uploads/media even when `clone_images` is false
- keep blocked plugins and do not run blocked-plugin cleanup
- skip DDEV/dev `wp-config.php` rewrites, including `WP_ENVIRONMENT_TYPE=development` and `wp-config-ddev.php`
- still run configured URL search-replace unless `--skip-search-replace` is set

Migration mode is a top-level `migrate` command. Do not recommend `pull --migrate` or `push --migrate`.

## Required Values

Source SSH values:

```text
--user or pull_user or WP_SSH_PULL_USER
--host or pull_host or WP_SSH_PULL_HOST
--remote-path or pull_remote_path or WP_SSH_PULL_REMOTE_PATH
```

Target DB values:

```text
--db-host or migrate_db_host or WP_SSH_MIGRATE_DB_HOST
--db-name or migrate_db_name or WP_SSH_MIGRATE_DB_NAME
--db-user or migrate_db_user or WP_SSH_MIGRATE_DB_USER
--db-password or migrate_db_password or WP_SSH_MIGRATE_DB_PASSWORD
--db-prefix or migrate_db_prefix or WP_SSH_MIGRATE_DB_PREFIX (optional)
```

Use configured `pull_domain_replacements` or `--skip-search-replace` according to the migration plan. If the target URL differs from the source URL, configure replacements before running the migration.

## Direct Command

Use args mode for one-off migrations:

```bash
wp-ssh-bridge migrate --silent \
  --user deploy \
  --host source.example.com \
  --remote-path /home/source/public_html \
  --db-host db.example.com \
  --db-name target_db \
  --db-user target_user \
  --db-password target_password
```

Add `--db-prefix wp_` only when the target table prefix should differ from the copied source `wp-config.php`.

## DDEV Projects

`wp-ssh-bridge migrate` does not support DDEV projects and exits with an error when the project root is a DDEV project. Migration is a live host-to-host move into a standalone target, so it must run against a plain (non-DDEV) destination directory. Routing the database import through `ddev wp` would import into the local DDEV container database instead of the injected target credentials.

If the user wants a DDEV development copy, use the normal DDEV pull workflow in `references/ddev.md`. Reserve `migrate` for standalone target directories. Do not recommend native `ddev pull <provider> --migrate`; migration is intentionally exposed only as the standalone `wp-ssh-bridge migrate` command.

## Config Mode

Use config mode for repeatable migration defaults:

```yaml
pull_user: "deploy"
pull_host: "source.example.com"
pull_remote_path: "/home/source/public_html"

migrate_db_host: "db.example.com"
migrate_db_name: "target_db"
migrate_db_user: "target_user"
migrate_db_password: "target_password"
migrate_db_prefix: "wp_"

pull_domain_replacements:
  - old: "source.example.com"
    new: "target.example.com"
```

Run it with:

```bash
wp-ssh-bridge migrate --silent
```

Prefer environment variables for password values when the config file is versioned.

## Server-To-Server Transfer

Do not present server-to-server transfer as the default migration path. The implemented migration path is local-orchestrated: source to host to target working tree.

Server-to-server can be faster, but it changes the trust and support model:

- the source server needs SSH reachability to the target server
- the source server needs target credentials or agent-forwarded access
- target host keys must be trusted from the source server
- many shared hosts block outbound SSH or have limited transfer tooling
- failures are harder to diagnose because both remote hosts are active participants

Recommend the local-orchestrated `migrate` path unless the user explicitly accepts those requirements. Treat server-to-server as a future explicit workflow, not a mode that silently changes transport.

## Verification

After migration, verify the target values that should have changed:

```bash
wp config get DB_HOST --path=/path/to/wordpress
wp config get DB_NAME --path=/path/to/wordpress
wp option get home --path=/path/to/wordpress
wp plugin list --path=/path/to/wordpress
```

The target is a standalone WordPress install, so verify with a direct `wp` against the target path (not `ddev wp`).

If URL replacement was skipped or misconfigured, check the configured `pull_domain_replacements` and rerun only after confirming the intended source and target domains.
