# shellcheck shell=bash
# Local DDEV and wp-env projects for `wpsb-e2e ddev|wp-env`, sourced by
# wpsb-e2e. They run on this machine and reach the simulated source through
# the SSH wrapper, exactly as a developer's project reaches a real host.

PROJECTS="$STATE/projects"

source_config() {
    cat <<'YAML'
pull_destination: wpsb-source
pull_remote_path: /var/www/site
YAML
}

# Prints the source URL rows still in the local database: the check the
# container scenarios run, for a project whose database only WP-CLI reaches.
report_local_site() { # wp-cli-prefix...
    local prefix
    prefix=$("$@" config get table_prefix)
    printf 'table prefix:  %s\n' "$prefix"
    printf 'home (DB):     %s\n' "$("$@" db query "SELECT option_value FROM ${prefix}options WHERE option_name = 'home'" --skip-column-names)"
    printf 'home (WP):     %s\n' "$("$@" option get home)"
    printf 'source URLs:   %s rows in options and posts\n' \
        "$("$@" db query "SELECT (SELECT COUNT(*) FROM ${prefix}options WHERE option_value LIKE '%source.wpsb.test%') + (SELECT COUNT(*) FROM ${prefix}posts WHERE post_content LIKE '%source.wpsb.test%' OR guid LIKE '%source.wpsb.test%')" --skip-column-names)"
    printf 'tables:        %s\n' "$("$@" db query "SHOW TABLES" --skip-column-names | tr '\n' ' ')"
}

# --- DDEV -----------------------------------------------------------------------

DDEV_DIR="$PROJECTS/ddev"
DDEV_NAME=${WPSB_E2E_DDEV_NAME:-wpsb-e2e}

ddev_wp() { (cd "$DDEV_DIR" && ddev wp "$@"); }

cmd_ddev() {
    command -v ddev > /dev/null || die "ddev is not installed"
    prepare
    case "${1:-pull}" in
        init)
            mkdir -p "$DDEV_DIR"
            (cd "$DDEV_DIR" && ddev config --project-name="$DDEV_NAME" --project-type=wordpress \
                --docroot=. --php-version=8.3 --database=mariadb:11.8 > /dev/null)
            source_config > "$DDEV_DIR/.ddev/wp-ssh.yaml"
            log "Starting DDEV project $DDEV_NAME"
            (cd "$DDEV_DIR" && with_env ddev start -y)
            ;;
        pull)
            [ -f "$DDEV_DIR/.ddev/config.yaml" ] || cmd_ddev init
            log "Direct pull into DDEV ($DDEV_DIR)"
            (cd "$DDEV_DIR" && with_env wp-ssh-bridge pull --silent -y)
            report_local_site ddev_wp
            ;;
        native)
            [ -f "$DDEV_DIR/.ddev/config.yaml" ] || cmd_ddev init
            (cd "$DDEV_DIR" && with_env wp-ssh-bridge provider install > /dev/null)
            log "Native ddev pull wp-ssh ($DDEV_DIR)"
            (cd "$DDEV_DIR" && with_env ddev pull wp-ssh -y)
            report_local_site ddev_wp
            ;;
        report) report_local_site ddev_wp ;;
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
wpenv_wp() { wpenv run cli --env-cwd=/var/www/html wp "$@" 2> /dev/null; }

cmd_wpenv() {
    command -v npm > /dev/null || die "npm is required for wp-env"
    prepare
    case "${1:-pull}" in
        init)
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
            log "Starting wp-env on port $WPENV_PORT"
            wpenv start
            ;;
        pull)
            [ -f "$WPENV_DIR/.wp-env.json" ] || cmd_wpenv init
            log "Pull into wp-env ($WPENV_DIR)"
            (cd "$WPENV_DIR" && with_env wp-ssh-bridge pull --silent)
            report_local_site wpenv_wp
            ;;
        report) report_local_site wpenv_wp ;;
        rm)
            [ -d "$WPENV_DIR" ] || return 0
            wpenv destroy --force 2> /dev/null || (cd "$WPENV_DIR" && yes | npx --no-install wp-env destroy) || true
            rm -rf "$WPENV_DIR"
            ;;
        *) die "usage: wp-env [init|pull|report|rm]" ;;
    esac
}
