# DDEV Usage

Use this reference for DDEV projects, generated provider files, `ddev pull`, `ddev push`, or DDEV-specific troubleshooting.

## Required DDEV Workflow

Always use the DDEV provider workflow for DDEV projects. Do not replace it with direct wrapper commands unless the user explicitly asks to bypass DDEV's provider lifecycle.

1. Install or update `wp-ssh-bridge` if the binary is missing.
2. Run init from the DDEV project root:

```bash
./.ddev/bin/wp-ssh-bridge init
```

Use `wp-ssh-bridge init` instead only when the binary is intentionally installed on `PATH`.

3. Use the provider name chosen during init. The default provider is `wp-ssh`.
4. Pull with native DDEV:

```bash
ddev pull wp-ssh -y
```

For a custom provider:

```bash
ddev pull <provider> -y
```

5. Push only after confirming the target host, remote path, and target URL:

```bash
ddev push <provider> -y
```

DDEV owns the lifecycle output for authentication, database import, file import, and hooks. Do not manually reproduce provider steps.

## Values Users Provide

The CLI handles DDEV detection and provider command wiring. Focus on values DDEV cannot infer:

- Pull SSH user, host, optional port, and remote WordPress path.
- Push SSH user, host, optional port, remote WordPress path, and target URL.
- Local WordPress path only when the DDEV docroot is not enough.
- Whether uploads/media should be cloned during pull.
- Whether URL search-replace should be skipped.
- A project-specific blocked-plugin list when embedded defaults are not enough.

## Generated Files

After init, the DDEV provider setup uses:

```text
.ddev/wp-ssh.yaml
.ddev/providers/<provider>.yaml
.ddev/config.wp-ssh.yaml
```

Commit those files when the project wants versioned provider setup. Keep machine-local absolute paths out of config; use relative paths such as `web`, `public`, or `.ddev/plugin-blocklist.txt`.

## DDEV Overrides

Native `ddev pull <provider>` does not accept `wp-ssh-bridge` flags such as `--user`. Use DDEV's inline `--environment` flag for one-off values:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

With a full one-off pull source:

```bash
ddev pull wp-ssh \
  --environment=WP_SSH_PULL_USER=deploy,WP_SSH_PULL_HOST=production.example.com,WP_SSH_PULL_REMOTE_PATH=/home/production/public_html \
  -y
```

For push overrides:

```bash
ddev push wp-ssh \
  --environment=WP_SSH_PUSH_USER=deploy,WP_SSH_PUSH_HOST=staging.example.com,WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html,WP_SSH_PUSH_URL=https://staging.example.com \
  -y
```

Do not suggest `ddev pull wp-ssh --user deploy`; that flag belongs to direct `wp-ssh-bridge pull`, not native DDEV pull.

## Provider Maintenance

Regenerate provider files only after updating the binary or changing provider names. This is not the normal pull workflow.

```bash
./.ddev/bin/wp-ssh-bridge provider install
```

Use a custom provider name:

```bash
./.ddev/bin/wp-ssh-bridge provider install --provider wp-ssh-staging
```

Print generated YAML without writing files:

```bash
./.ddev/bin/wp-ssh-bridge provider generate --kind all
```

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
```

```bash
./.ddev/bin/wp-ssh-bridge init
```

```bash
ddev pull <provider> -y
```

```bash
./.ddev/bin/wp-ssh-bridge provider generate --kind all
```

Only run a suggested debugging step after the user confirms it.

## Verification

After a successful pull:

```bash
ddev wp option get home
ddev wp plugin list
```

After a push, ask the user to verify the target site in a browser. If they report a wrong target URL or failed push, show the exact error or wrong value and ask whether they want to debug before suggesting the next step.
