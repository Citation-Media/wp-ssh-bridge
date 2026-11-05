package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var notesTable = createTable("wp_notes", "id", "body")

func noteRows(bodies ...string) string {
	rows := make([]string, 0, len(bodies))
	for i, body := range bodies {
		rows = append(rows, "("+string(rune('1'+i))+",'"+string(appendSQLEscaped(nil, []byte(body)))+"')")
	}
	return notesTable + "INSERT INTO `wp_notes` VALUES " + strings.Join(rows, ",") + ";\n"
}

func TestFilterFindsNamesHoweverTheyAreWritten(t *testing.T) {
	for _, body := range []string{
		"an Anna Real", "an anna real", "an ANNA REAL", "an Anna  Real", "an anna   real", "an Anna\tReal",
		"an Anna\nReal", "an Anna\r\nReal", "an Anna\u00a0Real", "an Anna&nbsp;Real", "an anna real.", "an anna real's",
		"(anna real)", "<b>anna real</b>", `{"m":"an anna real"}`, `{"m":"an Anna\nReal"}`, `a:1:{s:1:"m";s:12:"von anna real";}`,
	} {
		output := requireFiltered(t, Config{}, usersDump(noteRows(body)))
		statement := output[strings.Index(output, "INSERT INTO `wp_notes`"):]
		assertContains(t, statement, "User 5")
		assertOmits(t, statement, "Real", "real", "REAL")
	}
}

// From an independent review: each of these stayed real.
func TestFilterFindsNamesNextToTypography(t *testing.T) {
	for _, body := range []string{
		"Frau\u00a0Anna Real kam", "\u201cAnna Real\u201d", "\u201eanna real\u201c", "\u00abAnna Real\u00bb", "\u2014Anna Real", "Anna Real\u2014", "Anna Real\u2026", "Anna Real\u2019s", "Anna Real\u2019",
	} {
		output := requireFiltered(t, Config{}, usersDump(noteRows(body)))
		statement := output[strings.Index(output, "INSERT INTO `wp_notes`"):]
		assertContains(t, statement, "User 5")
		assertOmits(t, statement, "Real", "real")
	}
}

func TestFilterLearnsNamesInEveryForm(t *testing.T) {
	for name, body := range map[string]string{
		"Anna Real ":                  "an anna real",
		"Anna Real\n":                 "an anna real",
		" Anna Real":                  "an anna real",
		"Robert Downey Jr.":           "an robert downey jr.",
		"Hans M.":                     "an Hans M. und",
		"Maria del Carmen Lopez Ruiz": "an maria del carmen lopez ruiz",
		"ŁUKASZ ŚLIWA":                "an łukasz śliwa",
		"Иван Петров":                 "an ИВАН ПЕТРОВ",
		"Örjan Svensson":              "an örjan svensson",
		strings.Repeat("a", 47) + " " + strings.Repeat("b", 48): "an " + strings.Repeat("a", 47) + " " + strings.Repeat("b", 48),
	} {
		row := "(5,'a','x','a','a@real.test','','2020','',0,'" + string(appendSQLEscaped(nil, []byte(name))) + "')"
		output := requireFiltered(t, Config{}, "-- MariaDB dump\n"+usersTable+"INSERT INTO `wp_users` VALUES "+row+";\n"+noteRows(body)+"-- Dump completed\n")
		assertContains(t, output[strings.Index(output, "INSERT INTO `wp_notes`"):], "User 5")
	}
}

func TestFilterLetsTheAddressScanHaveTheNamePartOfAnAddress(t *testing.T) {
	for _, body := range []string{"Anna Real@corp.test", "anna real@corp.test", "Anna Real (at) corp.test"} {
		output := requireFiltered(t, Config{}, usersDump(noteRows(body)))
		statement := output[strings.Index(output, "INSERT INTO `wp_notes`"):]
		assertOmits(t, statement, "corp.test")
	}
}

func TestFilterLeavesWordsThatOnlyResembleAName(t *testing.T) {
	for _, body := range []string{"anna", "real", "annareal", "anna-real", "anna_real", "real anna", "anna realistic", "xanna real", "anna\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\nreal"} {
		output := requireFiltered(t, Config{}, usersDump(noteRows(body)))
		statement := output[strings.Index(output, "INSERT INTO `wp_notes`"):]
		if strings.Contains(statement, "User 5") {
			t.Errorf("%q was replaced: %s", body, statement)
		}
	}
}

func TestFilterStaysLinearOnNameShapedValues(t *testing.T) {
	for _, unit := range []string{"anna ", "anna  ", "a-", "a.", "anna-", "anna\n", "anna&nbsp;", "anna\u00a0", "\\na", "a'"} {
		value := strings.Repeat(unit, (1<<20)/len(unit))
		if _, _, err := runFilter(t, Config{}, usersDump(noteRows(value))); err != nil {
			t.Fatalf("%q: %v", unit, err)
		}
	}
}

func TestFilterLearnsNamesFromEveryKindOfPerson(t *testing.T) {
	comments := createTable("wp_comments", "comment_ID", "comment_author", "comment_author_email", "comment_content", "comment_type", "user_id")
	customers := createTable("wp_wc_customer_lookup", "customer_id", "user_id", "username", "first_name", "last_name", "email")
	crm := createTable("wp_crm", "id", "first_name", "last_name", "email", "message")
	forms := createTable("wp_form_entries", "id", "entry_id", "meta_key", "meta_value")
	dump := usersDump(customers + "INSERT INTO `wp_wc_customer_lookup` VALUES (1,0,'','Kurt','Kunde','kurt@real.test');\n" +
		comments + "INSERT INTO `wp_comments` VALUES (1,'Karla Kommentar','karla@real.test','Hallo','comment',0),(2,'Mein Blogtitel','','Ping','pingback',0);\n" +
		crm + "INSERT INTO `wp_crm` VALUES (1,'Carl','Crm','carl@real.test','Gruss, carl crm');\n" +
		forms + "INSERT INTO `wp_form_entries` VALUES (1,9,'first_name','Fritz'),(2,9,'last_name','Formular'),(3,10,'full_name','Frieda Formular');\n" +
		noteRows("kurt kunde, Karla Kommentar, CARL CRM, fritz formular, frieda formular, mein blogtitel"))
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "Kunde", "kunde", "Kommentar", "Crm", "CRM", "crm'", "carl crm", "ormular")
	assertContains(t, output, "mein blogtitel")
}

func TestFilterProtectsWhatTheSiteNeeds(t *testing.T) {
	posts := createTable("wp_posts", "ID", "post_title", "post_name", "guid", "post_type", "post_status", "post_excerpt", "post_content")
	postmeta := createTable("wp_postmeta", "meta_id", "post_id", "meta_key", "meta_value")
	options := createTable("wp_options", "option_id", "option_name", "option_value", "autoload")
	lookup := createTable("wp_wc_product_meta_lookup", "product_id", "sku", "global_unique_id")
	dump := usersDump(options + "INSERT INTO `wp_options` VALUES (1,'siteurl','http://93.184.216.34','yes'),(2,'db_version','0123-456789','yes'),(3,'woocommerce_store_address','Anna Real 1','yes'),(4,'other','0123-456789','yes'),(5,'blogdescription','hello stranger@real.test','yes');\n" +
		posts + "INSERT INTO `wp_posts` VALUES (1,'T','0170-1234567','http://93.184.216.34/?p=1','product','publish','','');\n" +
		postmeta + "INSERT INTO `wp_postmeta` VALUES (1,1,'_sku','0123-456789'),(2,1,'_price','0123-456789'),(3,1,'_wp_attached_file','2026/anna real.jpg'),(4,1,'_sku','stranger@real.test'),(5,1,'unknown','0123-456789');\n" +
		lookup + "INSERT INTO `wp_wc_product_meta_lookup` VALUES (1,'0123-456789','4111111111111111');\n")
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output,
		"'siteurl','http://93.184.216.34'", "'db_version','0123-456789'", "'woocommerce_store_address','Anna Real 1'",
		"(1,'T','0170-1234567','http://93.184.216.34/?p=1','product'", "'_sku','0123-456789'", "'_wp_attached_file','2026/anna real.jpg'",
		"(1,'0123-456789','4111111111111111')",
		"'other','+00 000 0000000'", "'_price','+00 000 0000000'", "'unknown','+00 000 0000000'",
	)
	// The one thing no protection covers.
	assertOmits(t, output, "stranger@real.test")
}

func TestFilterLeavesTheSitesOwnNamesInFreeText(t *testing.T) {
	terms := createTable("wp_terms", "term_id", "name", "slug", "term_group")
	options := createTable("wp_options", "option_id", "option_name", "option_value", "autoload")
	dump := "-- MariaDB dump\n" + usersTable +
		"INSERT INTO `wp_users` VALUES (5,'a','x','a','a@real.test','','2020','',0,'Paul Smith'),(6,'b','x','b','b@real.test','','2020','',0,'Citation Media'),(7,'c','x','c','c@real.test','','2020','',0,'Anna Real');\n" +
		terms + "INSERT INTO `wp_terms` VALUES (1,'Paul Smith','paul-smith',0);\n" +
		options + "INSERT INTO `wp_options` VALUES (1,'blogname','Citation Media','yes');\n" +
		noteRows("Paul Smith, citation media, Anna Real") + "-- Dump completed\n"
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output, "'Paul Smith, citation media, User 7'", "(5,'user-5','!pseudonymized','user-5','user-5@example.invalid','','2020','',0,'User 5')")
}

// Both came from comparing a real WooCommerce and EDD database with its pull.
func TestFilterKeepsSettingsThatOnlyResemblePersonalData(t *testing.T) {
	options := createTable("wp_options", "option_id", "option_name", "option_value", "autoload")
	templates := createTable("wp_plugin_templates", "id", "email_id", "sender", "recipient", "name")
	emails := createTable("wp_edd_emails", "id", "email_id", "sender", "recipient")
	output := requireFiltered(t, Config{}, usersDump(options+"INSERT INTO `wp_options` VALUES (1,'woocommerce_registration_generate_password','yes','yes'),(2,'smtp_password','hunter2','yes');\n"+
		templates+"INSERT INTO `wp_plugin_templates` VALUES (1,'order','shop','customer','Welcome mail');\n"+
		emails+"INSERT INTO `wp_edd_emails` VALUES (1,'order','edd','customer');\n"))
	assertContains(t, output, "'woocommerce_registration_generate_password','yes'", "(2,'smtp_password','','yes')", "(1,'order','shop','customer','Welcome mail')", "(1,'order','edd','customer')")
}

func TestFilterHonorsTheProjectsKeepList(t *testing.T) {
	custom := createTable("wp_catalog", "id", "code", "other", "phone")
	forms := createTable("wp_form_entries", "id", "meta_key", "meta_value")
	postmeta := createTable("wp_postmeta", "meta_id", "post_id", "meta_key", "meta_value")
	cfg := Config{
		PseudonymizeKeepFields: "catalog.code, catalog.phone, form_entries.product_code, postmeta._my_code",
		PseudonymizeKeepText:   "Anna Real, 0800-123456",
	}
	dump := usersDump(custom + "INSERT INTO `wp_catalog` VALUES (1,'0123-456789','0123-456789','0171 1234567');\n" +
		forms + "INSERT INTO `wp_form_entries` VALUES (1,'product_code','0123-456789'),(2,'phone','0171 1234567');\n" +
		postmeta + "INSERT INTO `wp_postmeta` VALUES (1,1,'_my_code','0123-456789');\n" +
		noteRows("anna real and Anna Real, hotline 0800-123456, other 0800-123457", `{"phone":"0800-123456"}`))
	output := requireFiltered(t, cfg, dump)
	assertContains(t, output,
		"(1,'0123-456789','+00 000 0000000','0171 1234567')", "(1,'product_code','0123-456789')", "(2,'phone','+00 000 0000000')",
		"(1,1,'_my_code','0123-456789')", "anna real and Anna Real, hotline 0800-123456, other +00 000 0000000", `{\"phone\":\"0800-123456\"}`,
		"user-5@example.invalid", "'User 5'",
	)
}

func TestFilterReadsPeopleFromPluginColumnsItUsedToMiss(t *testing.T) {
	members := createTable("wp_members", "id", "display_name", "vat_id", "name", "customer_mail")
	both := createTable("wp_gifts", "id", "billing_first_name", "billing_last_name", "shipping_first_name", "shipping_last_name")
	kept := createTable("wp_crm", "id", "email", "name")
	numbers := "CREATE TABLE `wp_tax_links` (\n  `id` bigint(20) NOT NULL,\n  `tax_id` bigint(20) NOT NULL\n) ENGINE=InnoDB;\n"
	cfg := Config{PseudonymizeKeepFields: "crm.email"}
	output := requireFiltered(t, cfg, usersDump(members+"INSERT INTO `wp_members` VALUES (1,'Mona Member','DE123456789','Nils Neben','nils@real.test');\n"+
		both+"INSERT INTO `wp_gifts` VALUES (1,'Bert','Billing','Sina','Shipping');\n"+
		kept+"INSERT INTO `wp_crm` VALUES (1,'carl@real.test','Carl Crm');\n"+
		numbers+"INSERT INTO `wp_tax_links` VALUES (1,12345);\n"+
		noteRows("bert billing, sina shipping")))
	assertOmits(t, output, "Mona", "DE123456789", "Neben", "Billing", "billing,", "Shipping", "shipping'", "Carl Crm", "carl@real.test")
	assertContains(t, output, "(1,12345)")
}

func TestExportOrderPutsTheInstallationsUsersFirst(t *testing.T) {
	wp := `wp_ssh_wp() { case "$*" in *"config get table_prefix"*) echo wp_ ;; *information_schema*) echo wp_crm ;; *"db query"*) printf 'wp_2_users\tBASE TABLE\nwp_crm\tBASE TABLE\nwp_posts\tBASE TABLE\nwp_users\tBASE TABLE\n' ;; *"db export"*) printf '%s\n' "$*" >&2 ;; esac; }`
	_, stderr, err := runExportCommands(t, wp)
	if err != nil {
		t.Fatalf("export: %v: %s", err, stderr)
	}
	if !strings.Contains(stderr, "--tables=wp_users,wp_2_users,") {
		t.Errorf("wp_users should come first: %s", stderr)
	}
}

func TestProjectRuleOnAKnownTableKeepsItsIdentity(t *testing.T) {
	comments := createTable("wp_comments", "comment_ID", "comment_author", "comment_author_email", "comment_content", "comment_type", "user_id", "extra")
	cfg := Config{PseudonymizeRules: []PseudonymizeRule{{Table: "comments", Columns: "extra=blank"}}}
	output := requireFiltered(t, cfg, usersDump(comments+"INSERT INTO `wp_comments` VALUES (1,'x','x@real.test','t','comment',5,'note');\n"))
	assertContains(t, output, "(1,'User 5','user-5@example.invalid'")
}

func TestOneRunCannotLiftASafeguardForGood(t *testing.T) {
	dir := t.TempDir()
	runtime := runtimeContext{Root: dir, Mode: modeStandalone}
	cfg := Config{User: "deploy", Host: "example.com", SkipPseudonymize: true, AllowPseudonymizedPush: true}
	if err := persistPromptedConfig(runtime, configOptions{}, cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := readConfigFile(configPathForRuntime(runtime, ""))
	if err != nil {
		t.Fatal(err)
	}
	if saved.User != "deploy" || saved.SkipPseudonymize || saved.AllowPseudonymizedPush {
		t.Errorf("saved = %+v", saved)
	}
}

func TestPseudonymizeSettingsDoNotStopAClone(t *testing.T) {
	broken := Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html", PseudonymizeEmailDomain: "not a domain"}
	if err := broken.validatePullRequired(); err == nil {
		t.Error("a pull should report the broken setting")
	}
	broken.SkipPseudonymize = true
	if err := broken.validatePullRequired(); err != nil {
		t.Errorf("a clone should ignore it: %v", err)
	}
}

func TestPseudonymizeFlagsOnlyApplyToTheirCommand(t *testing.T) {
	app := newApp(strings.NewReader(""), &strings.Builder{}, &strings.Builder{})
	for _, tc := range []struct {
		name string
		run  func([]string) error
		flag string
	}{
		{"pull", app.commandPull, "--allow-pseudonymized-push"},
		{"push", app.commandPush, "--skip-pseudonymize"},
		{"clone", app.commandClone, "--skip-pseudonymize"},
		{"clone", app.commandClone, "--allow-pseudonymized-push"},
	} {
		if err := tc.run([]string{tc.flag}); err == nil || !strings.Contains(err.Error(), "only applies to") {
			t.Errorf("%s %s: %v", tc.name, tc.flag, err)
		}
	}
}

func TestKeepSettingsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"address in keep text", Config{PseudonymizeKeepText: "info@shop.test"}, "cannot hold email"},
		{"address in another spelling", Config{PseudonymizeKeepText: "info(at)shop.test"}, "cannot hold email"},
		{"encoded address", Config{PseudonymizeKeepText: "info%40shop.test"}, "cannot hold email"},
		{"entity address", Config{PseudonymizeKeepText: "info&#64;shop.test"}, "cannot hold email"},
		{"no table", Config{PseudonymizeKeepFields: "sku"}, "table.column"},
		{"bad table", Config{PseudonymizeKeepFields: "a`b.sku"}, "table.column"},
		{"empty field", Config{PseudonymizeKeepFields: "postmeta."}, "table.column"},
	} {
		err := tc.cfg.validatePseudonymize()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"column with a rule", Config{PseudonymizeKeepFields: "users.user_email"}, "by rule"},
		{"key with a rule", Config{PseudonymizeKeepFields: "postmeta._billing_email"}, "by rule"},
		{"table a pull leaves out", Config{PseudonymizeKeepFields: "woocommerce_sessions.session_value"}, "leaves that whole table out"},
	} {
		_, err := buildPseudonymizeSpec(tc.cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestProtectionsDoNotCollideWithRules(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for table, columns := range spec.Protected {
		rule, ok := spec.Tables[table]
		if !ok {
			continue
		}
		for column := range columns {
			if _, ruled := rule.Columns[column]; ruled {
				t.Errorf("%s.%s is both protected and replaced by a rule", table, column)
			}
		}
	}
	for table, rule := range spec.Tables {
		if rule.Meta == nil {
			continue
		}
		for _, key := range rule.Meta.ProtectKeys {
			if _, ruled := rule.Meta.Keys[key]; ruled {
				t.Errorf("%s key %s is both protected and replaced by a rule", table, key)
			}
		}
	}
}

func TestExportOrderPutsPeopleAheadOfFreeText(t *testing.T) {
	order := pseudonymizeDumpOrder(pseudonymizeAdapters())
	position := map[string]int{}
	for i, table := range order {
		position[table] = i
	}
	sequence := []string{"users", "usermeta", "terms", "wc_customer_lookup", "edd_customers", dumpOrderPeople, "comments", "options", "posts", "postmeta"}
	for i := 1; i < len(sequence); i++ {
		if position[sequence[i-1]] >= position[sequence[i]] {
			t.Errorf("%s should come before %s in %v", sequence[i-1], sequence[i], order)
		}
	}
}

func TestSanitizeRemovesEverySecretFromWPConfig(t *testing.T) {
	input := `<?php
define( 'DB_NAME', 'prod_db' );
define('DB_PASSWORD', 'pa);ss\'word');
define(
	'DB_USER',
	'prod_user'
);
DEFINE("DB_HOST", "db.prod.test");
// define('DB_PASSWORD', 'it's-the-old-one');
define('AUTH_KEY',         'salt-one');
define('NONCE_SALT', "salt-two");
define('WP_CACHE_KEY_SALT', 'salt-three');
define('WPMS_SMTP_PASS', 'smtp-secret');
define('FTP_PASS', 'ftp-secret');
define('STRIPE_SECRET_KEY_LIVE', 'stripe-secret');
define('AWS_ACCESS_KEY_ID', 'aws-id');
define('MY_API_KEY', getenv('MY_KEY'));
define('ACF_PRO_LICENSE', 'license-secret');
define('CF_API_TOKEN', 'token-secret');
define('USE_SECRET_KEY', true);
define('KEEPME_TIMEOUT', 30);
define('WP_MEMORY_LIMIT', '256M');
$table_prefix = 'abc_';
/* That's all, stop editing! Happy publishing. */
require_once ABSPATH . 'wp-settings.php';
`
	got := sanitizeWPConfigContents(input)
	for _, secret := range []string{"prod_db", "pa);ss", "word", "prod_user", "db.prod.test", "it's-the-old-one", "salt-one", "salt-two", "salt-three", "smtp-secret", "ftp-secret", "stripe-secret", "aws-id", "getenv", "license-secret", "token-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("sanitized config still holds %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		"define('AUTH_KEY', 'wp-ssh-bridge-local-AUTH_KEY');", "define('MY_API_KEY', 'wp-ssh-bridge-local-MY_API_KEY');",
		"define('USE_SECRET_KEY', true);", "define('KEEPME_TIMEOUT', 30);", "define('WP_MEMORY_LIMIT', '256M');", "$table_prefix = 'abc_';", "require_once ABSPATH . 'wp-settings.php';",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitized config misses %q:\n%s", want, got)
		}
	}
	if again := sanitizeWPConfigContents(got); again != got {
		t.Errorf("sanitizing twice changed the file:\n%s\n---\n%s", got, again)
	}
}

// From an independent review: each of these lost a secret or broke the file.
func TestSanitizeKeepsWPConfigValidAndSecretFree(t *testing.T) {
	for _, tc := range []struct{ name, input, want, gone string }{
		{"commented define keeps the next line", "<?php\n// define('WP_DEBUG', true);\nrequire_once ABSPATH . 'wp-settings.php';\n", "\nrequire_once ABSPATH . 'wp-settings.php';\n", ""},
		{"commented password", "<?php\n// define('DB_HOST', 'it's old');\ndefine('AUTH_KEY', 'a)b');\n", "define('AUTH_KEY', 'wp-ssh-bridge-local-AUTH_KEY');", "it's old"},
		{"block comment", "<?php\n/* define('DB_PASSWORD', 'oldpass'); */\n$x = 1;\n", "$x = 1;", "oldpass"},
		{"heredoc", "<?php\ndefine('DB_PASSWORD', <<<EOT\nabc)hunter2\nEOT\n);\n$x = 1;\n", "$x = 1;", "hunter2"},
		{"body of an if", "<?php\nif ($a) define('WP_DEBUG', true); else define('WP_DEBUG', false);\n", "if ($a) define('WP_DEBUG', false); else define('WP_DEBUG', false);", ""},
		{"next line body of an if", "<?php\nif ($a)\n\tdefine('DB_HOST', 'prodhost');\n$x = 1;\n", "define('DB_HOST', 'wp-ssh-bridge-local-DB_HOST');", "prodhost"},
		{"guarded define", "<?php\ndefined('DB_HOST') || define('DB_HOST', 'prodhost');\nrequire 'x.php';\n", "defined('DB_HOST') || define('DB_HOST', 'wp-ssh-bridge-local-DB_HOST');\nrequire 'x.php';", "prodhost"},
		{"class constant call", "<?php\nConfig::define('DB_PASSWORD', 'prodpass');\n", "Config::define('DB_PASSWORD', 'wp-ssh-bridge-local-DB_PASSWORD');", "prodpass"},
		{"backslash define", "<?php\n\\define('DB_PASSWORD', 'prodpass');\n", "\\define('DB_PASSWORD', 'wp-ssh-bridge-local-DB_PASSWORD');", "prodpass"},
		{"nested define", "<?php\ndefine('A', define('MY_KEY', 'nestedsecret'));\n", "define('A', define('MY_KEY', 'wp-ssh-bridge-local-MY_KEY'));", "nestedsecret"},
		{"variable behind a define", "<?php\n$db_pass = 'varsecret';\ndefine('DB_PASSWORD', $db_pass);\n", "$db_pass = 'wp-ssh-bridge-local';", "varsecret"},
		{"const", "<?php\nconst DB_PASSWORD = 'constsecret';\n", "<?php\n", "constsecret"},
		{"secret const", "<?php\nconst MY_API_KEY = 'constsecret';\n", "const MY_API_KEY = 'wp-ssh-bridge-local-MY_API_KEY';", "constsecret"},
		{"array value", "<?php\n$redis_server = array('host' => 'h', 'auth' => 'redissecret');\n", "'auth' => 'wp-ssh-bridge-local'", "redissecret"},
		{"settings array", "<?php\ndefine('AS3CF_SETTINGS', serialize(array('access-key-id' => 'a1', 'secret-access-key' => 's1')));\n", "AS3CF_SETTINGS", "s1"},
		{"putenv", "<?php\nputenv('STRIPE_SECRET_KEY=sk_live_1');\n", "putenv('STRIPE_SECRET_KEY=wp-ssh-bridge-local');", "sk_live_1"},
		{"licence", "<?php\ndefine('WPMDB_LICENCE', 'lic1');\n", "wp-ssh-bridge-local-WPMDB_LICENCE", "lic1"},
		{"url with a password", "<?php\ndefine('DATABASE_URL', 'mysql://u:urlsecret@h/d');\n", "wp-ssh-bridge-local-DATABASE_URL", "urlsecret"},
		{"CRLF", "<?php\r\ndefine('DB_NAME', 'prod');\r\n$x = 1;\r\n", "<?php\r\n", "prod"},
		{"a word holding define", "<?php\n$undefined = 'define';\nmy_define('DB_NAME', 'keepme');\n", "my_define('DB_NAME', 'keepme');", ""},
	} {
		got := sanitizeWPConfigContents(tc.input)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: misses %q:\n%s", tc.name, tc.want, got)
		}
		if tc.gone != "" && strings.Contains(got, tc.gone) {
			t.Errorf("%s: still holds %q:\n%s", tc.name, tc.gone, got)
		}
		if again := sanitizeWPConfigContents(got); again != got {
			t.Errorf("%s: not idempotent:\n%s\n---\n%s", tc.name, got, again)
		}
	}
}

func TestPullLeavesCopiesOfTheConfigBehind(t *testing.T) {
	excludes, err := buildRsyncExcludes(t.TempDir(), Config{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(excludes, " ")
	for _, want := range []string{"wp-config.php", "wp-config.php.*", "wp-config*.bak", ".env"} {
		if !strings.Contains(joined, want) {
			t.Errorf("excludes miss %q: %s", want, joined)
		}
	}
}

func pullWPConfigWith(t *testing.T, ssh string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	installFakeSSH(t, dir, ssh)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	app := newApp(strings.NewReader(""), stdout, stderr)
	err := app.pullSanitizedWPConfig(context.Background(), dir, Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"})
	return dir, err
}

func TestPullSanitizedWPConfigWritesOnlyTheResult(t *testing.T) {
	dir, err := pullWPConfigWith(t, "#!/bin/sh\nprintf 'motd from a login script\\n\\nwp-ssh-wp-config-file\\n<?php\\ndefine(\"DB_PASSWORD\", \"prod-secret\");\\n'\n")
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, "wp-config.php"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "prod-secret") || strings.Contains(string(written), "motd") || !strings.Contains(string(written), "wp-config-ddev.php") {
		t.Errorf("wp-config.php = %s", written)
	}
}

func TestPullSanitizedWPConfigKeepsTheLocalFileWhenTheSourceHasNone(t *testing.T) {
	dir, err := pullWPConfigWith(t, "#!/bin/sh\nprintf '\\nwp-ssh-wp-config-missing\\n'\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wp-config.php")); !os.IsNotExist(err) {
		t.Errorf("a source without the file wrote one: %v", err)
	}
}

func TestPullSanitizedWPConfigFailsWhenTheSourceDoesNotAnswer(t *testing.T) {
	for _, ssh := range []string{"#!/bin/sh\nexit 255\n", "#!/bin/sh\nprintf 'something else\\n'\n"} {
		dir, err := pullWPConfigWith(t, ssh)
		if err == nil {
			t.Errorf("%q: no error", ssh)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "wp-config.php")); !os.IsNotExist(statErr) {
			t.Errorf("%q: wrote a file: %v", ssh, statErr)
		}
	}
}

func confirmingApp(input string, terminal bool) (*App, *strings.Builder) {
	stdout := &strings.Builder{}
	app := newApp(strings.NewReader(input), stdout, &strings.Builder{})
	app.stdinTerminal = func() bool { return terminal }
	return app, stdout
}

func TestRealDataNeedsAPersonAtATerminal(t *testing.T) {
	cfg := Config{SkipPseudonymize: true, User: "deploy", Host: "example.com"}

	if err := newApp(strings.NewReader("real data\n"), &strings.Builder{}, &strings.Builder{}).confirmRealDataPull(cfg); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Errorf("piped input passed the confirmation: %v", err)
	}
	app, _ := confirmingApp("real data\n", false)
	if err := app.confirmRealDataPull(cfg); err == nil {
		t.Error("no terminal passed the confirmation")
	}
	for _, answer := range []string{"", "y\n", "yes\n", "REAL DATA\n"} {
		app, _ := confirmingApp(answer, true)
		if err := app.confirmRealDataPull(cfg); err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("answer %q: %v", answer, err)
		}
	}
	app, stdout := confirmingApp("real data\n", true)
	if err := app.confirmRealDataPull(cfg); err != nil {
		t.Errorf("the right answer failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "deploy@example.com") {
		t.Errorf("the prompt should name the source: %s", stdout.String())
	}
	app, stdout = confirmingApp("", false)
	if err := app.confirmRealDataPull(Config{}); err != nil || stdout.Len() != 0 {
		t.Errorf("a pseudonymized pull asked: %v, %s", err, stdout.String())
	}
}
