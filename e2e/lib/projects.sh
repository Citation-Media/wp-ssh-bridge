# shellcheck shell=bash
# Local DDEV and wp-env projects for `wpsb-e2e ddev|wp-env` and the ddev and
# wp-env scenarios, sourced by wpsb-e2e. They run on this machine and reach
# the simulated source through the SSH wrapper, exactly as a developer's
# project reaches a real host.

PROJECTS="$STATE/projects"

source_config() {
    cat <<'YAML'
pull_destination: wpsb-source
pull_remote_path: /var/www/site
YAML
}

# Prints what a pull left behind, for a project whose database only its own
# tooling reaches. The scenarios assert on the same values.
report_local_site() { # project (ddev or wp-env)
    local prefix
    prefix=$(project_prefix "$1")
    printf 'table prefix:  %s\n' "$prefix"
    printf 'home (DB):     %s\n' "$(project_sql "$1" "SELECT option_value FROM ${prefix}options WHERE option_name = 'home'")"
    printf 'source URLs:   %s rows in options and posts\n' \
        "$(project_sql "$1" "SELECT (SELECT COUNT(*) FROM ${prefix}options WHERE option_value LIKE '%source.wpsb.test%') + (SELECT COUNT(*) FROM ${prefix}posts WHERE post_content LIKE '%source.wpsb.test%')")"
    printf 'tables:        %s\n' "$(project_sql "$1" "SHOW TABLES" | tr '\n' ' ')"
}

project_sql() { # project query
    case "$1" in
        ddev) ddev_sql "$2" ;;
        wp-env) wpenv_sql "$2" ;;
    esac
}

project_prefix() {
    case "$1" in
        ddev) (cd "$DDEV_DIR" && ddev wp config get table_prefix) ;;
        wp-env) wpenv run cli wp config get table_prefix 2> /dev/null ;;
    esac
}

# --- DDEV -----------------------------------------------------------------------

DDEV_DIR="$PROJECTS/ddev"
DDEV_NAME=${WPSB_E2E_DDEV_NAME:-wpsb-e2e}

ddev_sql() { (cd "$DDEV_DIR" && ddev mysql -N -B -e "$1"); }
ddev_url() { (cd "$DDEV_DIR" && ddev describe -j | jq -r '.raw.primary_url'); }

ddev_init() {
    mkdir -p "$DDEV_DIR"
    (cd "$DDEV_DIR" && ddev config --project-name="$DDEV_NAME" --project-type=wordpress \
        --docroot=. --php-version=8.3 --database=mariadb:11.8 > /dev/null)
    source_config > "$DDEV_DIR/.ddev/wp-ssh.yaml"
}

ddev_ensure() {
    command -v ddev > /dev/null || die "ddev is not installed"
    command -v jq > /dev/null || die "jq is required for the DDEV project"
    [ -f "$DDEV_DIR/.ddev/config.yaml" ] || ddev_init
    log "Starting DDEV project $DDEV_NAME"
    (cd "$DDEV_DIR" && with_env ddev start -y > /dev/null)
}

# Empties the project for the next pull: no WordPress files, and a database
# holding only a table of something else, which the pull's default
# db_reset (database) must remove. wp-config-ddev.php stays, as in any started
# DDEV project: the sanitized wp-config.php takes its database settings from it.
ddev_reset() {
    find "$DDEV_DIR" -mindepth 1 -maxdepth 1 ! -name .ddev ! -name wp-config-ddev.php -exec rm -rf {} +
    rm -rf "$DDEV_DIR/.ddev/.downloads"
    ddev_sql "DROP DATABASE IF EXISTS db; CREATE DATABASE db; CREATE TABLE db.zz_local_only (id INT PRIMARY KEY);"
}

cmd_ddev() {
    prepare
    case "${1:-pull}" in
        init) ddev_ensure ;;
        pull)
            ddev_ensure
            log "Direct pull into DDEV ($DDEV_DIR)"
            (cd "$DDEV_DIR" && with_env wp-ssh-bridge pull --silent -y)
            report_local_site ddev
            ;;
        native)
            ddev_ensure
            (cd "$DDEV_DIR" && with_env wp-ssh-bridge provider install > /dev/null)
            log "Native ddev pull wp-ssh ($DDEV_DIR)"
            (cd "$DDEV_DIR" && with_env ddev pull wp-ssh -y)
            report_local_site ddev
            ;;
        report) report_local_site ddev ;;
        rm)
            [ -d "$DDEV_DIR" ] || return 0
            (cd "$DDEV_DIR" && ddev delete -Oy) || true
            rm -rf "$DDEV_DIR"
            ;;
        *) die "usage: ddev [init|pull|native|report|rm]" ;;
    esac
}

# --- wp-env -----------------------------------------------------------------------

WPENV_DIR="$PROJECTS/wp-env"

wpenv() { (cd "$WPENV_DIR" && npx --no-install wp-env "$@"); }
wpenv_sql() { wpenv run cli wp db query "$1" --skip-column-names 2> /dev/null; }
# The host side of wp-env's /var/www/html, where a pull writes the files.
wpenv_root() { printf '%s/WordPress\n' "$(wpenv status --json 2> /dev/null | jq -r '.installPath')"; }

wpenv_init() {
    mkdir -p "$WPENV_DIR"
    cat > "$WPENV_DIR/package.json" <<'JSON'
{
  "name": "wpsb-e2e-wp-env",
  "private": true,
  "devDependencies": {
    "@wordpress/env": "latest"
  }
}
JSON
    cat > "$WPENV_DIR/.wp-env.json" <<JSON
{
  "port": $WPENV_PORT,
  "testsEnvironment": false
}
JSON
    source_config > "$WPENV_DIR/.wp-ssh.yaml"
    log "Installing @wordpress/env"
    (cd "$WPENV_DIR" && npm install --no-audit --no-fund --silent)
}

wpenv_ensure() {
    command -v npm > /dev/null || die "npm is required for wp-env"
    command -v jq > /dev/null || die "jq is required for the wp-env project"
    [ -f "$WPENV_DIR/.wp-env.json" ] || wpenv_init
    log "Starting wp-env on port $WPENV_PORT"
    wpenv start > /dev/null
}

# Back to a fresh wp-env site under the wp_ prefix, plus a table of something
# else. Files a previous pull brought along are removed, so the page builder
# stand-ins only exist when this pull delivers them.
wpenv_reset() {
    local root
    root=$(wpenv_root)
    rm -rf "$root/wp-content/mu-plugins" "$root/wp-content/uploads/2026"
    local slug
    for slug in $BLOCKED_SAMPLE wpsb-e2e-keep; do
        rm -rf "${root:?}/wp-content/plugins/$slug"
    done
    wpenv reset development > /dev/null
    wpenv_sql "CREATE TABLE zz_local_only (id INT PRIMARY KEY)"
}

cmd_wpenv() {
    prepare
    case "${1:-pull}" in
        init) wpenv_ensure ;;
        pull)
            wpenv_ensure
            log "Pull into wp-env ($WPENV_DIR)"
            (cd "$WPENV_DIR" && with_env wp-ssh-bridge pull --silent)
            report_local_site wp-env
            ;;
        report) report_local_site wp-env ;;
        rm)
            [ -d "$WPENV_DIR" ] || return 0
            wpenv destroy --force 2> /dev/null || (cd "$WPENV_DIR" && yes | npx --no-install wp-env destroy) || true
            rm -rf "$WPENV_DIR"
            ;;
        *) die "usage: wp-env [init|pull|report|rm]" ;;
    esac
}
