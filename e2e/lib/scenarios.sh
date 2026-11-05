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

SUITE_CONTAINERS="pull pull-db-reset-none pull-legacy page-builders push-new-host push-prefix-mismatch push-after-skip-import push-keeps-pull-omissions push-refuses-pseudonyms clone-existing-site clone-db-prefix clone-foreign-table pseudonymize-shop pseudonymize-shop-legacy-orders pseudonymize-hardened-host pseudonymize-keep-users pseudonymize-project-rules pseudonymize-multisite pseudonymize-fails-closed pseudonymize-skip pseudonymize-clone"
SUITE_DDEV="ddev-pull ddev-native-pull"
SUITE_WPENV="wp-env-pull"
SCENARIOS="$SUITE_CONTAINERS $SUITE_DDEV $SUITE_WPENV"

# The blocked plugins the source runs; see e2e/host/fixtures/blocked-plugins.txt.
BLOCKED_SAMPLE=$(grep -v -e '^#' -e '^$' "$E2E_DIR/host/fixtures/blocked-plugins.txt" | tr '\n' ' ')

describe_scenario() {
    case "$1" in
        pull) echo "standalone pull: prefix alignment, installation-only export, table cleanup, URLs, blocked plugins, uploads" ;;
        pull-db-reset-none) echo "db_reset none keeps the old local installation's tables" ;;
        pull-legacy) echo "pull through a host without rsync, with exec() disabled and legacy MariaDB names" ;;
        page-builders) echo "cache rebuild runs, a failing builder only warns, --skip-cache-rebuild skips it" ;;
        push-new-host) echo "push into a new host prepared per the migration guide" ;;
        push-prefix-mismatch) echo "push stops before anything changes when the target uses another prefix" ;;
        push-after-skip-import) echo "push exports the local database even after pull --skip-import" ;;
        push-refuses-pseudonyms) echo "push stops before it overwrites real customers with pseudonyms" ;;
        clone-existing-site) echo "clone on the target replaces the previous installation, keeps neighbours" ;;
        clone-db-prefix) echo "clone --db-prefix renames tables, user_roles, and user meta keys" ;;
        clone-foreign-table) echo "clone stops before the import when it would overwrite a foreign table" ;;
        pseudonymize-shop) echo "real WooCommerce (HPOS and sync) and EDD: no personal data arrives, the shop still works" ;;
        pseudonymize-shop-legacy-orders) echo "WooCommerce orders in posts and postmeta: no personal data arrives" ;;
        pseudonymize-hardened-host) echo "the export streams from a host with exec() disabled and legacy MariaDB names" ;;
        pseudonymize-keep-users) echo "pseudonymize_keep_user_ids keeps a developer's account usable" ;;
        pseudonymize-project-rules) echo "an uncovered table is reported, and a project rule covers it" ;;
        pseudonymize-multisite) echo "network tables, subsites, and super admins" ;;
        pseudonymize-fails-closed) echo "a failing filter leaves nothing behind, on this machine or on the source" ;;
        pseudonymize-skip) echo "--skip-pseudonymize transfers real data, and says so" ;;
        pseudonymize-clone) echo "clone keeps real data and every file" ;;
        push-keeps-pull-omissions) echo "a file push keeps what a pull left out, and still mirrors the rest" ;;
        ddev-pull) echo "direct pull into a DDEV project: tables, URLs, blocked plugins, cache rebuild" ;;
        ddev-native-pull) echo "ddev pull wp-ssh through the generated provider: the same checks" ;;
        wp-env-pull) echo "pull into a wp-env project: tables, URLs, blocked plugins, cache rebuild" ;;
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

# Real data needs a person at a terminal, so this answers the prompt through a pseudo
# terminal, the way one would. It proves the prompt works, not that it cannot be scripted.
pull_workstation_real() {
    local status=0 line
    line="wp-ssh-bridge pull --silent --skip-pseudonymize"
    for arg in "$@"; do line+=" $arg"; done
    OUT=$(printf 'real data\n' | in_machine workstation env -C /work script -qec "$line" /dev/null 2>&1) || status=$?
    {
        printf '\n$ [workstation:/work] %s  (exit %s)\n' "$line" "$status"
        printf '%s\n' "$OUT"
    } >> "$LAST_LOG"
    return "$status"
}

# WP-CLI against the site of a machine or local project.
site_wp() { # workstation|target|ddev|wp-env args...
    local site=$1
    shift
    case "$site" in
        workstation) in_machine workstation wp --path=/work/public "$@" ;;
        target) in_machine target wp --path=/var/www/site "$@" ;;
        ddev) (cd "$DDEV_DIR" && ddev wp "$@") ;;
        wp-env) wpenv run cli wp "$@" ;;
    esac
}

# Writes a do-nothing plugin under a slug into a WordPress root on this machine,
# as `wpsb-seed plugin-stub` does in the containers.
write_plugin_stub() { # wordpress-root slug
    mkdir -p "$1/wp-content/plugins/$2"
    printf '<?php\n/**\n * Plugin Name: %s (wp-ssh-bridge E2E stand-in)\n * Version: 0.0.0\n */\n' "$2" \
        > "$1/wp-content/plugins/$2/$2.php"
}

# After a pull: no blocked plugin is installed or active, the ordinary plugin
# arrived and stays active, and the one blocked plugin placed locally before
# the pull was removed.
expect_blocked_plugins_cleaned() { # site path-machine wordpress-root
    local active slug
    active=" $(site_wp "$1" plugin list --status=active --field=name 2> /dev/null | tr '\n' ' ') "
    for slug in $BLOCKED_SAMPLE; do
        if path_exists "$2" "$3/wp-content/plugins/$slug"; then
            fail "blocked plugin $slug is gone" "$3/wp-content/plugins/$slug exists"
        else
            case "$active" in
                *" $slug "*) fail "blocked plugin $slug is gone" "it is still active" ;;
                *) pass "blocked plugin $slug is gone" ;;
            esac
        fi
    done
    case "$active" in
        *" wpsb-e2e-keep "*) pass "ordinary plugin wpsb-e2e-keep is active" ;;
        *) fail "ordinary plugin wpsb-e2e-keep is active" "active plugins:$active" ;;
    esac
    expect_output "Removed 1 local-only blocked plugin"
}

# --- scenarios ------------------------------------------------------------------

scenario_pull() {
    seed source workstation
    # A blocked plugin already in the local copy, as after an older pull.
    in_machine workstation wpsb-seed plugin-stub /work/public updraftplus

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
    expect_eq "users arrive pseudonymized" "$(sql workstation "SELECT user_email FROM abc_users WHERE ID = 1")" user-1@example.invalid

    expect_blocked_plugins_cleaned workstation workstation /work/public
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
    pull_workstation_real || status=$?
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

scenario_push_keeps_pull_omissions() {
    seed source workstation target-site
    local site=/var/www/site uploads=wp-content/uploads/woocommerce_uploads
    in_machine target sh -c "cd $site && mkdir -p $uploads && printf x > .env && printf x > wp-config.php.bak && printf x > old-dump.sql && printf x > $uploads/product.zip && printf x > stale.txt"
    local status=0
    pull_workstation || status=$?
    expect_ok "pull succeeds" "$status"
    in_machine workstation sh -c "mkdir -p /work/public/$uploads && printf y > /work/public/$uploads/new.zip"
    status=0
    cli workstation /work push --silent --skip-db || status=$?
    expect_ok "file push succeeds" "$status"
    expect_path target "$site/.env"
    expect_path target "$site/wp-config.php.bak"
    expect_path target "$site/old-dump.sql"
    expect_path target "$site/$uploads/product.zip"
    expect_path target "$site/$uploads/new.zip"
    expect_no_path target "$site/stale.txt"
}

scenario_push_prefix_mismatch() {
    seed source workstation target-site
    local status=0
    pull_workstation_real --skip-files || status=$?
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
    pull_workstation_real --skip-files || status=$?
    sql workstation "INSERT INTO abc_options (option_name, option_value, autoload) VALUES ('wpsb_e2e_marker', 'from the workstation', 'no')"
    pull_workstation_real --skip-files --skip-import || status=$?
    expect_ok "pull and pull --skip-import succeed" "$status"
    status=0
    cli workstation /work push --silent --skip-files || status=$?
    expect_ok "push succeeds" "$status"
    expect_eq "target got the local database, not the downloaded dump" \
        "$(option target abc_ wpsb_e2e_marker)" "from the workstation"
}

scenario_push_refuses_pseudonyms() {
    seed source workstation target-site
    local status=0
    pull_workstation --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    status=0
    cli workstation /work push --silent --skip-files || status=$?
    expect_fail "push stops" "$status"
    expect_output "the local database holds pseudonyms"
    expect_eq "target site untouched" "$(option target wp_ blogname)" "WPSB E2E Previous Target"
    # With the override, the next safety check is what stops this particular push.
    status=0
    cli workstation /work push --silent --skip-files --allow-pseudonymized-push || status=$?
    expect_no_output "the local database holds pseudonyms"
    expect_output "the push target uses the table prefix"
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
    local slug
    for slug in $BLOCKED_SAMPLE; do
        expect_path target "/var/www/site/wp-content/plugins/$slug"
    done
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

# --- pseudonymization -----------------------------------------------------------

# Everything the fixtures plant as personal data carries one of these.
LEAK_PATTERN='canary|203\.0\.113\.|realmail\.test|555012[0-9]|0532 0130|4111 1111|2001:4860'

# Lists the tables of a database holding planted personal data, by looking at the
# real dump rather than through WordPress.
leaking_tables() { # database
    local table found="" hits
    for table in $(tables "$1"); do
        # Not grep -q: it closes the pipe early, which pipefail reports as no match.
        hits=$(compose exec -T db mariadb-dump -uroot -proot --skip-extended-insert "$1" "$table" | grep -ciE "$LEAK_PATTERN" || true)
        [ "$hits" = 0 ] || found="$found $table"
    done
    printf '%s' "${found# }"
}

expect_no_leaks() { # database [expected leftover tables]
    expect_eq "no planted personal data in $1" "$(leaking_tables "$1")" "${2:-}"
}

# Proves the fixture really challenges the filter: a table that holds no planted
# data on the source would pass the leak check without the rule doing anything.
expect_source_holds() { # table...
    local holding table
    holding=" $(leaking_tables source) "
    for table in "$@"; do
        case "$holding" in
            *" $table "*) pass "the source holds planted data in $table" ;;
            *) fail "the source holds planted data in $table" "it holds some only in:$holding" ;;
        esac
    done
}

source_checksums() {
    local table
    for table in $(tables source); do
        sql source "CHECKSUM TABLE \`$table\`"
    done
}

expect_source_clean_after_pull() { # checksums before
    expect_eq "the source database is unchanged" "$(source_checksums)" "$1"
    expect_eq "no transfer files left on the source" \
        "$(in_machine source sh -c 'ls /tmp | grep -c "^ddev-" || true')" 0
}

# A pull removes its dump after the import, so this pulls one that stays.
expect_downloaded_dump_clean() { # pull arguments...
    local status=0 hits
    pull_workstation --skip-files --skip-import "$@" || status=$?
    expect_ok "pull --skip-import succeeds" "$status"
    expect_path workstation /work/.wp-ssh/.downloads/db.sql.gz
    hits=$(in_machine workstation sh -c "gzip -dc /work/.wp-ssh/.downloads/db.sql.gz | grep -ciE '$LEAK_PATTERN' || true")
    expect_eq "the dump on this machine holds no planted personal data" "$hits" 0
    # Proves the search above can find something: the real dump is there and readable.
    expect_eq "the dump holds the pseudonyms instead" \
        "$(in_machine workstation sh -c "gzip -dc /work/.wp-ssh/.downloads/db.sql.gz | grep -c 'user-2@example.invalid' | grep -vc '^0$' || true")" 1
    expect_no_path workstation /work/.wp-ssh/.downloads/db.sql.gz.partial
}

# What the scan and the column names leave behind, and what they must not touch.
expect_free_text_replaced() { # database
    local note
    note=$(sql "$1" "SELECT body FROM abc_wpsb_notes WHERE id = 1")
    case "$note" in
        "Call User 2 at +00 000 0000000 or +00 000 0000000, from 192.0.2.1 or 2001:db8::1. IBAN XX00REDACTED, card 0000-0000-0000-0000, address guest-"*"@example.invalid. WooCommerce 8.6.1.0 stays.")
            pass "free text holds placeholders only" ;;
        *) fail "free text holds placeholders only" "$note" ;;
    esac
    expect_eq "a datetime column stays" "$(sql "$1" "SELECT created FROM abc_wpsb_notes WHERE id = 1")" "2026-01-01 10:00:00"
    expect_eq "a plugin's phone column gets a placeholder" "$(sql "$1" "SELECT phone FROM abc_wpsb_crm_contacts WHERE id = 1")" "+00 000 0000000"
    expect_eq "its birth date too" "$(sql "$1" "SELECT date_of_birth FROM abc_wpsb_crm_contacts WHERE id = 1")" 1970-01-01
    expect_eq "a label about a phone stays" "$(sql "$1" "SELECT phone_label FROM abc_wpsb_crm_contacts WHERE id = 1")" Telefon
    expect_eq "a form's phone field gets a placeholder" "$(sql "$1" "SELECT meta_value FROM abc_wpsb_form_entries WHERE id = 1")" "+00 000 0000000"
    expect_eq "a form's other field stays" "$(sql "$1" "SELECT meta_value FROM abc_wpsb_form_entries WHERE id = 3")" blue
    expect_eq "a name in lower case with an odd gap, in a table before its owner's" \
        "$(sql "$1" "SELECT body FROM abc_wpsb_aaa_log WHERE id = 1 AND body LIKE 'Guest % and Guest % called'" | wc -l | tr -d ' ')" 1
    expect_eq "a message signed by the row's own person" \
        "$(sql "$1" "SELECT message FROM abc_wpsb_tickets WHERE id = 1 AND message LIKE 'Thanks, Guest %'" | wc -l | tr -d ' ')" 1
    expect_eq "an article number shaped like a phone number stays" \
        "$(sql "$1" "SELECT meta_value FROM abc_postmeta WHERE meta_key = '_sku' AND meta_value = '0123-456789'" | sort -u)" 0123-456789
    expect_eq "a slug shaped like a phone number stays" \
        "$(sql "$1" "SELECT post_name FROM abc_posts WHERE post_title = 'Protected product'")" 0170-5551234
    expect_eq "a brand keeps its name in a product text, though a commenter shares it" \
        "$(sql "$1" "SELECT post_content FROM abc_posts WHERE post_title = 'Protected product'")" "Worn by Protectedbrand Label since 1970."
    expect_eq "while the commenter of that name gets a pseudonym" \
        "$(sql "$1" "SELECT COUNT(*) FROM abc_comments WHERE comment_author = 'Protectedbrand Label'")" 0
}

COMMON_SHOP_TABLES=(abc_wpsb_aaa_log abc_wpsb_tickets abc_wpsb_notes abc_wpsb_form_entries abc_wpsb_crm_contacts abc_users abc_usermeta abc_comments abc_options abc_posts abc_postmeta abc_woocommerce_sessions abc_woocommerce_payment_tokens abc_woocommerce_payment_tokenmeta abc_woocommerce_log abc_woocommerce_api_keys abc_woocommerce_downloadable_product_permissions abc_wc_download_log abc_wc_customer_lookup abc_edd_customers abc_edd_customer_addresses abc_edd_customer_email_addresses abc_edd_orders abc_edd_order_addresses abc_edd_ordermeta abc_edd_customermeta abc_edd_order_transactions abc_edd_logs_file_downloads abc_edd_notes)

scenario_pseudonymize_shop() {
    seed source-shop workstation
    expect_source_holds "${COMMON_SHOP_TABLES[@]}" abc_wc_orders abc_wc_order_addresses abc_wc_orders_meta
    local before status=0
    before=$(source_checksums)
    pull_workstation || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "Pull source database downloaded and pseudonymized"
    expect_output "Personal data was replaced in tables no pseudonymization rule covers"
    expect_output "abc_wpsb_crm_contacts ("

    expect_no_leaks workstation
    expect_free_text_replaced workstation
    expect_source_clean_after_pull "$before"
    expect_eq "the marker that stops a push is set" \
        "$(option workstation abc_ wp_ssh_bridge_pseudonymized | cut -c1-2)" 20

    # One person keeps one pseudonym across WordPress, WooCommerce, and EDD.
    local anna=user-2@example.invalid
    expect_eq "the user's address" "$(sql workstation "SELECT user_email FROM abc_users WHERE ID = 2")" "$anna"
    expect_eq "their customer record" "$(sql workstation "SELECT DISTINCT email FROM abc_wc_customer_lookup WHERE user_id = 2")" "$anna"
    expect_eq "their orders, including a guest order under the same address in other case" \
        "$(sql workstation "SELECT COUNT(*) FROM abc_wc_orders WHERE billing_email = '$anna'")" 2
    expect_eq "their comment" "$(sql workstation "SELECT DISTINCT comment_author_email FROM abc_comments WHERE user_id = 2")" "$anna"
    expect_eq "their EDD customer" "$(sql workstation "SELECT email FROM abc_edd_customers WHERE user_id = 2")" "$anna"
    expect_eq "a guest's two orders share one pseudonym" \
        "$(sql workstation "SELECT COUNT(DISTINCT billing_email), COUNT(*) FROM abc_wc_orders WHERE billing_email LIKE 'guest-%'" | tr '\t' ' ')" "1 2"
    expect_eq "the guest's addresses follow their order" \
        "$(sql workstation "SELECT COUNT(*) FROM abc_wc_order_addresses a JOIN abc_wc_orders o ON o.id = a.order_id WHERE o.billing_email LIKE 'guest-%' AND CONCAT('guest-', a.last_name, '@example.invalid') = o.billing_email")" 4

    # What a developer needs stays.
    expect_eq "order totals survive" "$(sql workstation "SELECT SUM(total_amount) FROM abc_wc_orders")" \
        "$(sql source "SELECT SUM(total_amount) FROM abc_wc_orders")"
    expect_eq "countries survive" "$(sql workstation "SELECT DISTINCT country FROM abc_wc_order_addresses")" DE
    expect_eq "products survive" "$(sql workstation "SELECT post_title FROM abc_posts WHERE post_type = 'product' LIMIT 1")" "WPSB E2E Product"
    expect_eq "a serialized option with an address stays readable" \
        "$(site_wp workstation option pluck wpsb_e2e_serialized contact | cut -d@ -f2)" example.invalid

    # The real plugins read the rewritten data through their own APIs.
    expect_eq "WooCommerce still reads its orders" \
        "$(site_wp workstation eval 'echo count(wc_get_orders(array("limit" => -1, "type" => "shop_order")));')" 4
    expect_eq "WooCommerce finds them by the pseudonym" \
        "$(site_wp workstation eval 'echo count(wc_get_orders(array("limit" => -1, "billing_email" => "user-2@example.invalid")));')" 2
    # shellcheck disable=SC2016 # PHP code, for WP-CLI to evaluate.
    expect_eq "WooCommerce reads the customer" \
        "$(site_wp workstation eval '$c = new WC_Customer(2); echo $c->get_billing_email(), " ", $c->get_billing_country();')" "$anna DE"
    expect_eq "EDD reads its customer" \
        "$(site_wp workstation eval 'echo edd_get_customer_by("email", "user-2@example.invalid")->user_id;')" 2
    expect_eq "webhooks cannot fire from the copy" "$(sql workstation "SELECT DISTINCT status FROM abc_wc_webhooks")" disabled
    expect_eq "nobody can log in with a production password" \
        "$(sql workstation "SELECT COUNT(*) FROM abc_users WHERE user_pass <> '!pseudonymized'")" 0

    expect_downloaded_dump_clean
    expect_source_clean_after_pull "$before"
}

scenario_pseudonymize_shop_legacy_orders() {
    SEED_ORDERS=legacy seed source-shop workstation
    expect_source_holds "${COMMON_SHOP_TABLES[@]}"
    expect_eq "the source really keeps its orders in postmeta" \
        "$(sql source "SELECT COUNT(*) FROM abc_postmeta WHERE meta_key = '_billing_email'")" 4
    local status=0
    pull_workstation --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    expect_no_leaks workstation
    expect_eq "the registered customer's order follows their address" \
        "$(sql workstation "SELECT COUNT(*) FROM abc_postmeta WHERE meta_key = '_billing_email' AND meta_value = 'user-2@example.invalid'")" 2
    expect_eq "the customer link survives" \
        "$(sql workstation "SELECT COUNT(*) FROM abc_postmeta WHERE meta_key = '_customer_user' AND meta_value = '2'")" 1
}

scenario_pseudonymize_hardened_host() {
    seed source-shop workstation
    local status=0
    pull_workstation --skip-files --destination wpsb-legacy || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "PHP function compatibility enabled"
    expect_output "MariaDB client compatibility enabled"
    expect_no_leaks workstation
    expect_downloaded_dump_clean --destination wpsb-legacy
}

scenario_pseudonymize_keep_users() {
    seed source workstation
    printf 'pseudonymize_keep_user_ids: "1"\n' | in_machine workstation sh -c 'cat >> /work/.wp-ssh.yaml'
    sql source "INSERT INTO abc_users (ID, user_login, user_pass, user_nicename, user_email, display_name) VALUES (2, 'canary', 'x', 'canary', 'canary@realmail.test', 'Canary')"
    local status=0
    pull_workstation --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    expect_eq "the kept account keeps its login" "$(sql workstation "SELECT user_login FROM abc_users WHERE ID = 1")" admin
    expect_eq "its password still works" \
        "$(site_wp workstation user check-password admin wpsb-e2e-admin > /dev/null 2>&1; echo $?)" 0
    expect_eq "everyone else is pseudonymized" "$(sql workstation "SELECT user_login FROM abc_users WHERE ID = 2")" user-2
    expect_no_leaks workstation
}

scenario_pseudonymize_project_rules() {
    seed source-shop workstation
    in_machine workstation sh -c 'cat >> /work/.wp-ssh.yaml' <<'YAML'
pseudonymize_rules:
  - table: wpsb_crm_contacts
    columns: "email=email,full_name=full_name"
YAML
    local status=0
    pull_workstation --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    expect_no_output "abc_wpsb_crm_contacts ("
    expect_no_leaks workstation
    expect_eq "the contact is merged with the user of the same address" \
        "$(sql workstation "SELECT email FROM abc_wpsb_crm_contacts WHERE id = 1")" user-2@example.invalid
}

scenario_pseudonymize_multisite() {
    seed source-multisite workstation
    expect_source_holds abc_users abc_usermeta abc_sitemeta abc_signups abc_registration_log abc_2_comments abc_2_options
    local status=0
    pull_workstation --skip-files --db-reset none || status=$?
    expect_ok "pull succeeds" "$status"
    expect_no_leaks workstation
    local id
    id=$(sql source "SELECT ID FROM abc_users WHERE user_login = 'canarynetwork'")
    expect_eq "super admins follow the renamed logins" \
        "$(sql workstation "SELECT meta_value FROM abc_sitemeta WHERE meta_key = 'site_admins'" | grep -c "user-$id")" 1
    expect_eq "a subsite comment follows its author" \
        "$(sql workstation "SELECT comment_author_email FROM abc_2_comments WHERE user_id = $id")" "user-$id@example.invalid"
}

scenario_pseudonymize_fails_closed() {
    seed source workstation
    sql source "RENAME TABLE abc_users TO abc_members"
    local status=0
    pull_workstation --skip-files || status=$?
    expect_fail "pull stops" "$status"
    expect_output "the export must start with abc_users"
    expect_table workstation wp_options
    expect_no_table workstation abc_options
    expect_no_path workstation /work/.wp-ssh/.downloads/db.sql.gz
    expect_eq "no transfer files left on the source" \
        "$(in_machine source sh -c 'ls /tmp | grep -c "^ddev-" || true')" 0
    sql source "RENAME TABLE abc_members TO abc_users"
}

scenario_pseudonymize_skip() {
    seed source workstation
    local status=0
    pull_workstation --skip-files --skip-pseudonymize || status=$?
    expect_fail "without a terminal, real data is refused" "$status"
    expect_output "needs a person to confirm"
    expect_no_path workstation /work/.wp-ssh/.downloads/db.sql.gz
    status=0
    pull_workstation_real --skip-files || status=$?
    expect_ok "pull succeeds" "$status"
    expect_output "this pull transfers real personal data"
    expect_eq "the real address arrives" \
        "$(sql workstation "SELECT user_email FROM abc_users WHERE ID = 1")" admin@example.test
    expect_eq "no marker is set" "$(option workstation abc_ wp_ssh_bridge_pseudonymized)" ""
}

scenario_pseudonymize_clone() {
    seed source-shop target
    in_machine source sh -c 'cd /var/www/site && printf x > .env && printf x > wp-config.php.bak && printf x > old-dump.sql'
    local status=0
    clone_on_target || status=$?
    expect_ok "clone succeeds" "$status"
    expect_eq "a clone keeps real customers by default" \
        "$(sql target "SELECT user_email FROM abc_users WHERE ID = 2")" canary-anna@realmail.test
    expect_no_output "pseudonymized"
    expect_path target /var/www/site/.env
    expect_path target /var/www/site/wp-config.php.bak
    expect_path target /var/www/site/old-dump.sql
    expect_eq "a clone needs no confirmation and keeps the real password hashes" \
        "$(sql target "SELECT COUNT(*) FROM abc_users WHERE user_pass = '!pseudonymized'")" 0
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
    expect_eq "users arrive pseudonymized" "$(sql "$1" "SELECT user_email FROM abc_users WHERE ID = 1")" user-1@example.invalid
    expect_blocked_plugins_cleaned "$1" local "$2"
    expect_path local "$2/wp-content/mu-plugins/wpsb-e2e-page-builders.php"
    expect_set "Elementor stand-in ran" "$(option "$1" abc_ wpsb_e2e_elementor_flushed)"
}

scenario_ddev_pull() {
    seed source
    ddev_ensure
    ddev_reset
    write_plugin_stub "$DDEV_DIR" wp-mail-smtp
    local status=0
    local_run "$DDEV_DIR" wp-ssh-bridge pull --silent -y || status=$?
    expect_ok "pull succeeds" "$status"
    expect_project_pull ddev "$DDEV_DIR" "$(ddev_url)"
    if grep -q "wp-config-ddev.php" "$DDEV_DIR/wp-config.php" 2> /dev/null; then
        pass "wp-config.php includes wp-config-ddev.php"
    else
        fail "wp-config.php includes wp-config-ddev.php" "not found"
    fi
    expect_no_source_secrets "$DDEV_DIR/wp-config.php"
}

# The source's wp-config.php holds these, so none may reach this machine.
expect_no_source_secrets() { # file
    local source_password source_salt
    source_password=$(in_machine source wp --path=/var/www/site config get DB_PASSWORD 2> /dev/null)
    source_salt=$(in_machine source wp --path=/var/www/site config get AUTH_KEY 2> /dev/null)
    expect_set "the source has a database password to protect" "$source_password"
    if grep -qF -e "'$source_password'" -e "$source_salt" "$1" 2> /dev/null; then
        fail "the source's credentials stay out of wp-config.php" "found in $1"
    else
        pass "the source's credentials stay out of wp-config.php"
    fi
}

scenario_ddev_native_pull() {
    seed source
    ddev_ensure
    ddev_reset
    write_plugin_stub "$DDEV_DIR" wp-mail-smtp
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
    write_plugin_stub "$(wpenv_root)" cloudflare
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
