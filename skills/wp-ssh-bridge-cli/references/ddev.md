# DDEV Usage

Use this reference for DDEV projects, generated provider files, `ddev pull`, `ddev push`, or DDEV-specific troubleshooting.

## Workflow

Commands below write the bare `wp-ssh-bridge`. Installed by the script inside a DDEV project, it is `./.ddev/bin/wp-ssh-bridge`; installed with npm, `npx wp-ssh-bridge`, and then every machine needs `npm install` before `ddev pull`, because the provider files call the package's binary by a project-relative path.

1. Install or update `wp-ssh-bridge` if the binary is missing.
2. Run init from the DDEV project root:

```bash
wp-ssh-bridge init
```

   Without a terminal, run `init --silent` with `--destination` and `--remote-path`; see "Set Up A Project Without Prompts" in `SKILL.md`.

3. Pull. The default provider is `wp-ssh`; use the name chosen during init otherwise.

```bash
ddev pull wp-ssh -y                 # native: interactive use
wp-ssh-bridge pull --silent         # direct: scripts and flags
```

   Native `ddev pull` shows DDEV's lifecycle output, lets DDEV import the dump, and runs each provider step as its own process that authenticates separately. It takes no CLI flags; use `--environment=` (see "DDEV Overrides"). The direct pull takes every flag, needs one SSH approval per run, wraps the import in maintenance mode, and removes the tables the dump lacks as `db_reset` sets (default `database`); native `ddev pull` always empties the database. Both rewrite URLs, align the table prefix, remove blocked plugins, and rebuild page builder CSS.

4. Push only after the user confirms the push destination, remote path, and target URL:

```bash
ddev push wp-ssh -y
```

   A direct `wp-ssh-bridge push` in DDEV prints the resolved target and asks `Continue`; `--yes` or `--silent` skips that prompt. It exports the local database afresh on every run; native `ddev push` uploads DDEV's own fresh export.

Do not manually reproduce provider steps.

Both paths use the same transport: rsync when it exists on both sides, otherwise files fall back to tar over SSH (messages say `scp/tar`). The database dump always streams over SSH.

## Values Users Provide

The CLI handles DDEV detection and provider command wiring. Focus on values DDEV cannot infer:

- Pull SSH destination and remote WordPress path.
- Push SSH destination, remote WordPress path, and target URL.
- Local WordPress path only when the DDEV docroot is not enough.
- Whether uploads/media should be cloned during pull.
- Whether URL search-replace should be skipped.
- Domain mappings for multisite or custom domains.
- A project-specific blocked-plugin list when embedded defaults are not enough.

## Generated Files

After init, the DDEV provider setup uses:

```text
.ddev/wp-ssh.yaml
.ddev/providers/<provider>.yaml
.ddev/config.wp-ssh.yaml
```

Commit those files when the project wants versioned provider setup. Keep machine-local absolute paths out of config; use relative paths such as `web`, `public`, or `.wp-ssh-plugins.txt`.

## Domain Mappings

Add mappings with `wp-ssh-bridge domains add --old example.com --new example.ddev.site`; syntax and multisite guidance are in "Domain Mappings" in `references/general-usage.md`. In DDEV mode the command also refreshes the provider files and adds the local hosts to `additional_hostnames`. Run `ddev restart` afterwards so DDEV routes the new hostnames.

## DDEV Overrides

Native `ddev pull <provider>` does not accept `wp-ssh-bridge` flags such as `--destination`. Use DDEV's inline `--environment` flag for one-off values:

```bash
ddev pull wp-ssh \
  --environment=WP_SSH_PULL_DESTINATION=deploy@production.example.com,WP_SSH_PULL_REMOTE_PATH=/home/production/public_html \
  -y
```

```bash
ddev push wp-ssh \
  --environment=WP_SSH_PUSH_DESTINATION=deploy@staging.example.com,WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html,WP_SSH_PUSH_URL=https://staging.example.com \
  -y
```

Do not override with `WP_SSH_PULL_USER` or `WP_SSH_PULL_HOST` when the config uses `pull_destination`; that is rejected with `the destination already carries the user, host, and port`. For per-developer SSH users, commit a `~/.ssh/config` alias as the destination and let each developer own the `Host` block.

## Clone

Clone is not a DDEV workflow. `wp-ssh-bridge clone` exits with `clone does not support DDEV projects; …`. Use the DDEV pull workflow for DDEV onboarding, and see `references/clone.md` for host-to-host site copies.

## Provider Maintenance

Regenerate provider files only after updating the binary or switching install routes. This is not the normal pull workflow.

```bash
wp-ssh-bridge provider install
```

To rename the provider permanently, write the name into the config:

```bash
wp-ssh-bridge init --silent --provider wp-ssh-staging
```

`provider install --provider <name>` only regenerates the files; the next direct pull regenerates them under the configured name again.

`provider generate --kind all` prints the provider YAML without writing files. It does not read the project config: without `--binary` and `--provider` it prints the defaults (`wp-ssh-bridge`, `wp-ssh`).

## Debugging Rules

If anything is unclear or fails:

1. Show the exact error or relevant output the user reported.
2. State the likely failure area in plain language.
3. Ask the user to confirm before running any debugging command.
4. Suggest one focused debugging step at a time.

Do not instruct the AI to fix DDEV pulls by manually running lower-level provider callbacks, hand-written rsync commands, direct WP-CLI repair commands, or manual file edits unless the user explicitly asks for lower-level work.

Useful debugging steps to suggest for confirmation:

```bash
ddev describe -j
wp-ssh-bridge init
ddev pull <provider> -y
```

If a provider callback fails with `.ddev/bin/wp-ssh-bridge: No such file or directory`, reinstall or update the project-local binary, then rerun `./.ddev/bin/wp-ssh-bridge init` or `./.ddev/bin/wp-ssh-bridge provider install`. The same error naming a path under `node_modules` means the npm package is not installed on this machine: run `npm install`. After switching between the script and npm, rerun `provider install` so the provider files point at the binary that is actually present; `--binary <path>` pins a different path explicitly.

Only run a suggested debugging step after the user confirms it.

## Verification

After a successful pull:

```bash
ddev wp option get home
ddev wp plugin list
```

After a push, ask the user to verify the target site in a browser. If they report a wrong target URL or failed push, show the exact error or wrong value and ask whether they want to debug before suggesting the next step.
