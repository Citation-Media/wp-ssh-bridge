# shellcheck shell=bash
# Scenarios for `wpsb-e2e test`, sourced by wpsb-e2e. Each one seeds the
# machines it needs, runs the CLI inside the containers, and asserts on the
# databases and files rather than on the CLI's own success lines.
#
# The CLI output of the last run is kept in e2e/.state/last-run.log.
#
# Scenarios come in suites: `containers` needs only Docker, `ddev` and
# `wp-env` also start a local project of that runtime on this machine.

source "$E2E_DIR/lib/projects.sh"

SUITE_CONTAINERS="pull pull-db-reset-none pull-legacy page-builders push-new-host push-prefix-mismatch push-after-skip-import clone-existing-site clone-db-prefix clone-foreign-table"
SUITE_DDEV="ddev-pull ddev-native-pull"
SUITE_WPENV="wp-env-pull"
SCENARIOS="$SUITE_CONTAINERS $SUITE_DDEV $SUITE_WPENV"

describe_scenario() {
    case "$1" in
        pull) echo "standalone pull: prefix alignment, installation-only export, table cleanup, URLs, blocked plugin, uploads" ;;
        pull-db-reset-none) echo "db_reset none keeps the old local installation's tables" ;;
        pull-legacy) echo "pull through a host without rsync, with exec() disabled and legacy MariaDB names" ;;
        page-builders) echo "cache rebuild runs, a failing builder only warns, --skip-cache-rebuild skips it" ;;
        push-new-host) echo "push into a new host prepared per the migration guide" ;;
        push-prefix-mismatch) echo "push stops before anything changes when the target uses another prefix" ;;
        push-after-skip-import) echo "push exports the local database even after pull --skip-import" ;;
        clone-existing-site) echo "clone on the target replaces the previous installation, keeps neighbours" ;;
        clone-db-prefix) echo "clone --db-prefix renames tables, user_roles, and user meta keys" ;;
        clone-foreign-table) echo "clone stops before the import when it would overwrite a foreign table" ;;
        ddev-pull) echo "direct pull into a DDEV project" ;;
        ddev-native-pull) echo "ddev pull wp-ssh through the generated provider" ;;
        wp-env-pull) echo "pull into a wp-env project" ;;
    esac
}

# --- helpers --------------------------------------------------------------------

LAST_LOG="$STATE/last-run.log"
FAILED_CHECKS=0

# Runs the CLI on a machine from a working directory; output goes to the log
# and into $OUT. Returns the CLI's exit status.
cli() { # machine dir args...
    local machine=$1 dir=$2 status=0
    shift 2
    OUT=$(in_machine "$machine" env -C "$dir" wp-ssh-bridge "$@" 2>&1) || status=$?
    {
        printf '\n$ [%s:%s] wp-ssh-bridge %s  (exit %s)\n' "$machine" "$dir" "$*" "$status"
        printf '%s\n' "$OUT"
    } >> "$LAST_LOG"
    return "$status"
}

# Runs a command on this machine with the E2E ssh and CLI on PATH, like cli().
local_run() { # dir cmd...
    local dir=$1 status=0
    shift
    OUT=$(cd "$dir" && with_env "$@" 2>&1) || status=$?
    {
        printf '\n$ [local:%s] %s  (exit %s)\n' "$dir" "$*" "$status"
        printf '%s\n' "$OUT"
    } >> "$LAST_LOG"
    return "$status"
}

# Queries a database directly, bypassing WordPress and its constants. The
# ddev and wp-env databases are reached through their own tooling.
sql() { # database query
    case "$1" in
        ddev | wp-env) project_sql "$1" "$2" ;;
        *) compose exec -T db mariadb -uroot -proot -N -B "$1" -e "$2" ;;
    esac
}

tables() { sql "$1" "SHOW TABLES" | tr '\n' ' '; }

pass() { printf '    \033[32m✓\033[0m %s\n' "$1"; }
fail() {
    printf '    \033[31m✗\033[0m %s\n' "$1"
    [ -z "${2:-}" ] || printf '      %s\n' "$2"
    FAILED_CHECKS=$((FAILED_CHECKS + 1))
}

expect_ok() { # description status
    if [ "$2" -eq 0 ]; then pass "$1"; else fail "$1" "exit status $2; see $LAST_LOG"; fi
}
expect_fail() {
    if [ "$2" -ne 0 ]; then pass "$1"; else fail "$1" "exited 0; see $LAST_LOG"; fi
}
expect_eq() { # description actual expected
    if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "expected '$3', got '$2'"; fi
}
expect_output() { # text
    case "$OUT" in
        *"$1"*) pass "output says: $1" ;;
        *) fail "output says: $1" "not found; see $LAST_LOG" ;;
    esac
}
expect_no_output() {
    case "$OUT" in
        *"$1"*) fail "output does not say: $1" "found; see $LAST_LOG" ;;
        *) pass "output does not say: $1" ;;
    esac
}
expect_table() { # database table
    case " $(tables "$1") " in
        *" $2 "*) pass "$1 has table $2" ;;
        *) fail "$1 has table $2" "tables: $(tables "$1")" ;;
    esac
}
expect_no_table() {
    case " $(tables "$1") " in
        *" $2 "*) fail "$1 has no table $2" "it is still there" ;;
        *) pass "$1 has no table $2" ;;
    esac
}
# A machine name, or `local` for a path on this machine.
path_exists() { # machine path
    if [ "$1" = local ]; then test -e "$2"; else in_machine "$1" test -e "$2"; fi
}
expect_path() { # machine path
    if path_exists "$1" "$2"; then pass "$1 has $2"; else fail "$1 has $2" "missing"; fi
}
expect_no_path() {
    if path_exists "$1" "$2"; then fail "$1 has no $2" "it exists"; else pass "$1 has no $2"; fi
}
expect_set() { # description value
    if [ -n "$2" ]; then pass "$1"; else fail "$1" "empty"; fi
}
option() { # database prefix name
    sql "$1" "SELECT option_value FROM ${2}options WHERE option_name = '$3'"
}
# Counts the options and post contents that still carry a URL. GUIDs are left
# out: the CLI skips them on purpose, as WordPress asks.
url_rows() { # database prefix url
    sql "$1" "SELECT (SELECT COUNT(*) FROM ${2}options WHERE option_value LIKE '%$3%') + (SELECT COUNT(*) FROM ${2}posts WHERE post_content LIKE '%$3%')"
}

pull_workstation() { cli workstation /work pull --silent "$@"; }

# --- scenarios ------------------------------------------------------------------

scenario_pull() {
    seed source workstation
    # A blocked plugin already in the local copy, as after an older pull.
    in_machine workstation cp -a /opt/wpsb-e2e/fixtures/plugins/cloudflare /work/public/wp-content/plugins/

    local status=0
    pull_workstation --clone-images || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "Local table prefix set to abc_"

    expect_table workstation abc_options
    expect_table workstation abc_wpsb_log
    expect_no_table workstation abc_shop_options
    expect_no_table workstation other_app_sessions
    expect_no_table workstation wp_options
    expect_table workstation unrelated_local

    expect_eq "home in the database is the local URL" "$(option workstation abc_ home)" https://workstation.wpsb.test
    expect_eq "no source URL left in options and posts" "$(url_rows workstation abc_ source.wpsb.test)" 0

    expect_output "Removed 1 local-only blocked plugin"
    expect_no_path workstation /work/public/wp-content/plugins/cloudflare
    expect_path workstation /work/public/wp-content/uploads/2026/09/wpsb-e2e.txt
    expect_path workstation /work/public/wp-content/mu-plugins/wpsb-e2e-page-builders.php
}

scenario_pull_db_reset_none() {
    seed source workstation
    local status=0
    pull_workstation --skip-files --db-reset none || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "kept because db_reset is none"
    expect_table workstation wp_options
    expect_table workstation abc_options
}

scenario_pull_legacy() {
    seed source workstation
    local status=0
    pull_workstation --destination wpsb-legacy || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "MariaDB client compatibility enabled"
    expect_output "PHP function compatibility enabled"
    expect_output "falling back to scp/tar"
    expect_table workstation abc_options
    expect_no_table workstation abc_shop_options
    expect_path workstation /work/public/wp-includes/version.php
}

scenario_page_builders() {
    seed source workstation
    in_machine source wp --path=/var/www/site --quiet option update wpsb_e2e_fail_bricks 1

    # With files: the stand-ins are an mu-plugin the pull brings along.
    local status=0
    pull_workstation || status=$?
    expect_ok "pull succeeds although a builder fails" "$status"
    expect_output "Local Elementor CSS rebuilt"
    expect_output "Could not rebuild local Bricks CSS"
    expect_set "Elementor stand-in ran locally" "$(option workstation abc_ wpsb_e2e_elementor_flushed)"

    sql workstation "DELETE FROM abc_options WHERE option_name = 'wpsb_e2e_elementor_flushed'"
    status=0
    pull_workstation --skip-files --skip-cache-rebuild || status=$?
    expect_ok "pull with --skip-cache-rebuild succeeds" "$status"
    expect_no_output "Elementor CSS rebuilt"
    expect_eq "Elementor stand-in did not run" "$(option workstation abc_ wpsb_e2e_elementor_flushed)" ""
}

scenario_push_new_host() {
    seed source workstation target-config
    local status=0
    pull_workstation || status=$?
    expect_ok "pull succeeds" "$status"
    status=0
    cli workstation /work push --silent || status=$?
    expect_ok "push succeeds" "$status"
    expect_output "Skipping remote maintenance mode"
    expect_table target abc_options
    expect_table target abc_wpsb_log
    expect_eq "home on the target is the push URL" "$(option target abc_ home)" https://target.wpsb.test
    expect_eq "no workstation URL on the target" "$(url_rows target abc_ workstation.wpsb.test)" 0
    expect_eq "no source URL on the target" "$(url_rows target abc_ source.wpsb.test)" 0
    expect_set "Elementor stand-in ran on the target" "$(option target abc_ wpsb_e2e_elementor_flushed)"
    expect_eq "target keeps its own database name" \
        "$(in_machine target wp --path=/var/www/site config get DB_NAME)" target
}

scenario_push_prefix_mismatch() {
    seed source workstation target-site
    local status=0
    pull_workstation --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    status=0
    cli workstation /work push --silent || status=$?
    expect_fail "push stops" "$status"
    expect_output "the push target uses the table prefix wp_, but the local database uses abc_"
    expect_no_table target abc_options
    expect_no_path target /var/www/site/.maintenance
    expect_eq "target site untouched" "$(option target wp_ blogname)" "WPSB E2E Previous Target"
}

scenario_push_after_skip_import() {
    seed source workstation target-config
    local status=0
    pull_workstation --skip-files || status=$?
    sql workstation "INSERT INTO abc_options (option_name, option_value, autoload) VALUES ('wpsb_e2e_marker', 'from the workstation', 'no')"
    pull_workstation --skip-files --skip-import || status=$?
    expect_ok "pull and pull --skip-import succeed" "$status"
    status=0
    cli workstation /work push --silent --skip-files || status=$?
    expect_ok "push succeeds" "$status"
    expect_eq "target got the local database, not the downloaded dump" \
        "$(option target abc_ wpsb_e2e_marker)" "from the workstation"
}

clone_on_target() {
    in_machine target mkdir -p /home/site/clone
    cli target /home/site/clone clone --silent --destination wpsb-source --remote-path /var/www/site \
        --local-wp-path /var/www/site --db-host db --db-name target --db-user target --db-password target "$@"
}

scenario_clone_existing_site() {
    seed source
    SEED_PREFIX=old_ seed target-site
    local status=0
    clone_on_target || status=$?
    expect_ok "clone succeeds" "$status"
    expect_output "Keeping the source site URL"
    expect_table target abc_options
    expect_no_table target old_options
    expect_table target other_app_sessions
    expect_eq "home stays the source URL" "$(option target abc_ home)" https://source.wpsb.test
    expect_eq "wp-config.php uses the source prefix" \
        "$(in_machine target wp --path=/var/www/site config get table_prefix)" abc_
    expect_path target /var/www/site/wp-content/plugins/cloudflare
}

scenario_clone_db_prefix() {
    seed source target
    local status=0
    clone_on_target --db-prefix new_ || status=$?
    expect_ok "clone succeeds" "$status"
    expect_table target new_options
    expect_no_table target abc_options
    expect_eq "user_roles option renamed" \
        "$(sql target "SELECT COUNT(*) FROM new_options WHERE option_name = 'new_user_roles'")" 1
    expect_eq "capabilities meta key renamed" \
        "$(sql target "SELECT COUNT(*) FROM new_usermeta WHERE meta_key = 'new_capabilities'")" 1
    expect_eq "wp-config.php uses the new prefix" \
        "$(in_machine target wp --path=/var/www/site config get table_prefix)" new_
}

scenario_clone_foreign_table() {
    seed source target
    sql target "CREATE TABLE abc_wpsb_log (id INT PRIMARY KEY)"
    local status=0
    clone_on_target || status=$?
    expect_fail "clone stops" "$status"
    expect_output "which does not belong"
    expect_no_table target abc_options
}

# The checks a pull into a DDEV or wp-env project must pass: installation-only
# export, the database emptied of everything else (db_reset database), URLs
# moved to the project, the blocked plugin kept out, the cache rebuilt.
expect_project_pull() { # project wordpress-root local-url
    expect_table "$1" abc_options
    expect_table "$1" abc_wpsb_log
    expect_no_table "$1" abc_shop_options
    expect_no_table "$1" other_app_sessions
    expect_no_table "$1" zz_local_only
    expect_no_table "$1" wp_options
    expect_eq "home in the database is the project URL" "$(option "$1" abc_ home)" "$3"
    expect_eq "no source URL left in options and posts" "$(url_rows "$1" abc_ source.wpsb.test)" 0
    expect_no_path local "$2/wp-content/plugins/cloudflare"
    expect_path local "$2/wp-content/mu-plugins/wpsb-e2e-page-builders.php"
    expect_set "Elementor stand-in ran" "$(option "$1" abc_ wpsb_e2e_elementor_flushed)"
}

scenario_ddev_pull() {
    seed source
    ddev_ensure
    ddev_reset
    local status=0
    local_run "$DDEV_DIR" wp-ssh-bridge pull --silent -y || status=$?
    expect_ok "pull succeeds" "$status"
    expect_project_pull ddev "$DDEV_DIR" "$(ddev_url)"
    if grep -q "wp-config-ddev.php" "$DDEV_DIR/wp-config.php" 2> /dev/null; then
        pass "wp-config.php includes wp-config-ddev.php"
    else
        fail "wp-config.php includes wp-config-ddev.php" "not found"
    fi
}

scenario_ddev_native_pull() {
    seed source
    ddev_ensure
    ddev_reset
    local status=0
    local_run "$DDEV_DIR" wp-ssh-bridge provider install || status=$?
    local_run "$DDEV_DIR" ddev pull wp-ssh -y || status=$?
    expect_ok "provider install and ddev pull succeed" "$status"
    expect_project_pull ddev "$DDEV_DIR" "$(ddev_url)"
}

scenario_wp_env_pull() {
    seed source
    wpenv_ensure
    wpenv_reset
    local status=0
    local_run "$WPENV_DIR" wp-ssh-bridge pull --silent || status=$?
    expect_ok "pull succeeds" "$status"
    expect_project_pull wp-env "$(wpenv_root)" "http://localhost:$WPENV_PORT"
}

# --- runner ---------------------------------------------------------------------

cmd_test() {
    if [ "${1:-}" = --list ]; then
        local suite members name
        for suite in containers ddev wp-env; do
            printf '%s\n' "$suite"
            case "$suite" in
                containers) members=$SUITE_CONTAINERS ;;
                ddev) members=$SUITE_DDEV ;;
                wp-env) members=$SUITE_WPENV ;;
            esac
            for name in $members; do printf '  %-24s %s\n' "$name" "$(describe_scenario "$name")"; done
        done
        return
    fi
    local selected="" name failed=""
    [ $# -gt 0 ] || set -- containers
    for name in "$@"; do
        case "$name" in
            containers) selected="$selected $SUITE_CONTAINERS" ;;
            ddev) selected="$selected $SUITE_DDEV" ;;
            wp-env) selected="$selected $SUITE_WPENV" ;;
            all) selected="$selected $SCENARIOS" ;;
            *) selected="$selected $name" ;;
        esac
    done
    for name in $selected; do
        case " $SCENARIOS " in *" $name "*) ;; *) die "unknown scenario: $name (see test --list)" ;; esac
    done
    prepare
    : > "$LAST_LOG"
    for name in $selected; do
        printf '\n\033[1m%s\033[0m — %s\n' "$name" "$(describe_scenario "$name")"
        local before=$FAILED_CHECKS
        "scenario_$(printf '%s' "$name" | tr '-' '_')"
        [ "$FAILED_CHECKS" -eq "$before" ] || failed="$failed $name"
    done
    printf '\n'
    if [ -n "$failed" ]; then
        printf '\033[31m%s failed check(s) in:%s\033[0m\nCLI output: %s\n' "$FAILED_CHECKS" "$failed" "$LAST_LOG"
        return 1
    fi
    printf '\033[32mAll scenarios passed.\033[0m\n'
}
