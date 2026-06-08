#!/usr/bin/env bash
#ddev-generated
set -eu -o pipefail

PROJECT_ROOT="${DDEV_APPROOT:-/var/www/html}"
DOWNLOAD_DIR="${PROJECT_ROOT}/.ddev/.downloads"
PLUGIN_LIST_DEFAULT="${PROJECT_ROOT}/.ddev/commands/wp-plugins-removal.txt"
PLUGIN_REMOVAL_COMMAND="${WP_SSH_PULL_PLUGIN_REMOVAL_COMMAND:-${PROJECT_ROOT}/.ddev/commands/web/wp-plugins-removal}"
SSH_OPTIONS=(
  -o BatchMode=yes
  -o PasswordAuthentication=no
  -o KbdInteractiveAuthentication=no
  -o PubkeyAuthentication=yes
  -o PreferredAuthentications=publickey
)

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

is_truthy() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|on|ON) return 0 ;;
    *) return 1 ;;
  esac
}

require_safe_remote_path() {
  local name="$1"
  local value="$2"

  case "$value" in
    *"'"*|*$'\n'*|*$'\r'*)
      die "${name} must not contain single quotes or newlines."
      ;;
  esac
}

require_safe_ssh_value() {
  case "${WP_SSH_PULL_USER}" in
    *[!A-Za-z0-9._-]*|"")
      die "WP_SSH_PULL_USER must contain only letters, numbers, dots, underscores, or hyphens."
      ;;
  esac

  case "${WP_SSH_PULL_HOST}" in
    *[!A-Za-z0-9._-]*|"")
      die "WP_SSH_PULL_HOST must contain only letters, numbers, dots, underscores, or hyphens."
      ;;
  esac

  case "${WP_SSH_PULL_PORT:-}" in
    ""|*[!0-9]*)
      if [ -n "${WP_SSH_PULL_PORT:-}" ]; then
        die "WP_SSH_PULL_PORT must be numeric."
      fi
      ;;
  esac
}

require_settings() {
  : "${WP_SSH_PULL_USER:?Set WP_SSH_PULL_USER with --environment=WP_SSH_PULL_USER=<user>.}"
  : "${WP_SSH_PULL_HOST:?Set WP_SSH_PULL_HOST in a project config file.}"
  : "${WP_SSH_PULL_REMOTE_PATH:?Set WP_SSH_PULL_REMOTE_PATH in a project config file.}"

  require_safe_ssh_value
  require_safe_remote_path "WP_SSH_PULL_REMOTE_PATH" "${WP_SSH_PULL_REMOTE_PATH}"
  require_safe_remote_path "WP_SSH_PULL_REMOTE_TMP_DIR" "${WP_SSH_PULL_REMOTE_TMP_DIR:-/tmp}"
}

ssh_target() {
  printf '%s@%s' "${WP_SSH_PULL_USER}" "${WP_SSH_PULL_HOST}"
}

ssh_command_string() {
  local command="ssh"
  local option

  if [ -n "${WP_SSH_PULL_PORT:-}" ]; then
    command="${command} -p ${WP_SSH_PULL_PORT}"
  fi

  for option in "${SSH_OPTIONS[@]}"; do
    command="${command} ${option}"
  done

  printf '%s' "${command}"
}

run_ssh() {
  local remote_command="$1"
  local ssh_args=()

  if [ -n "${WP_SSH_PULL_PORT:-}" ]; then
    ssh_args+=("-p" "${WP_SSH_PULL_PORT}")
  fi

  ssh_args+=("${SSH_OPTIONS[@]}")

  ssh "${ssh_args[@]}" "$(ssh_target)" "${remote_command}"
}

remote_wp_path() {
  printf '%s' "${WP_SSH_PULL_REMOTE_PATH%/}"
}

local_wp_root() {
  local local_path

  local_path="${WP_SSH_PULL_LOCAL_WP_PATH:-${DDEV_DOCROOT:-}}"

  if [ -z "${local_path}" ] || [ "${local_path}" = "." ]; then
    printf '%s' "${PROJECT_ROOT}"
    return
  fi

  case "${local_path}" in
    /*) printf '%s' "${local_path%/}" ;;
    *) printf '%s/%s' "${PROJECT_ROOT}" "${local_path%/}" ;;
  esac
}

plugin_list_file() {
  printf '%s' "${WP_SSH_PULL_PLUGIN_REMOVE_FILE:-${PLUGIN_LIST_DEFAULT}}"
}

read_plugin_list() {
  local list_file
  local plugin

  list_file="$(plugin_list_file)"
  [ -f "${list_file}" ] || return 0

  while IFS= read -r plugin || [ -n "${plugin}" ]; do
    plugin="${plugin%%#*}"
    plugin="${plugin#${plugin%%[![:space:]]*}}"
    plugin="${plugin%${plugin##*[![:space:]]}}"

    [ -n "${plugin}" ] || continue

    case "${plugin}" in
      /*|*..*|*" "*|*$'\t'*|*[^A-Za-z0-9._/-]*)
        die "Invalid plugin entry '${plugin}' in ${list_file}. Use a plugin slug or basename."
        ;;
    esac

    printf '%s\n' "${plugin}"
  done < "${list_file}"
}

add_default_excludes() {
  RSYNC_EXCLUDES+=(
    "--exclude=.git/"
    "--exclude=.ddev/"
    "--exclude=wp-config-ddev.php"
    "--exclude=wp-content/cache/"
    "--exclude=wp-content/debug.log"
    "--exclude=wp-content/upgrade/"
    "--exclude=wp-content/updraft/"
    "--exclude=wp-content/ai1wm-backups/"
    "--exclude=wp-content/languages/wpml/queue/"
  )
}

add_media_excludes() {
  if is_truthy "${WP_SSH_PULL_CLONE_IMAGES:-false}"; then
    return
  fi

  RSYNC_EXCLUDES+=("--exclude=wp-content/uploads/")
}

add_plugin_excludes() {
  local plugin
  local plugin_dir

  while IFS= read -r plugin; do
    plugin_dir="${plugin%%/*}"
    plugin_dir="${plugin_dir%.php}"
    [ -n "${plugin_dir}" ] || continue
    RSYNC_EXCLUDES+=("--exclude=wp-content/plugins/${plugin_dir}/")
    RSYNC_EXCLUDES+=("--exclude=wp-content/plugins/${plugin_dir}.php")
  done < <(read_plugin_list)
}

build_rsync_excludes() {
  RSYNC_EXCLUDES=()
  add_default_excludes
  add_media_excludes
  add_plugin_excludes
}

auth() {
  require_settings

  ssh-add -l >/dev/null || die "Run 'ddev auth ssh' before pulling from upstream."
  run_ssh "printf 'SSH key authentication works for %s\\n' '$(ssh_target)'"
}

db_pull() {
  local remote_tmp_dir
  local remote_dump
  local remote_dump_gz
  local remote_wp
  local project_name

  require_settings
  mkdir -p "${DOWNLOAD_DIR}"

  remote_tmp_dir="${WP_SSH_PULL_REMOTE_TMP_DIR:-/tmp}"
  remote_tmp_dir="${remote_tmp_dir%/}"
  remote_wp="$(remote_wp_path)"
  project_name="${DDEV_PROJECT:-wordpress}"
  remote_dump="${remote_tmp_dir}/ddev-${project_name}-$(date +%Y%m%d%H%M%S)-${RANDOM}.sql"
  remote_dump_gz="${remote_dump}.gz"

  printf 'Creating remote database export with WP-CLI...\n'
  run_ssh "set -eu; cleanup() { rm -f '${remote_dump}' '${remote_dump_gz}'; }; trap cleanup INT TERM HUP EXIT; cd '${remote_wp}'; command -v wp >/dev/null; rm -f '${remote_dump}' '${remote_dump_gz}'; wp --allow-root db export '${remote_dump}'; gzip -f '${remote_dump}'; trap - EXIT"

  printf 'Downloading database export...\n'
  rsync -azs -e "$(ssh_command_string)" "$(ssh_target):${remote_dump_gz}" "${DOWNLOAD_DIR}/db.sql.gz"

  printf 'Deleting remote database export...\n'
  run_ssh "rm -f '${remote_dump}' '${remote_dump_gz}'"
}

files_pull() {
  local destination
  local rsync_status

  require_settings
  build_rsync_excludes

  destination="$(local_wp_root)"
  mkdir -p "${destination}"
  printf 'Syncing upstream WordPress files into the local DDEV environment...\n'

  set +e
  rsync -azs --delete --safe-links "${RSYNC_EXCLUDES[@]}" -e "$(ssh_command_string)" "$(ssh_target):$(remote_wp_path)/" "${destination}/"
  rsync_status=$?
  set -e

  if [ "${rsync_status}" -eq 24 ]; then
    printf 'Continuing after rsync warning: remote files vanished during transfer.\n' >&2
    return 0
  fi

  return "${rsync_status}"
}

files_import() {
  printf 'Files are already synced into the local WordPress environment.\n'
}

sanitize_wp_config() {
  local wp_root
  local wp_config

  wp_root="$(local_wp_root)"
  wp_config="${wp_root}/wp-config.php"

  [ -f "${wp_config}" ] || return 0

  printf 'Sanitizing wp-config.php for DDEV-managed database settings...\n'
  WP_CONFIG_PATH="${wp_config}" php <<'PHP'
<?php
$path = getenv('WP_CONFIG_PATH');
$contents = file_get_contents($path);
if ($contents === false) {
    fwrite(STDERR, "Unable to read {$path}\n");
    exit(1);
}

$contents = preg_replace(
    '/^[ \t]*define\(\s*[\'\"]DB_(?:NAME|USER|PASSWORD|HOST|CHARSET|COLLATE)[\'\"]\s*,\s*.*?\);\h*(?:\r?\n)?/m',
    '',
    $contents
);

$contents = preg_replace(
    '/define\(\s*[\'\"]COOKIE_DOMAIN[\'\"]\s*,\s*\$_SERVER\s*\[\s*[\'\"]HTTP_HOST[\'\"]\s*\]\s*\);/',
    "define('COOKIE_DOMAIN', \$_SERVER['HTTP_HOST'] ?? '');",
    $contents
);

if (preg_match('/^[ \t]*define\(\s*[\'\"]WP_ENVIRONMENT_TYPE[\'\"]\s*,\s*.*?\);/m', $contents)) {
    $contents = preg_replace(
        '/^[ \t]*define\(\s*[\'\"]WP_ENVIRONMENT_TYPE[\'\"]\s*,\s*.*?\);/m',
        "define('WP_ENVIRONMENT_TYPE', 'development');",
        $contents
    );
} else {
    $environment_snippet = "\ndefine('WP_ENVIRONMENT_TYPE', 'development');\n";
    $pattern = '/(\n\s*\/\* That(?:\\\'|’)s all, stop editing! Happy publishing\. \*\/)/';
    if (preg_match($pattern, $contents)) {
        $contents = preg_replace($pattern, $environment_snippet . '$1', $contents, 1);
    } else {
        $contents = rtrim($contents) . $environment_snippet;
    }
}

if (!str_contains($contents, 'wp-config-ddev.php')) {
    $snippet = <<<'SNIPPET'

// Include for DDEV-managed settings in wp-config-ddev.php.
$ddev_settings = __DIR__ . '/wp-config-ddev.php';
if (is_readable($ddev_settings) && !defined('DB_USER')) {
    require_once($ddev_settings);
}
SNIPPET;

    $pattern = '/(\n\s*(?:require_once|require)\s+ABSPATH\s*\.\s*[\'\"]wp-settings\.php[\'\"]\s*;)/';
    if (preg_match($pattern, $contents)) {
        $contents = preg_replace($pattern, $snippet . '$1', $contents, 1);
    } else {
        $contents = rtrim($contents) . $snippet . "\n";
    }
}

if (file_put_contents($path, $contents) === false) {
    fwrite(STDERR, "Unable to write {$path}\n");
    exit(1);
}
PHP
}

wp_cli() {
  local wp_root="$1"
  shift

  wp --path="${wp_root}" --allow-root --skip-plugins --skip-themes "$@"
}

trim_trailing_slash() {
  local value="$1"

  while [ "${value}" != "/" ] && [ "${value%/}" != "${value}" ]; do
    value="${value%/}"
  done

  printf '%s' "${value}"
}

local_site_url() {
  local url

  url="${WP_SSH_PULL_LOCAL_URL:-${DDEV_PRIMARY_URL_WITHOUT_PORT:-${DDEV_PRIMARY_URL:-}}}"
  [ -n "${url}" ] || return 1

  trim_trailing_slash "${url}"
}

current_wp_url() {
  local wp_root="$1"
  local url

  url="$(wp_cli "${wp_root}" option get home 2>/dev/null || true)"
  if [ -z "${url}" ]; then
    url="$(wp_cli "${wp_root}" option get siteurl 2>/dev/null || true)"
  fi

  [ -n "${url}" ] || return 1
  trim_trailing_slash "${url}"
}

url_base() {
  local url="$1"

  URL_TO_PARSE="${url}" php <<'PHP'
<?php
$url = getenv('URL_TO_PARSE');
$parts = parse_url($url);
if (!is_array($parts) || empty($parts['scheme']) || empty($parts['host'])) {
    exit(1);
}

$base = $parts['scheme'] . '://' . $parts['host'];
if (isset($parts['port'])) {
    $base .= ':' . $parts['port'];
}

echo $base;
PHP
}

url_host() {
  local url="$1"

  URL_TO_PARSE="${url}" php <<'PHP'
<?php
$url = getenv('URL_TO_PARSE');
$parts = parse_url($url);
if (!is_array($parts) || empty($parts['host'])) {
    exit(1);
}

echo $parts['host'];
PHP
}

sql_quote() {
  local value="$1"

  SQL_VALUE="${value}" php <<'PHP'
<?php
$value = getenv('SQL_VALUE');
echo "'", str_replace(["\\", "'"], ["\\\\", "\\'"], $value), "'";
PHP
}

wp_table_prefix() {
  local wp_root="$1"
  local prefix

  prefix="$(wp_cli "${wp_root}" db prefix 2>/dev/null || true)"
  case "${prefix}" in
    *[!A-Za-z0-9_]*|"") return 1 ;;
  esac

  printf '%s' "${prefix}"
}

wp_table_exists() {
  local wp_root="$1"
  local table="$2"
  local table_sql
  local result

  table_sql="$(sql_quote "${table}")"
  result="$(wp_cli "${wp_root}" db query "SHOW TABLES LIKE ${table_sql}" --skip-column-names 2>/dev/null || true)"

  [ "${result}" = "${table}" ]
}

run_search_replace() {
  local wp_root="$1"
  local old_value="$2"
  local new_value="$3"

  [ -n "${old_value}" ] || return 0
  [ -n "${new_value}" ] || return 0
  [ "${old_value}" != "${new_value}" ] || return 0

  printf 'Replacing WordPress URLs: %s -> %s\n' "${old_value}" "${new_value}"
  wp_cli "${wp_root}" search-replace "${old_value}" "${new_value}" --all-tables-with-prefix --precise --skip-columns=guid --report-changed-only
}

replace_multisite_domain_table() {
  local wp_root="$1"
  local table="$2"
  local old_domain="$3"
  local new_domain="$4"
  local old_domain_sql
  local new_domain_sql

  wp_table_exists "${wp_root}" "${table}" || return 0

  old_domain_sql="$(sql_quote "${old_domain}")"
  new_domain_sql="$(sql_quote "${new_domain}")"

  printf 'Replacing WordPress multisite domains in %s: %s -> %s\n' "${table}" "${old_domain}" "${new_domain}"
  wp_cli "${wp_root}" db query "UPDATE \`${table}\` SET domain = ${new_domain_sql} WHERE domain = ${old_domain_sql}"
}

replace_multisite_domains() {
  local wp_root="$1"
  local old_base="$2"
  local new_base="$3"
  local old_domain
  local new_domain
  local prefix

  old_domain="$(url_host "${old_base}" || true)"
  new_domain="$(url_host "${new_base}" || true)"

  [ -n "${old_domain}" ] || return 0
  [ -n "${new_domain}" ] || return 0
  [ "${old_domain}" != "${new_domain}" ] || return 0

  prefix="$(wp_table_prefix "${wp_root}" || true)"
  [ -n "${prefix}" ] || return 0

  replace_multisite_domain_table "${wp_root}" "${prefix}site" "${old_domain}" "${new_domain}"
  replace_multisite_domain_table "${wp_root}" "${prefix}blogs" "${old_domain}" "${new_domain}"
}

replace_site_urls() {
  local wp_root
  local old_url
  local new_url
  local old_base
  local new_base

  if is_truthy "${WP_SSH_PULL_SKIP_SEARCH_REPLACE:-false}"; then
    return 0
  fi

  command -v wp >/dev/null || return 0

  wp_root="$(local_wp_root)"
  [ -f "${wp_root}/wp-config.php" ] || return 0

  old_url="$(current_wp_url "${wp_root}" || true)"
  new_url="$(local_site_url || true)"

  if [ -z "${old_url}" ] || [ -z "${new_url}" ]; then
    printf 'Skipping WordPress URL replacement because the old or new URL could not be detected.\n' >&2
    return 0
  fi

  old_base="$(url_base "${old_url}" || true)"
  new_base="$(url_base "${new_url}" || true)"

  if [ -z "${old_base}" ] || [ -z "${new_base}" ]; then
    printf 'Skipping WordPress URL replacement because the old or new URL is invalid.\n' >&2
    return 0
  fi

  run_search_replace "${wp_root}" "${old_url}" "${new_url}"
  run_search_replace "${wp_root}" "http://${old_base#*://}" "${new_base}"
  run_search_replace "${wp_root}" "https://${old_base#*://}" "${new_base}"
  replace_multisite_domains "${wp_root}" "${old_base}" "${new_base}"
}

post_pull() {
  sanitize_wp_config
  replace_site_urls

  if [ -x "${PLUGIN_REMOVAL_COMMAND}" ]; then
    bash "${PLUGIN_REMOVAL_COMMAND}"
  fi
}

case "${1:-}" in
  auth) auth ;;
  db-pull) db_pull ;;
  files-pull) files_pull ;;
  files-import) files_import ;;
  post-pull) post_pull ;;
  sanitize-config) sanitize_wp_config ;;
  *) die "Usage: $0 {auth|db-pull|files-pull|files-import|post-pull|sanitize-config}" ;;
esac
