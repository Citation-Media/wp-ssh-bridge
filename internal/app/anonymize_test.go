package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWPScript builds a fake `wp` that logs every call to $WP_FAKE_LOG and answers the
// anonymization probes for a regular single-site install. Override arms are matched
// before the defaults, so a test only spells out what differs.
func fakeWPScript(overrides ...string) string {
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WP_FAKE_LOG\"\ncase \"$*\" in\n"
	for _, override := range overrides {
		script += "  " + override + "\n"
	}
	return script + `  *"db prefix"*) printf 'wp_\n'; exit 0 ;;
  *"db tables"*) printf 'wp_commentmeta,wp_comments,wp_options,wp_usermeta,wp_users\n'; exit 0 ;;
  *"db query"*) printf 'Success: Query succeeded. Rows affected: -1\n'; exit 0 ;;
  *"core is-installed --network"*) exit 1 ;;
  *"config get MULTISITE"*) exit 1 ;;
  *"cache flush"*) exit 0 ;;
esac
exit 1
`
}

func installFakeWP(t *testing.T, dir string, script string) string {
	t.Helper()
	logPath := filepath.Join(dir, "wp.log")
	t.Setenv("WP_FAKE_LOG", logPath)
	installFakeCommand(t, dir, "wp", script)
	return logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// assertLogOrder fails unless every fragment appears in log, in the given order.
func assertLogOrder(t *testing.T, log string, fragments ...string) {
	t.Helper()
	last := -1
	for _, fragment := range fragments {
		index := strings.Index(log, fragment)
		if index < 0 {
			t.Fatalf("log missing %q:\n%s", fragment, log)
		}
		if index < last {
			t.Fatalf("log fragment %q appears out of order:\n%s", fragment, log)
		}
		last = index
	}
}

func TestAnonymizeEmailDomainPrefersConfiguredThenDestinationHost(t *testing.T) {
	t.Parallel()
	if got := anonymizeEmailDomain("dev.example.test", "https://site.ddev.site"); got != "dev.example.test" {
		t.Fatalf("configured domain ignored: %q", got)
	}
	if got := anonymizeEmailDomain("", "https://site.ddev.site:8443/blog"); got != "site.ddev.site" {
		t.Fatalf("destination host not derived: %q", got)
	}
	if got := anonymizeEmailDomain("", "staging.example.com"); got != "staging.example.com" {
		t.Fatalf("protocol-less destination host not derived: %q", got)
	}
	for _, destination := range []string{"", "http://localhost:8080", "not a url", "https://[::1]/"} {
		if got := anonymizeEmailDomain("", destination); got != anonymizeFallbackDomain {
			t.Fatalf("anonymizeEmailDomain(%q) = %q, want fallback %q", destination, got, anonymizeFallbackDomain)
		}
	}
}

func TestValidateAnonymizeRejectsUnsafeDomain(t *testing.T) {
	t.Parallel()
	if err := (Config{}).validateAnonymize(); err != nil {
		t.Fatalf("empty domain rejected: %v", err)
	}
	if err := (Config{AnonymizeEmailDomain: "site.ddev.site"}).validateAnonymize(); err != nil {
		t.Fatalf("safe domain rejected: %v", err)
	}
	for _, domain := range []string{"bad domain", "x'; DROP TABLE wp_users; --", "-leading.example", "trailing.example-", "user@example.com"} {
		if err := (Config{AnonymizeEmailDomain: domain}).validateAnonymize(); err == nil {
			t.Fatalf("validateAnonymize(%q) accepted unsafe domain", domain)
		}
	}
}

func TestAnonymizeUsersSQLRewritesIdentityProfileAndWooCommerceFields(t *testing.T) {
	t.Parallel()
	sql := anonymizeUsersSQL("wp_", "site.ddev.site", "abcd1234")
	for _, want := range []string{
		"START TRANSACTION;",
		"UPDATE `wp_users` SET user_login = CONCAT('wp-ssh-tmp-abcd1234-', ID);",
		"UPDATE `wp_users` SET user_login = CONCAT('user-', ID), user_nicename = CONCAT('user-', ID), user_email = CONCAT('user-', ID, '@site.ddev.site'), display_name = CONCAT('User ', ID), user_url = '', user_activation_key = '';",
		"UPDATE `wp_usermeta` SET meta_value = CASE meta_key WHEN 'first_name' THEN 'User' WHEN 'last_name' THEN CAST(user_id AS CHAR) WHEN 'nickname' THEN CONCAT('user-', user_id) WHEN 'description' THEN 'Anonymized user profile.' WHEN 'billing_email' THEN CONCAT('user-', user_id, '@site.ddev.site')",
		"WHEN 'billing_first_name' THEN 'User' WHEN 'billing_last_name' THEN CAST(user_id AS CHAR) WHEN 'billing_company' THEN '' WHEN 'billing_address_1' THEN 'Example Street 1' WHEN 'billing_address_2' THEN '' WHEN 'billing_city' THEN 'Example City' WHEN 'billing_postcode' THEN '12345' WHEN 'billing_phone' THEN ''",
		"WHEN 'shipping_first_name' THEN 'User' WHEN 'shipping_last_name' THEN CAST(user_id AS CHAR) WHEN 'shipping_company' THEN '' WHEN 'shipping_address_1' THEN 'Example Street 1' WHEN 'shipping_address_2' THEN '' WHEN 'shipping_city' THEN 'Example City' WHEN 'shipping_postcode' THEN '12345' WHEN 'shipping_phone' THEN ''",
		"END WHERE meta_key IN ('first_name', 'last_name', 'nickname', 'description', 'billing_email', 'billing_first_name',",
		"'shipping_phone');",
		"DELETE FROM `wp_usermeta` WHERE meta_key = 'session_tokens'; COMMIT;",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
	assertLogOrder(t, sql, "wp-ssh-tmp-", "user_login = CONCAT('user-', ID)")
	for _, untouched := range []string{"user_pass", "billing_country", "billing_state", "shipping_country", "shipping_state", "SELECT"} {
		if strings.Contains(sql, untouched) {
			t.Fatalf("SQL unexpectedly touches %s:\n%s", untouched, sql)
		}
	}
}

func TestCommentTableNamesSelectsPerSiteTables(t *testing.T) {
	t.Parallel()
	tables := commentTableNames("wp_", parseTableList("wp_commentmeta,wp_comments,wp_2_comments,wp_2_commentmeta,wp_3_comments,wp_old_comments,wp_wc_comments,wpx_comments,wp_posts\n"))
	want := []commentTable{
		{name: "wp_comments", hasMeta: true},
		{name: "wp_2_comments", hasMeta: true},
		{name: "wp_3_comments", hasMeta: false},
	}
	if len(tables) != len(want) {
		t.Fatalf("commentTableNames() = %#v, want %#v", tables, want)
	}
	for index := range want {
		if tables[index] != want[index] {
			t.Fatalf("commentTableNames()[%d] = %#v, want %#v", index, tables[index], want[index])
		}
	}
	if got := want[1].metaName(); got != "wp_2_commentmeta" {
		t.Fatalf("metaName() = %q", got)
	}

	if tables := commentTableNames("wp_", parseTableList("wp_options\nwp_posts\n")); len(tables) != 0 {
		t.Fatalf("commentTableNames() found tables without comments: %#v", tables)
	}
}

func TestAnonymizeCommentsSQLRewritesAuthorsAndContent(t *testing.T) {
	t.Parallel()
	sql := anonymizeCommentsSQL([]commentTable{{name: "wp_comments", hasMeta: true}, {name: "wp_2_comments"}}, "site.ddev.site")
	for _, want := range []string{
		"START TRANSACTION;",
		"UPDATE `wp_comments` SET comment_author = CASE WHEN user_id > 0 THEN CONCAT('User ', user_id) ELSE CONCAT('Guest ', comment_ID) END, comment_author_email = CASE WHEN user_id > 0 THEN CONCAT('user-', user_id, '@site.ddev.site') ELSE CONCAT('guest-', comment_ID, '@site.ddev.site') END, comment_author_url = '', comment_author_IP = '', comment_agent = '', comment_content = CONCAT('Anonymized comment ', comment_ID, '.');",
		"DELETE FROM `wp_commentmeta` WHERE meta_key LIKE 'akismet_%';",
		"UPDATE `wp_2_comments` SET comment_author = CASE",
		"COMMIT;",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("SQL missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "wp_2_commentmeta") {
		t.Fatalf("SQL touches a commentmeta table that does not exist:\n%s", sql)
	}
	for _, untouched := range []string{"comment_date", "comment_approved", "comment_type", "comment_post_ID", "comment_parent", "SELECT"} {
		if strings.Contains(sql, untouched) {
			t.Fatalf("SQL unexpectedly touches %s:\n%s", untouched, sql)
		}
	}
	if got := anonymizeCommentsDoneMessage(2); got != "Comment records anonymized in 2 tables" {
		t.Fatalf("done message = %q", got)
	}
	if got := anonymizeCommentsDoneMessage(1); got != "Comment records anonymized in 1 table" {
		t.Fatalf("done message for one table = %q", got)
	}
}

func TestAnonymizeUserMetaRulesCoverProfileAndAddressKeys(t *testing.T) {
	t.Parallel()
	keys := map[string]bool{}
	for _, rule := range anonymizeUserMetaRules("site.ddev.site") {
		if keys[rule.key] {
			t.Fatalf("duplicate meta rule for %q", rule.key)
		}
		keys[rule.key] = true
	}
	for _, want := range []string{"first_name", "last_name", "nickname", "description", "billing_email", "billing_address_1", "shipping_address_1", "shipping_phone"} {
		if !keys[want] {
			t.Fatalf("meta rules missing %q", want)
		}
	}
}

func TestAnonymizeMessagesAndHelpers(t *testing.T) {
	t.Parallel()
	if got := anonymizeUsersDoneMessage("site.ddev.site"); got != "User records anonymized (user-<ID>@site.ddev.site)" {
		t.Fatalf("done message = %q", got)
	}
	if got := strings.Join(nonEmptyLines("\nuser-1\n  user-2 \n\n"), ","); got != "user-1,user-2" {
		t.Fatalf("nonEmptyLines() = %q", got)
	}
	if got := superAdminLoginsJSON([]int{1, 7}); got != `["user-1","user-7"]` {
		t.Fatalf("superAdminLoginsJSON() = %q", got)
	}
	if got := superAdminLoginsJSON(nil); got != `[]` {
		t.Fatalf("superAdminLoginsJSON(nil) = %q", got)
	}
}

func TestAnonymizeLocalDataRunsBulkSQLAndFlushesCache(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakeWPScript())
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{LocalURL: "https://site.ddev.site"}
	if err := app.anonymizeLocalData(context.Background(), dir, cfg, preferredLocalURL(dir, cfg)); err != nil {
		t.Fatalf("anonymizeLocalData() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"User records anonymized (user-<ID>@site.ddev.site)",
		"Comment records anonymized in 1 table",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing success message %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}

	log := readLog(t, logPath)
	assertLogOrder(t, log,
		"db prefix",
		"db query START TRANSACTION; UPDATE `wp_users` SET user_login = CONCAT('wp-ssh-tmp-",
		"user_email = CONCAT('user-', ID, '@site.ddev.site'), display_name = CONCAT('User ', ID)",
		"DELETE FROM `wp_usermeta` WHERE meta_key = 'session_tokens'; COMMIT;",
		"db tables --all-tables-with-prefix --format=csv",
		"db query START TRANSACTION; UPDATE `wp_comments` SET comment_author = CASE",
		"DELETE FROM `wp_commentmeta` WHERE meta_key LIKE 'akismet_%'; COMMIT;",
		"cache flush",
	)
	if strings.Contains(log, "site option update") {
		t.Fatalf("single-site install must not rewrite site_admins:\n%s", log)
	}
}

func TestAnonymizeLocalDataRestoresMultisiteSuperAdmins(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakeWPScript(
		`*"db tables"*) printf 'wp_2_commentmeta,wp_2_comments,wp_commentmeta,wp_comments,wp_users\n'; exit 0 ;;`,
		`*"core is-installed --network"*) exit 0 ;;`,
		`*"super-admin list"*) printf '\nadmin\njane.doe\nghost\n'; exit 0 ;;`,
		`*"user list --network --login__in=admin,jane.doe,ghost --field=ID"*) printf '1\n7\n'; exit 0 ;;`,
		`*"site option update site_admins"*) exit 0 ;;`,
	))
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	if err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site"); err != nil {
		t.Fatalf("anonymizeLocalData() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	log := readLog(t, logPath)
	assertLogOrder(t, log,
		"super-admin list",
		"user list --network --login__in=admin,jane.doe,ghost --field=ID",
		"db query",
		`site option update site_admins ["user-1","user-7"] --format=json`,
		// Comment tables follow the `wp db tables` listing order.
		"UPDATE `wp_2_comments` SET comment_author",
		"DELETE FROM `wp_2_commentmeta` WHERE meta_key LIKE 'akismet_%'",
		"UPDATE `wp_comments` SET comment_author",
	)
	if !strings.Contains(stdout.String(), "Comment records anonymized in 2 tables") {
		t.Fatalf("missing multisite comment success message:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "ghost") {
		t.Fatalf("warning must not print the original super admin login:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Skipping 1 local super admin login(s) that do not match an existing user.") {
		t.Fatalf("missing warning for stale super admin entry:\n%s", stderr.String())
	}
}

func TestAnonymizeLocalDataFailsWhenSuperAdminRestoreFails(t *testing.T) {
	dir := t.TempDir()
	installFakeWP(t, dir, fakeWPScript(
		`*"core is-installed --network"*) exit 0 ;;`,
		`*"super-admin list"*) printf 'admin\n'; exit 0 ;;`,
		`*"user list --network --login__in=admin --field=ID"*) printf '1\n'; exit 0 ;;`,
		`*"site option update site_admins"*) echo "Error: could not update" >&2; exit 1 ;;`,
	))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site")
	if err == nil {
		t.Fatal("anonymizeLocalData() ignored a failed super admin restore")
	}
	if !strings.Contains(err.Error(), "restore local multisite super admins") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnonymizeLocalDataFailsWhenSuperAdminLookupFails(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakeWPScript(
		`*"core is-installed --network"*) exit 0 ;;`,
		`*"super-admin list"*) printf 'admin\n'; exit 0 ;;`,
		`*"user list --network"*) echo "Error: boom" >&2; exit 1 ;;`,
	))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site")
	if err == nil {
		t.Fatal("anonymizeLocalData() ignored a failed super admin lookup")
	}
	if !strings.Contains(err.Error(), "resolve local multisite super admins") {
		t.Fatalf("unexpected error: %v", err)
	}
	if log := readLog(t, logPath); strings.Contains(log, "db query") {
		t.Fatalf("SQL ran although super admins could not be resolved:\n%s", log)
	}
}

func TestAnonymizeLocalDataUsesConfiguredDomainOverDestination(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakeWPScript(
		`*"db prefix"*) printf 'custom_\n'; exit 0 ;;`,
		`*"db tables"*) printf 'custom_comments\n'; exit 0 ;;`,
	))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	cfg := Config{AnonymizeEmailDomain: "dev.example.test"}
	if err := app.anonymizeLocalData(context.Background(), dir, cfg, "https://site.ddev.site"); err != nil {
		t.Fatalf("anonymizeLocalData() error = %v", err)
	}
	assertLogOrder(t, readLog(t, logPath),
		"UPDATE `custom_users` SET user_login = CONCAT('user-', ID), user_nicename = CONCAT('user-', ID), user_email = CONCAT('user-', ID, '@dev.example.test')",
		"UPDATE `custom_comments` SET comment_author = CASE WHEN user_id > 0 THEN CONCAT('User ', user_id) ELSE CONCAT('Guest ', comment_ID) END, comment_author_email = CASE WHEN user_id > 0 THEN CONCAT('user-', user_id, '@dev.example.test')",
	)
}

func TestAnonymizeLocalDataReportsMissingCommentTables(t *testing.T) {
	dir := t.TempDir()
	installFakeWP(t, dir, fakeWPScript(`*"db tables"*) printf 'wp_options,wp_users\n'; exit 0 ;;`))
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

	if err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site"); err != nil {
		t.Fatalf("anonymizeLocalData() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "No comment tables found") {
		t.Fatalf("missing no-comment-tables message:\n%s", stdout.String())
	}
}

func TestAnonymizeLocalDataRejectsUnsafeTablePrefix(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakeWPScript(`*"db prefix"*) printf 'wp-; DROP TABLE x; --\n'; exit 0 ;;`))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.anonymizeLocalData(context.Background(), dir, Config{}, "")
	if err == nil {
		t.Fatal("anonymizeLocalData() accepted an unsafe table prefix")
	}
	if !strings.Contains(err.Error(), "not safe for SQL") {
		t.Fatalf("unexpected error: %v", err)
	}
	if log := readLog(t, logPath); strings.Contains(log, "db query") {
		t.Fatalf("SQL ran despite unsafe prefix:\n%s", log)
	}
}

func TestAnonymizeLocalDataFailsWhenSQLFails(t *testing.T) {
	dir := t.TempDir()
	installFakeWP(t, dir, fakeWPScript(`*"db query"*"user_login = CONCAT("*) echo "ERROR 1146 (42S02): Table 'db.wp_users' doesn't exist" >&2; exit 1 ;;`))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site")
	if err == nil {
		t.Fatal("anonymizeLocalData() ignored a failed SQL update")
	}
	if !strings.Contains(err.Error(), "anonymize local user records") || !strings.Contains(err.Error(), "ERROR 1146") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnonymizeLocalDataFailsWhenCommentSQLFails(t *testing.T) {
	dir := t.TempDir()
	installFakeWP(t, dir, fakeWPScript(`*"db query"*"comment_author = CASE"*) echo "ERROR 1054 (42S22): Unknown column" >&2; exit 1 ;;`))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := app.anonymizeLocalData(context.Background(), dir, Config{}, "https://site.ddev.site")
	if err == nil {
		t.Fatal("anonymizeLocalData() ignored a failed comment update")
	}
	if !strings.Contains(err.Error(), "anonymize local comment records") || !strings.Contains(err.Error(), "ERROR 1054") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakePostPullWP extends the single-site fake with the probes postPull needs.
func fakePostPullWP() string {
	return fakeWPScript(
		`*"option get home"*) printf 'https://example.com\n'; exit 0 ;;`,
		`*"plugin list"*) printf '[]\n'; exit 0 ;;`,
		`*"search-replace"*) exit 0 ;;`,
	)
}

func TestPostPullAnonymizesByDefaultBeforeURLReplacement(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakePostPullWP())
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	cfg := Config{LocalURL: "https://site.ddev.site"}
	if err := app.postPull(context.Background(), adapter, cfg, false, true); err != nil {
		t.Fatalf("postPull() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	log := readLog(t, logPath)
	assertLogOrder(t, log,
		"db query START TRANSACTION; UPDATE `wp_users`",
		"UPDATE `wp_comments`",
		"search-replace https://example.com https://site.ddev.site",
	)
	if !strings.Contains(log, "'@site.ddev.site'") {
		t.Fatalf("anonymization did not use the local site host:\n%s", log)
	}
}

func TestPostPullSkipsDatabaseStepsWithoutImport(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakePostPullWP())
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	if err := app.postPull(context.Background(), adapter, Config{LocalURL: "https://site.ddev.site"}, false, false); err != nil {
		t.Fatalf("postPull() error = %v", err)
	}
	log, _ := os.ReadFile(logPath)
	for _, unexpected := range []string{"db query", "search-replace"} {
		if strings.Contains(string(log), unexpected) {
			t.Fatalf("%s ran although no database was imported:\n%s", unexpected, log)
		}
	}
}

func TestPostPullHonorsSkipAnonymize(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakePostPullWP())
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	if err := app.postPull(context.Background(), adapter, Config{SkipAnonymize: true, SkipSearchReplace: true}, false, true); err != nil {
		t.Fatalf("postPull() error = %v", err)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "db query") {
		t.Fatalf("anonymization ran despite skip_anonymize:\n%s", log)
	}
}

func TestPostPullCloneAnonymizesOnlyWhenRequested(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeWP(t, dir, fakePostPullWP())
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	adapter := adapterForRuntime(runtimeContext{Mode: modeStandalone, Root: dir})

	if err := app.postPull(context.Background(), adapter, Config{SkipSearchReplace: true}, true, true); err != nil {
		t.Fatalf("postPull(clone) error = %v", err)
	}
	if log, _ := os.ReadFile(logPath); strings.Contains(string(log), "db query") {
		t.Fatalf("clone anonymized without clone_anonymize:\n%s", log)
	}

	cfg := Config{SkipSearchReplace: true, CloneAnonymize: true, PullDomainReplacements: []DomainReplacement{{Old: "https://example.com", New: "https://staging.example.com"}}}
	if err := app.postPull(context.Background(), adapter, cfg, true, true); err != nil {
		t.Fatalf("postPull(clone, opt-in) error = %v", err)
	}
	log := readLog(t, logPath)
	if !strings.Contains(log, "db query START TRANSACTION;") {
		t.Fatalf("clone opt-in did not anonymize:\n%s", log)
	}
	if !strings.Contains(log, "'@staging.example.com'") {
		t.Fatalf("clone anonymization did not derive the target host from pull_domain_replacements:\n%s", log)
	}
}

const fakeAnonymizeSSH = `#!/bin/sh
printf '%s\n' "$*" >> "$SSH_FAKE_LOG"
case "$*" in
  *"'db' 'prefix'"*) printf 'wp_\n'; exit 0 ;;
  *"'db' 'tables'"*) printf 'wp_commentmeta,wp_comments,wp_users\n'; exit 0 ;;
  *"'db' 'query'"*) printf 'Success: Query succeeded. Rows affected: -1\n'; exit 0 ;;
  *"'core' 'is-installed' '--network'"*) exit 1 ;;
  *"'config' 'get' 'MULTISITE'"*) exit 1 ;;
esac
exit 0
`

func installFakeAnonymizeSSH(t *testing.T, dir string) string {
	t.Helper()
	logPath := filepath.Join(dir, "ssh.log")
	t.Setenv("SSH_FAKE_LOG", logPath)
	installFakeSSH(t, dir, fakeAnonymizeSSH)
	return logPath
}

func TestPostPushAnonymizesRemoteDataOnlyWhenRequested(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeAnonymizeSSH(t, dir)
	installFakeCommand(t, dir, "wp", "#!/bin/sh\nexit 1\n")
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{
		PushUser:       "deploy",
		PushHost:       "staging.example.com",
		PushRemotePath: "/var/www/html",
		PushURL:        "https://staging.example.com",
		PushDomainReplacements: []DomainReplacement{
			{Old: "example.ddev.site", New: "staging.example.com"},
		},
	}
	if err := app.postPush(context.Background(), dir, cfg); err != nil {
		t.Fatalf("postPush() error = %v", err)
	}
	if log := readLog(t, logPath); strings.Contains(log, "'db' 'query'") {
		t.Fatalf("push anonymized without push_anonymize:\n%s", log)
	}

	cfg.PushAnonymize = true
	if err := app.postPush(context.Background(), dir, cfg); err != nil {
		t.Fatalf("postPush(opt-in) error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"User records anonymized (user-<ID>@staging.example.com)",
		"Comment records anonymized in 1 table",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("missing success message %q:\nstdout:\n%s\nstderr:\n%s", want, stdout.String(), stderr.String())
		}
	}

	log := readLog(t, logPath)
	assertLogOrder(t, log,
		"'db' 'query' 'START TRANSACTION; UPDATE `wp_users` SET user_login = CONCAT(",
		"'db' 'tables' '--all-tables-with-prefix' '--format=csv'",
		"UPDATE `wp_comments` SET comment_author",
		"'cache' 'flush'",
	)
	if strings.LastIndex(log, "'search-replace'") < strings.Index(log, "'db' 'query'") {
		t.Fatalf("remote URL replacement ran before anonymization:\n%s", log)
	}
	if !strings.Contains(log, "@staging.example.com") {
		t.Fatalf("ssh log missing pseudonymized domain:\n%s", log)
	}
}

func TestPostPushAnonymizesEvenWhenSearchReplaceSkipped(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeAnonymizeSSH(t, dir)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	cfg := Config{
		PushUser:          "deploy",
		PushHost:          "staging.example.com",
		PushRemotePath:    "/var/www/html",
		PushAnonymize:     true,
		SkipSearchReplace: true,
	}
	if err := app.postPush(context.Background(), dir, cfg); err != nil {
		t.Fatalf("postPush() error = %v", err)
	}
	log := readLog(t, logPath)
	if !strings.Contains(log, "'db' 'query'") {
		t.Fatalf("ssh log missing anonymization:\n%s", log)
	}
	if !strings.Contains(log, "@"+anonymizeFallbackDomain) {
		t.Fatalf("anonymization without a push URL should use the fallback domain:\n%s", log)
	}
	if strings.Contains(log, "'search-replace'") {
		t.Fatalf("search-replace ran despite skip_search_replace:\n%s", log)
	}
}

func TestPostPushUsesMariaDBShimForRemoteQueriesWhenRequired(t *testing.T) {
	dir := t.TempDir()
	logPath := installFakeAnonymizeSSH(t, dir)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "staging.example.com", RemotePath: "/var/www/html", RemoteTmpDir: "/tmp"}
	app.setRemoteMariaDBCompatibility(target, true)

	cfg := Config{PushUser: "deploy", PushHost: "staging.example.com", PushRemotePath: "/var/www/html", PushAnonymize: true, SkipSearchReplace: true}
	if err := app.postPush(context.Background(), dir, cfg); err != nil {
		t.Fatalf("postPush() error = %v", err)
	}
	log := readLog(t, logPath)
	for _, want := range []string{"trap cleanup INT TERM HUP EXIT;", "WP_SSH_MARIADB_COMPAT_DIR='/tmp/.wp-ssh-bridge-mariadb-"} {
		if !strings.Contains(log, want) {
			t.Fatalf("ssh log missing MariaDB shim %q:\n%s", want, log)
		}
	}
}

func TestRemoteWPDatabaseCommandWrapsMariaDBCompatibility(t *testing.T) {
	t.Parallel()
	target := RemoteTarget{RemotePath: "/var/www/html", RemoteTmpDir: "/var/tmp"}
	command := remoteWPDatabaseCommand(target, "abc123", true, "db", "query", "SELECT 1")
	for _, want := range []string{
		"set -eu;",
		"cleanup() { if [ \"${WP_SSH_MARIADB_COMPAT_CREATED:-0}\" = 1 ]; then",
		"trap cleanup INT TERM HUP EXIT;",
		"WP_SSH_MARIADB_COMPAT_DIR='/var/tmp/.wp-ssh-bridge-mariadb-abc123';",
		"cd '/var/www/html';",
		"wp_ssh_wp '--allow-root' '--skip-plugins' '--skip-themes' 'db' 'query' 'SELECT 1';",
		"trap - EXIT",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote database command missing %q:\n%s", want, command)
		}
	}
	assertLogOrder(t, command, "WP_SSH_MARIADB_COMPAT_DIR=", "wp_ssh_wp '--allow-root'", "trap - EXIT")

	plain := remoteWPDatabaseCommand(target, "abc123", false, "db", "query", "SELECT 1")
	if want := remoteWPCommand(target, "db", "query", "SELECT 1"); plain != want {
		t.Fatalf("remote database command without shim = %q, want plain remote WP-CLI command %q", plain, want)
	}
}

func TestRemoteMariaDBCompatibilityForDatabaseCommandProbesUnknownTargets(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
printf 'required\n'
`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}

	if !app.remoteMariaDBCompatibilityForDatabaseCommand(context.Background(), dir, target) {
		t.Fatal("expected MariaDB compatibility to be detected")
	}
	if !app.remoteMariaDBCompatibilityForDatabaseCommand(context.Background(), dir, target) {
		t.Fatal("expected cached MariaDB compatibility result")
	}
	if got := strings.Count(readLog(t, logPath), "mysql --version"); got != 1 {
		t.Fatalf("expected one MariaDB probe, got %d", got)
	}

	app.setRemoteMariaDBCompatibility(target, false)
	if app.remoteMariaDBCompatibilityForDatabaseCommand(context.Background(), dir, target) {
		t.Fatal("expected preflight result to take precedence over probing")
	}
}

func TestRemoteMariaDBCompatibilityForDatabaseCommandCachesFailedProbe(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$*" >> `+shellQuote(logPath)+`
exit 1
`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}

	for range 2 {
		if app.remoteMariaDBCompatibilityForDatabaseCommand(context.Background(), dir, target) {
			t.Fatal("failed probe must not enable the shim")
		}
	}
	if got := strings.Count(readLog(t, logPath), "mysql --version"); got != 1 {
		t.Fatalf("failed probe should be cached, got %d probes", got)
	}
}

func TestConfigOptionsApplyAnonymizeFlagsPerCommand(t *testing.T) {
	t.Parallel()
	base := Config{SkipAnonymize: true, AnonymizeEmailDomain: "keep.example.test"}
	parse := func(args ...string) configOptions {
		t.Helper()
		opts, err := parseConfigCommand("pull", args, new(strings.Builder))
		if err != nil {
			t.Fatalf("parseConfigCommand() error = %v", err)
		}
		return opts
	}

	cfg := parse("--anonymize").apply(base)
	if cfg.SkipAnonymize || cfg.CloneAnonymize || cfg.PushAnonymize {
		t.Fatalf("--anonymize should only lift the skip: %#v", cfg)
	}
	cfg = parse("--anonymize").applyAnonymizeFor(base, anonymizeClone)
	if !cfg.CloneAnonymize || cfg.PushAnonymize {
		t.Fatalf("applyAnonymizeFor(clone) applied wrong keys: %#v", cfg)
	}
	cfg = parse("--anonymize").applyAnonymizeFor(base, anonymizePush)
	if cfg.CloneAnonymize || !cfg.PushAnonymize {
		t.Fatalf("applyAnonymizeFor(push) applied wrong keys: %#v", cfg)
	}
	cfg = parse("--anonymize").applyAnonymizeFor(base, anonymizeClone, anonymizePush)
	if !cfg.CloneAnonymize || !cfg.PushAnonymize || cfg.AnonymizeEmailDomain != "keep.example.test" {
		t.Fatalf("applyAnonymizeFor(clone, push) applied wrong keys: %#v", cfg)
	}
	cfg = parse().applyAnonymizeFor(Config{}, anonymizePush)
	if cfg.PushAnonymize {
		t.Fatalf("applyAnonymizeFor() enabled push without --anonymize: %#v", cfg)
	}
	cfg = parse("--skip-anonymize").apply(Config{})
	if !cfg.SkipAnonymize {
		t.Fatalf("--skip-anonymize not applied: %#v", cfg)
	}
	cfg = (configOptions{}).apply(Config{PushAnonymize: true})
	if !cfg.PushAnonymize || cfg.SkipAnonymize {
		t.Fatalf("apply() without flags changed configured values: %#v", cfg)
	}

	if _, err := parseConfigCommand("pull", []string{"--anonymize", "--skip-anonymize"}, new(strings.Builder)); err == nil {
		t.Fatal("parseConfigCommand() accepted contradictory anonymize flags")
	}
	if err := (configOptions{Anonymize: true}).rejectOperationFlags("init"); err != nil {
		t.Fatalf("rejectOperationFlags(init) rejected --anonymize: %v", err)
	}
	// The email domain is a config-only detail and intentionally has no flag.
	for _, command := range []string{"init", "pull", "clone", "push"} {
		if _, err := parseConfigCommand(command, []string{"--anonymize-email-domain", "dev.example.test"}, new(strings.Builder)); err == nil {
			t.Fatalf("parseConfigCommand(%s) accepted --anonymize-email-domain", command)
		}
	}
}

func TestPullRejectsUnsafeAnonymizeDomainBeforeConnecting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".wp-ssh.yaml"), []byte("anonymize_email_domain: \"bad domain\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.WorkDir = dir

	err := app.commandPull([]string{
		"--silent",
		"--user", "deploy",
		"--host", "source.example.com",
		"--remote-path", "/var/www/html",
	})
	if err == nil {
		t.Fatal("commandPull() accepted an unsafe anonymize_email_domain")
	}
	if !strings.Contains(err.Error(), "anonymize_email_domain must be a hostname") {
		t.Fatalf("unexpected error: %v", err)
	}
}
