# Configuration Modes

Use this reference when the user asks how to pass values the CLI cannot infer: SSH usernames, hosts, paths, URLs, media behavior, plugin block lists, or operation flags.

`wp-ssh-bridge` can be configured three ways:

1. Config file mode for repeatable project defaults.
2. Env mode for temporary shell or CI overrides.
3. Args mode for one-shot command overrides.

When the same manual value is provided more than once, the later source wins:

```text
YAML config -> environment variables -> CLI flags
```

In normal wording, tell users: config is the base, env overrides config, flags override both. Do not describe automatic defaults unless the user asks why a value appeared without being configured.

## Config File Mode

Use config mode for project defaults that should be easy to repeat.

DDEV config path:

```text
.ddev/wp-ssh.yaml
```

Standalone config path:

```text
.wp-ssh.yaml
```

Example:

```yaml
pull_user: "deploy"
pull_host: "production.example.com"
pull_port: "22"
pull_remote_path: "/home/production/public_html"
pull_remote_tmp_dir: "/tmp"

push_user: "deploy"
push_host: "staging.example.com"
push_port: "22"
push_remote_path: "/home/staging/public_html"
push_remote_tmp_dir: "/tmp"
push_url: "https://staging.example.com"

local_wp_path: "web"
local_url: "https://project.ddev.site"
clone_images: false
skip_search_replace: false
```

Notes:

- Remote paths and remote temp dirs must be absolute paths.
- Local project paths should be relative, for example `web` or `.ddev/plugin-blocklist.txt`.
- `skip-db`, `skip-files`, and `skip-import` are operation flags, not YAML config keys.

Use an alternate config file with either:

```bash
wp-ssh-bridge pull --silent --config-file .ddev/wp-ssh.production.yaml
```

or:

```bash
WP_SSH_CONFIG_FILE=.ddev/wp-ssh.production.yaml wp-ssh-bridge pull --silent
```

For native DDEV provider commands, use DDEV's inline `--environment` flag for one-off overrides:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

For multiple pull values:

```bash
ddev pull wp-ssh \
  --environment=WP_SSH_PULL_USER=deploy,WP_SSH_PULL_HOST=production.example.com,WP_SSH_PULL_REMOTE_PATH=/home/production/public_html \
  -y
```

Do not suggest `ddev pull wp-ssh --user deploy`; that flag belongs to `wp-ssh-bridge pull`, not native DDEV `pull`.

## Env Mode

Use env mode when values differ by shell, CI job, or temporary target.

Pull source:

```bash
export WP_SSH_PULL_USER=deploy
export WP_SSH_PULL_HOST=production.example.com
export WP_SSH_PULL_PORT=22
export WP_SSH_PULL_REMOTE_PATH=/home/production/public_html
export WP_SSH_PULL_REMOTE_TMP_DIR=/tmp
```

Push target:

```bash
export WP_SSH_PUSH_USER=deploy
export WP_SSH_PUSH_HOST=staging.example.com
export WP_SSH_PUSH_PORT=22
export WP_SSH_PUSH_REMOTE_PATH=/home/staging/public_html
export WP_SSH_PUSH_REMOTE_TMP_DIR=/tmp
export WP_SSH_PUSH_URL=https://staging.example.com
```

Local behavior:

```bash
export WP_SSH_LOCAL_WP_PATH=web
export WP_SSH_PULL_LOCAL_URL=https://project.ddev.site
export WP_SSH_PULL_CLONE_IMAGES=false
export WP_SSH_PULL_SKIP_SEARCH_REPLACE=false
export WP_SSH_PUSH_SKIP_SEARCH_REPLACE=false
export WP_SSH_PULL_PLUGIN_REMOVE_FILE=.ddev/plugin-blocklist.txt
```

Truthy boolean values include `1`, `true`, `yes`, and `on`.

Do not put private key contents in environment variables. SSH keys should stay in normal SSH files or key agents.

Native DDEV provider commands also accept these keys inline:

```bash
ddev pull wp-ssh --environment=WP_SSH_PULL_USER=deploy -y
```

## Args Mode

Use args mode for one-shot commands or local overrides.

Pull example:

```bash
wp-ssh-bridge pull --silent \
  --user deploy \
  --host production.example.com \
  --remote-path /home/production/public_html
```

Push example with concise aliases:

```bash
wp-ssh-bridge push --silent \
  --user deploy \
  --host staging.example.com \
  --remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Push also supports explicit push flags:

```bash
wp-ssh-bridge push --silent \
  --push-user deploy \
  --push-host staging.example.com \
  --push-remote-path /home/staging/public_html \
  --push-url https://staging.example.com
```

Common operation flags:

```text
--skip-db              pull/push files only
--skip-files           pull/push database only
--skip-import          pull only; download DB without importing
--clone-images         include wp-content/uploads during pull
--skip-search-replace  skip URL replacement
--yes, -y              confirm direct DDEV wrapper operation
--silent               do not prompt; also skips direct wrapper confirmation
```
