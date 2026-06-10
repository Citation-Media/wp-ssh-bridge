# Troubleshooting

Use this reference when the user reports errors, noisy output, or uncertainty about passing values.

## Passing An SSH Username

Direct wrapper args mode:

```bash
wp-ssh-bridge pull --silent --user deploy --host production.example.com --remote-path /home/production/public_html
```

Native DDEV pull inline mode:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

Native DDEV pull does not accept:

```bash
ddev pull wp-ssh --user deploy
```

Push with concise aliases:

```bash
wp-ssh-bridge push --silent --user deploy --host staging.example.com --remote-path /home/staging/public_html
```

Env mode:

```bash
export WP_SSH_PULL_USER=deploy
export WP_SSH_PUSH_USER=deploy
```

Config mode:

```yaml
pull_user: "deploy"
push_user: "deploy"
```

## Missing Required Values

Errors such as `missing SSH user`, `missing SSH host`, or `missing remote WordPress path` mean the effective config does not contain required target fields.

Fix with either config, env, or flags. For pull, the minimum is:

```bash
wp-ssh-bridge pull --silent --user deploy --host production.example.com --remote-path /home/production/public_html
```

For push, the minimum is:

```bash
wp-ssh-bridge push --silent --push-user deploy --push-host staging.example.com --push-remote-path /home/staging/public_html
```

## DDEV Output Is Still Noisy

If the user runs:

```bash
ddev pull wp-ssh
```

DDEV prints native lifecycle headings. Recommend the direct wrapper for cleaner output:

```bash
wp-ssh-bridge pull --silent
```

or:

```bash
./.ddev/bin/wp-ssh-bridge pull --silent
```

## Remote PHP Warnings

Remote PHP startup warnings, for example missing `imagick.so`, are produced by the remote PHP runtime. Current CLI versions suppress successful preflight output, but warnings from real data-changing operations may still surface because they can be actionable.

Fix the remote PHP extension configuration if the warning should disappear completely.

## WP-CLI Missing

The CLI first tries remote `wp`. If it is unavailable, it downloads a managed `wp-ssh-bridge-wp-cli.phar` into the configured remote temp dir and tests it.

Check:

```text
pull_remote_tmp_dir
push_remote_tmp_dir
WP_SSH_PULL_REMOTE_TMP_DIR
WP_SSH_PUSH_REMOTE_TMP_DIR
```

Remote temp dirs must be absolute paths and writable by the SSH user.

## Config File Not Used

Use an explicit config path:

```bash
wp-ssh-bridge pull --silent --config-file .ddev/wp-ssh.production.yaml
```

or:

```bash
WP_SSH_CONFIG_FILE=.ddev/wp-ssh.production.yaml wp-ssh-bridge pull --silent
```

Relative config paths resolve from the detected project root.

## Plugin Cleanup

Blocked plugins are removed only when they are reported by `wp plugin list`. The CLI uses WP-CLI to deactivate and delete matching plugins.

Add a project-specific plugin block list only when needed:

```yaml
plugin_remove_file: ".ddev/plugin-blocklist.txt"
```

The file accepts one plugin slug or plugin basename per line:

```text
updraftplus
wp-mail-smtp
some-plugin/some-plugin.php
```

## Remote Path Problems

Remote WordPress paths must be absolute and point to the WordPress root:

```text
/home/site/public_html
/var/www/html
/web
```

Invalid:

```text
public_html
~/public_html
```

Use an absolute path instead of shell expansion.
