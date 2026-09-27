package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// anonymizeFallbackDomain is used for pseudonymized email addresses when no destination site host can be detected.
// The .invalid TLD is reserved (RFC 2606), so pseudonymized addresses can never be delivered.
const anonymizeFallbackDomain = "example.invalid"

// anonymizeUsersLoginTempPrefix marks the first pass of the two-pass login rename. user_login
// carries a unique key, so rows first move to unique temporary names before taking the final
// user-<ID> form; otherwise an existing login such as "user-3" could collide mid-statement.
const anonymizeUsersLoginTempPrefix = "wp-ssh-tmp-"

var (
	validAnonymizeEmailDomain = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	validTablePrefix          = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// anonymizeOperation selects which per-command anonymization setting applies.
type anonymizeOperation string

const (
	anonymizePull  anonymizeOperation = "pull"
	anonymizeClone anonymizeOperation = "clone"
	anonymizePush  anonymizeOperation = "push"
)

func anonymizeOperationForPull(clone bool) anonymizeOperation {
	if clone {
		return anonymizeClone
	}
	return anonymizePull
}

// anonymizeEnabled reports whether the operation rewrites user and comment data. Pull
// anonymizes by default; clone and push only when explicitly enabled. skip_anonymize wins everywhere.
func (cfg Config) anonymizeEnabled(operation anonymizeOperation) bool {
	if cfg.SkipAnonymize {
		return false
	}
	switch operation {
	case anonymizePull:
		return true
	case anonymizeClone:
		return cfg.CloneAnonymize
	case anonymizePush:
		return cfg.PushAnonymize
	default:
		return false
	}
}

// validateAnonymize rejects email domains that cannot be embedded safely in SQL or addresses.
func (cfg Config) validateAnonymize() error {
	domain := strings.TrimSpace(cfg.AnonymizeEmailDomain)
	if domain == "" {
		return nil
	}
	if !validAnonymizeEmailDomain.MatchString(domain) {
		return errors.New("anonymize_email_domain must be a hostname containing only letters, numbers, dots, or hyphens, for example example.ddev.site")
	}
	return nil
}

// anonymizeEmailDomain prefers the configured domain, then the destination site host, then a
// reserved fallback. Derived hosts need a dot because WordPress rejects single-label email domains.
func anonymizeEmailDomain(configured string, destinationURL string) string {
	if domain := strings.TrimSpace(configured); domain != "" {
		return domain
	}
	host := replacementHost(destinationURL)
	if host != "" && strings.Contains(host, ".") && validAnonymizeEmailDomain.MatchString(host) {
		return host
	}
	return anonymizeFallbackDomain
}

// anonymizeUserMetaRule maps one usermeta key to the SQL expression that replaces its value.
type anonymizeUserMetaRule struct {
	key   string
	value string
}

// anonymizeUserMetaRules lists the profile and WooCommerce customer fields rewritten per user.
// Country and state stay untouched because they are coarse and keep tax/shipping logic working.
func anonymizeUserMetaRules(domain string) []anonymizeUserMetaRule {
	firstName := "'User'"
	lastName := "CAST(user_id AS CHAR)"
	email := "CONCAT('user-', user_id, " + sqlQuote("@"+domain) + ")"
	rules := []anonymizeUserMetaRule{
		{key: "first_name", value: firstName},
		{key: "last_name", value: lastName},
		{key: "nickname", value: "CONCAT('user-', user_id)"},
		{key: "description", value: "'Anonymized user profile.'"},
		{key: "billing_email", value: email},
	}
	for _, scope := range []string{"billing", "shipping"} {
		rules = append(rules,
			anonymizeUserMetaRule{key: scope + "_first_name", value: firstName},
			anonymizeUserMetaRule{key: scope + "_last_name", value: lastName},
			anonymizeUserMetaRule{key: scope + "_company", value: "''"},
			anonymizeUserMetaRule{key: scope + "_address_1", value: "'Example Street 1'"},
			anonymizeUserMetaRule{key: scope + "_address_2", value: "''"},
			anonymizeUserMetaRule{key: scope + "_city", value: "'Example City'"},
			anonymizeUserMetaRule{key: scope + "_postcode", value: "'12345'"},
			anonymizeUserMetaRule{key: scope + "_phone", value: "''"},
		)
	}
	return rules
}

// anonymizeUsersSQL rewrites identifying user columns with ID-derived pseudonyms in bulk.
// Only direct SQL is used so WordPress never sends email-change notifications to the
// original addresses. The statements run in one transaction, so a failure leaves neither
// temporary logins nor half-rewritten records behind on InnoDB tables. No result set is
// selected because `wp db query` only reports "Rows affected" for modifying statements.
func anonymizeUsersSQL(prefix string, domain string, tempToken string) string {
	users := "`" + prefix + "users`"
	usermeta := "`" + prefix + "usermeta`"
	rules := anonymizeUserMetaRules(domain)
	cases := make([]string, 0, len(rules))
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		cases = append(cases, "WHEN "+sqlQuote(rule.key)+" THEN "+rule.value)
		keys = append(keys, sqlQuote(rule.key))
	}
	return strings.Join([]string{
		"START TRANSACTION;",
		fmt.Sprintf("UPDATE %s SET user_login = CONCAT(%s, ID);", users, sqlQuote(anonymizeUsersLoginTempPrefix+tempToken+"-")),
		fmt.Sprintf("UPDATE %s SET user_login = CONCAT('user-', ID), user_nicename = CONCAT('user-', ID), user_email = CONCAT('user-', ID, %s), display_name = CONCAT('User ', ID), user_url = '', user_activation_key = '';", users, sqlQuote("@"+domain)),
		fmt.Sprintf("UPDATE %s SET meta_value = CASE meta_key %s END WHERE meta_key IN (%s);", usermeta, strings.Join(cases, " "), strings.Join(keys, ", ")),
		fmt.Sprintf("DELETE FROM %s WHERE meta_key = 'session_tokens';", usermeta),
		"COMMIT;",
	}, " ")
}

// commentTable is one per-site comments table and whether its commentmeta table exists.
type commentTable struct {
	name    string
	hasMeta bool
}

func (table commentTable) metaName() string {
	return strings.TrimSuffix(table.name, "comments") + "commentmeta"
}

// commentTableNames selects the per-site comments tables (wp_comments, wp_2_comments, ...)
// from `wp db tables` output.
func commentTableNames(prefix string, tables []string) []commentTable {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `(?:[0-9]+_)?comments$`)
	present := map[string]bool{}
	for _, table := range tables {
		present[table] = true
	}
	comments := []commentTable{}
	for _, table := range tables {
		if !pattern.MatchString(table) {
			continue
		}
		entry := commentTable{name: table}
		entry.hasMeta = present[entry.metaName()]
		comments = append(comments, entry)
	}
	return comments
}

// anonymizeCommentsSQL pseudonymizes who wrote each comment and what they wrote: author
// name, email, URL, IP address, user agent, and the comment text are replaced, and Akismet
// meta is deleted because it stores a full copy of the submitted comment. Thread, post,
// date, status, and type stay intact. Registered authors keep the same pseudonym as their
// user record; guests get a per-comment one.
func anonymizeCommentsSQL(tables []commentTable, domain string) string {
	statements := []string{"START TRANSACTION;"}
	for _, table := range tables {
		statements = append(statements, strings.Join([]string{
			fmt.Sprintf("UPDATE `%s` SET", table.name),
			"comment_author = CASE WHEN user_id > 0 THEN CONCAT('User ', user_id) ELSE CONCAT('Guest ', comment_ID) END,",
			fmt.Sprintf("comment_author_email = CASE WHEN user_id > 0 THEN CONCAT('user-', user_id, %s) ELSE CONCAT('guest-', comment_ID, %s) END,", sqlQuote("@"+domain), sqlQuote("@"+domain)),
			"comment_author_url = '', comment_author_IP = '', comment_agent = '',",
			"comment_content = CONCAT('Anonymized comment ', comment_ID, '.');",
		}, " "))
		if table.hasMeta {
			statements = append(statements, fmt.Sprintf("DELETE FROM `%s` WHERE meta_key LIKE 'akismet_%%';", table.metaName()))
		}
	}
	return strings.Join(append(statements, "COMMIT;"), " ")
}

// anonymizeUsersEmailPattern renders the resulting address scheme for step output.
func anonymizeUsersEmailPattern(domain string) string {
	return "user-<ID>@" + domain
}

func anonymizeUsersDoneMessage(domain string) string {
	return fmt.Sprintf("User records anonymized (%s)", anonymizeUsersEmailPattern(domain))
}

func anonymizeCommentsDoneMessage(tables int) string {
	return fmt.Sprintf("Comment records anonymized in %d %s", tables, pluralNoun(tables, "table"))
}

func validateTablePrefix(prefix string) error {
	if prefix == "" || !validTablePrefix.MatchString(prefix) {
		return fmt.Errorf("detected WordPress table prefix %q is not safe for SQL; expected letters, numbers, and underscores", prefix)
	}
	return nil
}

// wpRunner abstracts local versus push target WP-CLI execution for the anonymization steps.
type wpRunner interface {
	label() string
	output(ctx context.Context, args ...string) (string, error)
	runSilent(ctx context.Context, args ...string) error
	dbQuery(ctx context.Context, query string) error
	isMultisite(ctx context.Context) bool
}

type localWPRunner struct {
	app         *App
	projectRoot string
	cfg         Config
}

func (runner localWPRunner) label() string {
	return "local"
}

func (runner localWPRunner) output(ctx context.Context, args ...string) (string, error) {
	return runner.app.wpOutputErr(ctx, runner.projectRoot, runner.cfg, args...)
}

func (runner localWPRunner) runSilent(ctx context.Context, args ...string) error {
	return runner.app.runWPSilent(ctx, runner.projectRoot, runner.cfg, args...)
}

func (runner localWPRunner) dbQuery(ctx context.Context, query string) error {
	_, err := runner.output(ctx, "db", "query", query)
	return err
}

func (runner localWPRunner) isMultisite(ctx context.Context) bool {
	return runner.app.isMultisite(ctx, runner.projectRoot, runner.cfg)
}

type remoteWPRunner struct {
	app         *App
	projectRoot string
	target      RemoteTarget
}

func (runner remoteWPRunner) label() string {
	return "push target"
}

func (runner remoteWPRunner) output(ctx context.Context, args ...string) (string, error) {
	return runner.app.remoteWPOutputErr(ctx, runner.projectRoot, runner.target, args...)
}

func (runner remoteWPRunner) runSilent(ctx context.Context, args ...string) error {
	return runner.app.runRemoteWPSilent(ctx, runner.projectRoot, runner.target, args...)
}

// dbQuery needs the MariaDB client shim because `wp db query` shells out to the mysql client,
// unlike the other probes which read through PHP.
func (runner remoteWPRunner) dbQuery(ctx context.Context, query string) error {
	mariaDBCompat := runner.app.remoteMariaDBCompatibilityForDatabaseCommand(ctx, runner.projectRoot, runner.target)
	command := remoteWPDatabaseCommand(runner.target, randomID(), mariaDBCompat, "db", "query", query)
	_, err := runner.app.outputSSH(ctx, runner.projectRoot, runner.target, command)
	return err
}

func (runner remoteWPRunner) isMultisite(ctx context.Context) bool {
	return runner.app.remoteIsMultisite(ctx, runner.projectRoot, runner.target)
}

// anonymizeAfterImport runs the local anonymization when the operation enables it and a
// database was imported in this run; otherwise it is a no-op.
func (a *App) anonymizeAfterImport(ctx context.Context, projectRoot string, cfg Config, operation anonymizeOperation, dbImported bool) error {
	if !dbImported || !cfg.anonymizeEnabled(operation) {
		return nil
	}
	return a.anonymizeLocalData(ctx, projectRoot, cfg, preferredLocalURL(projectRoot, cfg))
}

// anonymizeLocalData pseudonymizes the imported local user and comment records with bulk SQL through local WP-CLI.
func (a *App) anonymizeLocalData(ctx context.Context, projectRoot string, cfg Config, destinationURL string) error {
	runner := localWPRunner{app: a, projectRoot: projectRoot, cfg: cfg}
	return a.anonymizeData(ctx, runner, anonymizeEmailDomain(cfg.AnonymizeEmailDomain, destinationURL))
}

// anonymizeRemoteData pseudonymizes the imported push target user and comment records with bulk SQL through remote WP-CLI.
func (a *App) anonymizeRemoteData(ctx context.Context, projectRoot string, target RemoteTarget, cfg Config, destinationURL string) error {
	runner := remoteWPRunner{app: a, projectRoot: projectRoot, target: target}
	return a.anonymizeData(ctx, runner, anonymizeEmailDomain(cfg.AnonymizeEmailDomain, destinationURL))
}

// anonymizeData runs the user step first so identities are gone even when the comment
// step fails, then rewrites comments and flushes the object cache once at the end.
func (a *App) anonymizeData(ctx context.Context, runner wpRunner, domain string) error {
	prefix, err := a.anonymizeUsers(ctx, runner, domain)
	if err != nil {
		return err
	}
	if err := a.anonymizeComments(ctx, runner, domain, prefix); err != nil {
		return err
	}
	if err := runner.runSilent(ctx, "cache", "flush"); err != nil {
		a.UI.Warning("Could not flush the %s object cache after anonymizing users; cached user data may be stale until it expires.", runner.label())
	}
	return nil
}

// anonymizeUsers rewrites the users and usermeta tables and returns the detected table prefix.
func (a *App) anonymizeUsers(ctx context.Context, runner wpRunner, domain string) (string, error) {
	label := runner.label()
	prefix := ""
	title := fmt.Sprintf("Anonymizing %s user records (%s)", label, anonymizeUsersEmailPattern(domain))
	err := a.runStepResult(title, func() (string, error) {
		raw, err := runner.output(ctx, "db", "prefix")
		if err != nil {
			return "", fmt.Errorf("detect %s WordPress table prefix: %w", label, err)
		}
		prefix = strings.TrimSpace(raw)
		if err := validateTablePrefix(prefix); err != nil {
			return "", err
		}

		// Multisite stores super admins by login name, so capture their IDs before the
		// rename and rewrite the network option afterwards.
		superAdminIDs, multisite, err := a.multisiteSuperAdminIDs(ctx, runner)
		if err != nil {
			return "", err
		}

		if err := runner.dbQuery(ctx, anonymizeUsersSQL(prefix, domain, randomID())); err != nil {
			return "", fmt.Errorf("anonymize %s user records: %w", label, err)
		}

		if multisite {
			if _, err := runner.output(ctx, "site", "option", "update", "site_admins", superAdminLoginsJSON(superAdminIDs), "--format=json"); err != nil {
				return "", fmt.Errorf("restore %s multisite super admins after anonymization: %w", label, err)
			}
		}
		return anonymizeUsersDoneMessage(domain), nil
	})
	return prefix, err
}

// anonymizeComments rewrites every per-site comments table, which covers multisite
// networks without extra probes because `wp db tables` lists all blog prefixes.
func (a *App) anonymizeComments(ctx context.Context, runner wpRunner, domain string, prefix string) error {
	label := runner.label()
	return a.runStepResult(fmt.Sprintf("Anonymizing %s comment records", label), func() (string, error) {
		listing, err := runner.output(ctx, wpTablesArgs...)
		if err != nil {
			return "", fmt.Errorf("list %s WordPress tables: %w", label, err)
		}
		tables := commentTableNames(prefix, parseTableList(listing))
		if len(tables) == 0 {
			return "No comment tables found", nil
		}
		if err := runner.dbQuery(ctx, anonymizeCommentsSQL(tables, domain)); err != nil {
			return "", fmt.Errorf("anonymize %s comment records: %w", label, err)
		}
		return anonymizeCommentsDoneMessage(len(tables)), nil
	})
}

// multisiteSuperAdminIDs resolves the current super admin logins to user IDs with one
// network-wide lookup; WP-CLI splits `--login__in` on commas and WordPress logins cannot
// contain commas. It reports whether the install is a multisite so callers know when the
// network option needs a rewrite.
func (a *App) multisiteSuperAdminIDs(ctx context.Context, runner wpRunner) ([]int, bool, error) {
	if !runner.isMultisite(ctx) {
		return nil, false, nil
	}
	label := runner.label()
	listing, err := runner.output(ctx, "super-admin", "list")
	if err != nil {
		return nil, true, fmt.Errorf("list %s multisite super admins before anonymization: %w", label, err)
	}
	logins := nonEmptyLines(listing)
	if len(logins) == 0 {
		return nil, true, nil
	}
	raw, err := runner.output(ctx, "user", "list", "--network", "--login__in="+strings.Join(logins, ","), "--field=ID")
	if err != nil {
		return nil, true, fmt.Errorf("resolve %s multisite super admins before anonymization: %w", label, err)
	}
	ids := []int{}
	for _, line := range nonEmptyLines(raw) {
		id, err := strconv.Atoi(line)
		if err != nil {
			return nil, true, fmt.Errorf("resolve %s multisite super admins before anonymization: unexpected user ID %q", label, line)
		}
		ids = append(ids, id)
	}
	if len(ids) < len(logins) {
		a.UI.Warning("Skipping %d %s super admin login(s) that do not match an existing user.", len(logins)-len(ids), label)
	}
	return ids, true, nil
}

func nonEmptyLines(output string) []string {
	lines := []string{}
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// superAdminLoginsJSON renders the post-rename login list for `wp site option update --format=json`.
func superAdminLoginsJSON(ids []int) string {
	logins := make([]string, 0, len(ids))
	for _, id := range ids {
		logins = append(logins, fmt.Sprintf("user-%d", id))
	}
	encoded, err := json.Marshal(logins)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// remoteMariaDBCompatibilityForDatabaseCommand reuses the preflight result when the same
// process ran it and probes the host once otherwise, so DDEV post-push hooks running in a
// fresh process still get the temporary mysql/mysqldump aliases on Netcup-style hosts.
func (a *App) remoteMariaDBCompatibilityForDatabaseCommand(ctx context.Context, projectRoot string, target RemoteTarget) bool {
	if required, ok := a.remoteMariaDBCompatibility[remoteTargetKey(target)]; ok {
		return required
	}
	required, err := a.detectRemoteMariaDBCompatibility(ctx, projectRoot, target)
	if err != nil {
		required = false
	}
	a.setRemoteMariaDBCompatibility(target, required)
	return required
}

// remoteWPDatabaseCommand wraps a remote WP-CLI database command in the temporary MariaDB
// client compatibility shim used by dbPush, including cleanup on failure. Without the shim
// it is the plain remote WP-CLI command.
func remoteWPDatabaseCommand(target RemoteTarget, id string, mariaDBCompat bool, args ...string) string {
	command := remoteWPCommand(target, args...)
	if !mariaDBCompat {
		return command
	}
	remoteTmp := trimTrailingSlash(defaultString(target.RemoteTmpDir, "/tmp"))
	mariaDBSetup, mariaDBCleanup := remoteMariaDBCompatibilityCommands(remoteTmp, id, true)
	return strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { %s; };", mariaDBCleanup),
		"trap cleanup INT TERM HUP EXIT;",
		mariaDBSetup,
		command + ";",
		mariaDBCleanup + ";",
		"trap - EXIT",
	}, " ")
}
