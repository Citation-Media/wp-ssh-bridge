package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

// runFilter runs the filter on a dump, and reports what it counted.
func runFilter(t *testing.T, cfg Config, dump string) (string, map[string]pseudonymizeTableReport, error) {
	t.Helper()
	spec, err := buildPseudonymizeSpec(cfg)
	if err != nil {
		t.Fatalf("buildPseudonymizeSpec() error = %v", err)
	}
	filter, err := newPseudonymizeFilter(spec, "wp_", 1)
	if err != nil {
		t.Fatalf("newPseudonymizeFilter() error = %v", err)
	}
	output := bytes.Buffer{}
	report, err := filter.run(context.Background(), strings.NewReader(dump), &output)
	return output.String(), report, err
}

func createTable(name string, columns ...string) string {
	body := make([]string, 0, len(columns))
	for _, column := range columns {
		body = append(body, "  `"+column+"` text")
	}
	return "CREATE TABLE `" + name + "` (\n" + strings.Join(body, ",\n") + "\n) ENGINE=InnoDB;\n"
}

var usersTable = createTable("wp_users", "ID", "user_login", "user_pass", "user_nicename", "user_email", "user_url", "user_registered", "user_activation_key", "user_status", "display_name")

const annaRow = "(5,'anna','$P$hash','anna','Anna@Real.test','https://anna.test','2020-01-01','key',0,'Anna Real')"

func usersDump(extra string) string {
	return "-- MariaDB dump\n" + usersTable + "INSERT INTO `wp_users` VALUES " + annaRow + ";\n" + extra + "-- Dump completed on 2026-11-04\n"
}

func requireFiltered(t *testing.T, cfg Config, dump string) string {
	t.Helper()
	output, _, err := runFilter(t, cfg, dump)
	if err != nil {
		t.Fatalf("filter failed: %v", err)
	}
	return output
}

func assertContains(t *testing.T, output string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(output, want) {
			t.Errorf("output misses %q:\n%s", want, output)
		}
	}
}

func assertOmits(t *testing.T, output string, unwanted ...string) {
	t.Helper()
	for _, value := range unwanted {
		if strings.Contains(output, value) {
			t.Errorf("output still contains %q:\n%s", value, output)
		}
	}
}

func TestFilterReplacesUserIdentity(t *testing.T) {
	output := requireFiltered(t, Config{}, usersDump(""))
	assertContains(t, output, "'user-5'", "user-5@example.invalid", "'User 5'", "!pseudonymized", "'2020-01-01'")
	assertOmits(t, output, "anna", "Anna", "$P$hash", "'key'")
}

func TestFilterMergesOnePersonAcrossTables(t *testing.T) {
	comments := createTable("wp_comments", "comment_ID", "comment_post_ID", "comment_author", "comment_author_email", "comment_author_url", "comment_author_IP", "comment_date", "comment_content", "comment_agent", "user_id")
	orders := createTable("wp_wc_orders", "id", "status", "customer_id", "billing_email", "ip_address", "user_agent", "customer_note", "transaction_id")
	addresses := createTable("wp_wc_order_addresses", "id", "order_id", "address_type", "first_name", "last_name", "city", "country", "email")
	dump := usersDump(
		comments +
			// A guest comment under the address of a registered user, in other case.
			"INSERT INTO `wp_comments` VALUES (1,1,'Anna','ANNA@real.test','','1.2.3.4','2020','Hi','UA',0);\n" +
			orders +
			"INSERT INTO `wp_wc_orders` VALUES (10,'wc-completed',0,'anna@REAL.test','1.2.3.4','UA','note','tx-1'),(11,'wc-completed',0,'guest@real.test','1.2.3.4','UA',NULL,''),(12,'wc-completed',0,'guest@real.test','1.2.3.4','UA','','');\n" +
			addresses +
			"INSERT INTO `wp_wc_order_addresses` VALUES (1,10,'shipping','Anna','Real','Berlin','DE',NULL),(2,11,'billing','Greta','Guest','Hamburg','DE','guest@real.test');\n")
	output := requireFiltered(t, Config{}, dump)

	assertContains(t, output, "(1,1,'User 5','user-5@example.invalid'", "(10,'wc-completed',0,'user-5@example.invalid'", "(1,10,'shipping','User','5','Example City','DE',NULL)")
	assertOmits(t, output, "guest@real.test", "Greta", "Hamburg", "1.2.3.4", "tx-1", "'note'")

	// Both orders of one guest, and that guest's address, must share one pseudonym.
	first := strings.Index(output, "(11,'wc-completed',0,'guest-")
	second := strings.Index(output, "(12,'wc-completed',0,'guest-")
	if first < 0 || second < 0 {
		t.Fatalf("guest orders were not pseudonymized:\n%s", output)
	}
	pseudonym := output[first+len("(11,'wc-completed',0,'") : first+len("(11,'wc-completed',0,'guest-")+12]
	if !strings.Contains(output, "(12,'wc-completed',0,'"+pseudonym) {
		t.Errorf("the guest's orders use different pseudonyms:\n%s", output)
	}
	if !strings.Contains(output, "'Guest','"+strings.TrimPrefix(pseudonym, "guest-")+"'") {
		t.Errorf("the guest's address uses another pseudonym than the order %s:\n%s", pseudonym, output)
	}
	// NULL stays NULL and empty stays empty, so absent data does not become invented data.
	assertContains(t, output, "'192.0.2.1','Pseudonymized',NULL,'')")
}

func TestFilterParsesSQLStringEdgeCases(t *testing.T) {
	table := createTable("wp_custom", "id", "body", "data", "n")
	dump := usersDump(table +
		`INSERT INTO ` + "`wp_custom`" + ` VALUES (1,'it\'s (a), test; ''quoted'' \\',_binary 'bin,)''',NULL),(2,'line\nbreak \"x\"',0xABCD,-1.5);` + "\n")
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output,
		`(1,'it\'s (a), test; ''quoted'' \\',_binary 'bin,)''',NULL)`,
		`(2,'line\nbreak \"x\"',0xABCD,-1.5);`,
	)
}

func TestFilterScansUnknownTablesAndKeepsSerializedDataValid(t *testing.T) {
	table := createTable("wp_custom", "id", "plain", "serialized", "nested", "json", "image")
	serialized := `a:1:{s:5:"email";s:15:"anna@real.test.";}`
	nested := `a:1:{s:1:"x";s:37:"a:1:{s:1:"e";s:15:"anna@real.test.";}";}`
	dump := usersDump(table + "INSERT INTO `wp_custom` VALUES (1,'mail guest@real.test now','" +
		strings.ReplaceAll(serialized, `"`, `\"`) + "','" + strings.ReplaceAll(nested, `"`, `\"`) +
		`','{\"e\":\"guest\\u0040real.test\"}','logo@2x.png');` + "\n")
	output := requireFiltered(t, Config{}, dump)

	assertOmits(t, output, "real.test")
	assertContains(t, output, "logo@2x.png")
	// The trailing dot is outside the address, so the old 15 bytes become 23 with it.
	assertContains(t, output, `s:23:\"user-5@example.invalid.\"`)
	assertContains(t, output, `s:45:\"a:1:{s:1:\"e\";s:23:\"user-5@example.invalid.\";}\"`)
}

func TestFilterDropsTablesAndMetaRows(t *testing.T) {
	sessions := createTable("wp_woocommerce_sessions", "session_id", "session_key", "session_value", "session_expiry")
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	options := createTable("wp_options", "option_id", "option_name", "option_value", "autoload")
	dump := usersDump(
		sessions + "INSERT INTO `wp_woocommerce_sessions` VALUES (1,'k','secret-cart',0);\n" +
			usermeta + "INSERT INTO `wp_usermeta` VALUES (1,5,'session_tokens','tok'),(2,5,'first_name','Anna'),(3,5,'billing_country','DE'),(4,5,'billing_city','Berlin'),(5,5,'wp_capabilities','a:0:{}');\n" +
			options + "INSERT INTO `wp_options` VALUES (1,'_transient_x','cached','no'),(2,'admin_email','owner@real.test','yes'),(3,'blogname','Site','yes');\n")
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "secret-cart", "session_tokens", "Anna", "Berlin", "_transient_x", "owner@real.test")
	assertContains(t, output, "(2,5,'first_name','User')", "(3,5,'billing_country','DE')", "(4,5,'billing_city','Example City')", "(5,5,'wp_capabilities','a:0:{}')", "'blogname','Site'")
}

func TestFilterKeepsConfiguredUsersUntouched(t *testing.T) {
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	dump := usersDump(usermeta + "INSERT INTO `wp_usermeta` VALUES (1,5,'first_name','Anna'),(2,5,'session_tokens','tok');\n")
	output := requireFiltered(t, Config{PseudonymizeKeepUserIDs: "5"}, dump)
	assertContains(t, output, annaRow, "'first_name','Anna'", "'session_tokens','tok'")
}

func TestFilterRewritesSubsiteTablesAndSuperAdmins(t *testing.T) {
	comments := createTable("wp_2_comments", "comment_ID", "comment_author", "comment_author_email", "user_id")
	sitemeta := createTable("wp_sitemeta", "meta_id", "site_id", "meta_key", "meta_value")
	dump := usersDump(
		comments + "INSERT INTO `wp_2_comments` VALUES (1,'Anna','anna@real.test',5);\n" +
			sitemeta + `INSERT INTO ` + "`wp_sitemeta`" + ` VALUES (1,1,'site_admins','a:1:{i:0;s:4:\"anna\";}');` + "\n")
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output, "(1,'User 5','user-5@example.invalid',5)", `a:1:{i:0;s:6:\"user-5\";}`)
}

func TestFilterHonorsStatementColumnsAndProjectRules(t *testing.T) {
	table := createTable("wp_crm", "id", "generated", "email", "full_name", "notes")
	cfg := Config{PseudonymizeRules: []PseudonymizeRule{{Table: "crm", Columns: "email=email,full_name=full_name,notes=text"}, {Table: "forms", Drop: true}}}
	dump := usersDump(table + "INSERT INTO `wp_crm` (`id`, `email`, `full_name`, `notes`) VALUES (1,'anna@real.test','Anna R','met her');\n" +
		createTable("wp_forms", "id", "body") + "INSERT INTO `wp_forms` VALUES (1,'free text');\n")
	output := requireFiltered(t, cfg, dump)
	assertContains(t, output, "(1,'user-5@example.invalid','User 5','Pseudonymized text.')")
	assertOmits(t, output, "met her", "free text", "Anna R")
}

func TestFilterStripsPHPNoticesAroundTheDump(t *testing.T) {
	dump := "Deprecated: something in phar on line 3\n\n" + usersDump("") + "PHP Warning: after the dump\n"
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "Deprecated", "PHP Warning")
	assertContains(t, output, "wp_ssh_bridge_pseudonymized")
}

// The filter must stop rather than pass through anything it does not understand.
func TestFilterReadsMariaDBMultilineInserts(t *testing.T) {
	dump := "-- MariaDB dump\n" + usersTable + "INSERT INTO `wp_users` VALUES\n" + annaRow + ",\n" +
		"(6,'bob','x','bob','bob@real.test','','2020','',0,'Bob; (Real),');\n-- Dump completed\n"
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "Anna@Real.test", "bob", "Bob")
	assertContains(t, output, "user-5@example.invalid", "user-6@example.invalid")
}

func TestFilterPassesTriggerBodiesThrough(t *testing.T) {
	trigger := "DELIMITER ;;\n/*!50003 CREATE*/ /*!50003 TRIGGER t AFTER INSERT ON wp_users FOR EACH ROW BEGIN\n" +
		"INSERT INTO `wp_log` VALUES (NEW.ID);\nEND */;;\nDELIMITER ;\n"
	table := createTable("wp_custom", "id", "body")
	output := requireFiltered(t, Config{}, usersDump(trigger+table+"INSERT INTO `wp_custom` VALUES (1,'x@real.test');\n"))
	assertContains(t, output, trigger)
	assertOmits(t, output, "x@real.test")
}

func TestFilterFindsAddressesInEveryNotation(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	tests := []struct{ name, value string }{
		{"umlaut in the domain", "anna@müller.de"},
		{"umlaut in the local part", "jürgen@real.test"},
		{"sharp s in the domain", "info@straße.de"},
		{"url encoded", "anna%2Btag%40real.test"},
		{"decimal entity", "anna&#64;real.test"},
		{"hex entity", "anna&#x40;real.test"},
		{"zip is a real top-level domain", "anna@shop.zip"},
		{"not valid serialized data", "a:1:{ call anna@real.test"},
		{"sentence end", "write to anna@real.test."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+test.value+"');\n"))
			assertContains(t, output, "@example.invalid")
			for _, part := range []string{"real.test", "müller", "straße", "shop.zip", "jürgen"} {
				assertOmits(t, output, part)
			}
		})
	}
}

// A person's address must not survive however the value spells it, and whether or not the
// address belongs to anyone the filter can name. Literals are as they stand in a dump.
func TestFilterReplacesEverySpellingOfAnAddress(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	tests := []struct{ name, literal string }{
		{"plain, belongs to nobody", `'stranger@real.test'`},
		{"upper case", `'STRANGER@REAL.TEST'`},
		{"mailto link", `'<a href="mailto:stranger@real.test">x</a>'`},
		{"apostrophe, backslash escaped", `'o\'brien@real.test'`},
		{"apostrophe, doubled", `'o''brien@real.test'`},
		{"apostrophe as an HTML entity", `'o&#039;brien@real.test'`},
		{"apostrophe in JSON", `'o\\u0027brien@real.test'`},
		{"JSON escape in the local part", `'j\\u00fcrgen@real.test'`},
		{"JSON escape in the domain", `'anna@m\\u00fcller.test'`},
		{"JSON escape of the separator", `'stranger\\u0040real.test'`},
		{"JSON escape of the dot", `'stranger@real\\u002etest'`},
		{"named entity in the local part", `'j&uuml;rgen@real.test'`},
		{"named entity in the domain", `'anna@m&uuml;ller.test'`},
		{"url encoded umlaut", `'j%C3%BCrgen%40real.test'`},
		{"double url encoded", `'stranger%2540real.test'`},
		{"url encoded dot", `'stranger%40real%2Etest'`},
		{"backslash before the separator", `'stranger\\@real.test'`},
		{"padded decimal entity", `'stranger&#064;real.test'`},
		{"upper case hex entity", `'stranger&#X40;real.test'`},
		{"padded hex entity", `'stranger&#x0040;real.test'`},
		{"named separator entity", `'stranger&commat;real&period;test'`},
		{"spam-proof round", `'stranger(at)real.test'`},
		{"spam-proof square with spaces", `'stranger [at] real [dot] test'`},
		{"spam-proof upper case", `'stranger[AT]real.test'`},
		{"punycode domain", `'stranger@xn--real-kva.test'`},
		{"punycode top-level domain", `'stranger@real.xn--p1ai'`},
		{"IP address literal", `'stranger@[192.0.2.1]'`},
		{"address in a file name", `'stranger@real.test.png'`},
		{"binary column", `_binary 'stranger@real.test'`},
		{"hex-blob column", `0x737472616e676572407265616c2e74657374`},
		{"inside JSON", `'{\"e\":\"stranger@real.test\"}'`},
		{"inside serialized data", `'a:1:{s:1:\"e\";s:18:\"stranger@real.test\";}'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,"+test.literal+");\n"))
			assertOmits(t, output, "real.test", "real%2Etest", "REAL.TEST", "real\\\\u002etest", "real&period;test", "real [dot]", "stranger", "STRANGER", "brien", "rgen", "ller", "xn--", "192.0.2.1", "7265616c")
			line := output[strings.Index(output, "INSERT INTO `wp_custom`"):]
			line = line[:strings.Index(line, "\n")]
			if !strings.Contains(line, "@example.invalid") && !strings.Contains(line, "406578616d706c652e696e76616c6964") {
				t.Fatalf("no pseudonym in %s", line)
			}
		})
	}
}

func TestFilterResolvesEverySpellingToTheSamePerson(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, literal := range []string{`'anna%40real.test'`, `'anna&#064;real.test'`, `'anna\\u0040real.test'`, `'ANNA(at)REAL.TEST'`, `'&lt;anna@real.test&gt;'`, `'\\u003canna@real.test\\u003e'`, `'%3Canna%40real.test%3E'`} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,"+literal+");\n"))
		assertContains(t, output, "user-5@example.invalid")
		assertOmits(t, output, "guest-")
	}
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'&lt;anna@real.test&gt;');\n"))
	assertContains(t, output, "'&lt;user-5@example.invalid&gt;'")
}

func TestFilterReplacesAdjacentAddresses(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, literal := range []string{`'anna@real.test,bob@real.test'`, `'anna@real.test-bob@real.test'`, `'anna@real.test|bob@real.test'`, `'anna@real.test;bob@real.test'`} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,"+literal+");\n"))
		assertOmits(t, output, "real.test", "bob")
	}
}

func TestFilterLeavesWhatOnlyLooksLikeAnAddress(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, value := range []string{"logo@2x.png", "logo@2x.min.png", "jquery@3.6.0.min.js", "swiper@11/swiper.css", "@media print", "@import url(a.css)", "price (at) noon", "bootstrap@5.3.3/dist"} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n"))
		assertContains(t, output, value)
	}
}

func TestFilterKeepsTheNotationOfBinaryValues(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,_binary 'anna@real.test');\n"))
	assertContains(t, output, "_binary 'user-5@example.invalid'")
	// 0x + "anna@real.test" stays hex, now holding the pseudonym.
	output = requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,0x616e6e61407265616c2e74657374);\n"))
	assertContains(t, output, "(1,0x757365722d35406578616d706c652e696e76616c6964)")
	// A hex value without an address passes untouched.
	output = requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,0x00ff10);\n"))
	assertContains(t, output, "(1,0x00ff10)")
}

func TestFilterScansSchemaAndTriggerBodies(t *testing.T) {
	schema := "CREATE TABLE `wp_custom` (\n  `id` text COMMENT 'owner anna@real.test',\n  `body` text\n) ENGINE=InnoDB;\nINSERT INTO `wp_custom` VALUES (1,'x');\n"
	trigger := "DELIMITER ;;\n/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER t BEFORE INSERT ON wp_custom FOR EACH ROW SET NEW.body='bob@real.test' */;;\nDELIMITER ;\n"
	output := requireFiltered(t, Config{}, usersDump(schema+trigger))
	assertOmits(t, output, "real.test", "bob")
	assertContains(t, output, "`root`@`localhost`", "owner user-5@example.invalid", "NEW.body='guest-")
}

func TestFilterReplacesAddressesAtAConfiguredRealDomain(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	output := requireFiltered(t, Config{PseudonymizeEmailDomain: "real.test"}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'bob@real.test');\n"))
	assertOmits(t, output, "bob@real.test")
	assertContains(t, output, "user-5@real.test")
	if !strings.Contains(output, "guest-") {
		t.Fatalf("bob has no pseudonym in %s", output)
	}
}

func TestFilterScansVeryLargeValues(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	blob := strings.Repeat("abcdefgh", 1<<20)
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+blob+" anna@real.test "+blob+"@real.test');\n"))
	assertOmits(t, output, "real.test")
}

func TestFilterBlanksSecretsInSettings(t *testing.T) {
	options := createTable("wp_options", "option_id", "option_name", "option_value", "autoload")
	settings := `a:3:{s:25:"paypal_live_client_secret";s:6:"hunter";s:5:"label";s:4:"Shop";s:6:"public";b:0;}`
	dump := usersDump(options + "INSERT INTO `wp_options` VALUES (1,'mailserver_pass','hunter','yes'),(2,'edd_settings','" + strings.ReplaceAll(settings, `"`, `\"`) + "','yes')," +
		"(3,'woocommerce_myaccount_lost_password_endpoint','lost-password','yes'),(4,'woocommerce_kco_settings','hunter','yes');\n")
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "hunter", "woocommerce_kco_settings")
	assertContains(t, output, "'lost-password'", `s:25:\"paypal_live_client_secret\";s:0:\"\";s:5:\"label\";s:4:\"Shop\"`)
}

func TestFilterKeepsOnlyByUserID(t *testing.T) {
	orders := createTable("wp_wc_orders", "id", "customer_id", "billing_email", "ip_address")
	addresses := createTable("wp_wc_order_addresses", "id", "order_id", "first_name", "email")
	dump := usersDump(orders + "INSERT INTO `wp_wc_orders` VALUES (1,5,'anna@real.test','1.1.1.1'),(2,0,'anna@real.test','2.2.2.2');\n" +
		addresses + "INSERT INTO `wp_wc_order_addresses` VALUES (1,1,'Anna','anna@real.test'),(2,2,'Stranger','anna@real.test');\n")
	output := requireFiltered(t, Config{PseudonymizeKeepUserIDs: "5"}, dump)
	assertContains(t, output, "(1,5,'anna@real.test','1.1.1.1')", "(1,1,'Anna','anna@real.test')")
	assertOmits(t, output, "2.2.2.2", "Stranger")
}

// A guest record carrying a registered user's address must not collide with that user's
// own record, because the email column of edd_customers is unique.
func TestFilterKeepsEDDCustomerAddressesDistinct(t *testing.T) {
	customers := createTable("wp_edd_customers", "id", "user_id", "email", "name")
	dump := usersDump(customers + "INSERT INTO `wp_edd_customers` VALUES (1,5,'other@real.test','A'),(2,0,'anna@real.test','B');\n")
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output, "(2,0,'user-5@example.invalid','User 5')")
	if strings.Count(output, "user-5@example.invalid','") != 2 { // the users row and customer 2
		t.Errorf("two records share one address:\n%s", output)
	}
}

func TestFilterReportsColumnsARuleNamesButTheTableLacks(t *testing.T) {
	table := createTable("wp_crm", "id", "email")
	cfg := Config{PseudonymizeRules: []PseudonymizeRule{{Table: "crm", Columns: "emial=email"}}}
	_, report, err := runFilter(t, cfg, usersDump(table+"INSERT INTO `wp_crm` VALUES (1,'x');\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := report["wp_crm"].Missing; len(got) != 1 || got[0] != "emial" {
		t.Errorf("missing = %v", got)
	}
}

func TestFilterFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		dump string
		want string
	}{
		{"users table is not first", createTable("wp_posts", "ID") + usersTable, "must start with wp_users"},
		{"users table is missing", "-- MariaDB dump\n", "does not contain"},
		{"insert without a table", usersTable + "INSERT INTO `wp_other` VALUES (1);\n", "no CREATE TABLE"},
		{"unterminated string", usersTable + "INSERT INTO `wp_users` VALUES (1,'a);\n", "ends inside a string"},
		{"wrong value count", usersTable + "INSERT INTO `wp_users` VALUES (1,'a');\n", "2 values for 10 columns"},
		{"replace statements", usersTable + "REPLACE INTO `wp_users` VALUES (5,'a','b','c','d','e','f','g',0,'h');\n", "does not understand"},
		{"insert ignore statements", usersTable + "INSERT  IGNORE INTO `wp_users` VALUES (5,'a','b','c','d','e','f','g',0,'h');\n", "does not understand"},
		{"cut off at a statement boundary", usersTable + "INSERT INTO `wp_users` VALUES " + annaRow + ";\n", "may be incomplete"},
		{"truncated statement", usersTable + "INSERT INTO `wp_users` VALUES (5,'a','b','c','d','e','f','g',0,'h')\n", "ends inside an INSERT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := runFilter(t, Config{}, test.dump)
			if err == nil {
				t.Fatalf("filter accepted the input")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not mention %q", err, test.want)
			}
		})
	}
}

func TestFilterReportsCountersPerTable(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	dump := usersDump(table + "INSERT INTO `wp_custom` VALUES (1,'x@real.test'),(2,'none');\n")
	_, report, err := runFilter(t, Config{}, dump)
	if err != nil {
		t.Fatalf("filter failed: %v", err)
	}
	got := report["wp_custom"]
	if got.Rows != 2 || got.Emails != 1 || got.Rule != 0 {
		t.Errorf("report for wp_custom = %+v", got)
	}
	if report["wp_users"].Rule != 1 {
		t.Errorf("report for wp_users = %+v", report["wp_users"])
	}
}

// Personal data a rule cannot name, found by its shape in free text.
func TestFilterReplacesPersonalDataInFreeText(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	tests := []struct {
		name, value, want string
		leaks             []string
	}{
		{"international phone", "call +49 170 1234567 now", placeholderPhone, []string{"1234567"}},
		{"phone with a double zero", "0049 170 1234567", placeholderPhone, []string{"1234567"}},
		{"national phone", "Bitte 0170 1234567 anrufen", placeholderPhone, []string{"1234567"}},
		{"national phone with a slash", "030/1234567", placeholderPhone, []string{"1234567"}},
		{"area code in parentheses", "(030) 1234567", placeholderPhone, []string{"1234567"}},
		{"labelled number without structure", "Tel.: 1234567", placeholderPhone, []string{"1234567"}},
		{"tel link", `<a href="tel:01701234567">x</a>`, placeholderPhone, []string{"1234567"}},
		{"north american phone", "212-555-0123", placeholderPhone, []string{"555-0123"}},
		{"IPv4", "login from 203.0.113.7 today", placeholderIPv4, []string{"203.0.113"}},
		{"IPv6", "login from 2a00:1450:4001:81b::200e today", placeholderIPv6, []string{"2a00:1450"}},
		{"IBAN", "DE89370400440532013000", placeholderIBAN, []string{"0532013000"}},
		{"IBAN in groups", "IBAN: DE89 3704 0044 0532 0130 00.", placeholderIBAN, []string{"0532", "DE89"}},
		{"card", "4111 1111 1111 1111", placeholderCard, []string{"4111"}},
		{"card without separators", "paid with 5555555555554444.", placeholderCard, []string{"5555"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := strings.ReplaceAll(test.value, `"`, `\"`)
			output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n"))
			assertContains(t, output, test.want)
			assertOmits(t, output, test.leaks...)
		})
	}
}

// Numbers that merely resemble personal data. Replacing them would break the copy.
func TestFilterLeavesOrdinaryNumbersAlone(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, value := range []string{
		"2026-11-04 10:30:00", "1699999999", "12345", "19.99", "1.234.567,89", "#ff0012", "v1.2.3",
		"8.6.1.0", "Chrome/120.0.0.0", "wordpress 6.4.2.1", "10.0.0.1", "192.168.1.20", "127.0.0.1", "192.0.2.1",
		"10:30:45", "a::b", "std::string", "4111111111111112", "DE89370400440532013001", "+00 000 0000000",
		"0000-0000-0000-0000", "2001:db8::1", "XX00REDACTED", "1970-01-01", "ISBN 978-3-16-148410-0",
		"product-12345678", "https://example.test/2026/11/04/post-12345/", "SKU 0012", "width:100%;height:50px",
	} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n"))
		assertContains(t, output, "(1,'"+value+"')")
	}
}

func TestFilterReplacesPlaceholdersWithoutTouchingThemAgain(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	first := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'+49 170 1234567 203.0.113.7 DE89370400440532013000 4111111111111111');\n"))
	assertContains(t, first, "(1,'"+placeholderPhone+" "+placeholderIPv4+" "+placeholderIBAN+" "+placeholderCard+"')")
}

// A name in free text only goes when the rules met it first, as a whole name.
func TestFilterReplacesKnownNamesInFreeText(t *testing.T) {
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	notes := createTable("wp_notes", "id", "body")
	dump := "-- MariaDB dump\n" + usersTable + "INSERT INTO `wp_users` VALUES (5,'anna','x','anna','anna@real.test','','2020','',0,'Anna Maria Real'),(6,'bob','x','bob','bob@real.test','','2020','',0,'bob');\n" +
		usermeta + "INSERT INTO `wp_usermeta` VALUES (1,6,'first_name','Bob'),(2,6,'last_name','Müller');\n" +
		notes + "INSERT INTO `wp_notes` VALUES (1,'Rechnung an Anna Maria Real, Anna Maria und BOB MÜLLER. Anna kommt.');\n-- Dump completed\n"
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output, "Rechnung an User 5, Anna Maria und User 6. Anna kommt.")
	assertOmits(t, output, "Real", "MÜLLER", "Müller")
}

func TestFilterDoesNotKeepNamesOfKeptAccountsInTheDictionary(t *testing.T) {
	notes := createTable("wp_notes", "id", "body")
	dump := "-- MariaDB dump\n" + usersTable + "INSERT INTO `wp_users` VALUES (5,'anna','x','anna','anna@real.test','','2020','',0,'Anna Real');\n" +
		notes + "INSERT INTO `wp_notes` VALUES (1,'Anna Real');\n-- Dump completed\n"
	output := requireFiltered(t, Config{PseudonymizeKeepUserIDs: "5"}, dump)
	assertContains(t, output, "(1,'Anna Real')")
}

// A table no adapter knows gets judged by what its columns are called.
func TestFilterReplacesColumnsByTheirNames(t *testing.T) {
	table := "CREATE TABLE `wp_crm` (\n" +
		"  `id` int(11) NOT NULL,\n  `email` varchar(100) DEFAULT NULL,\n  `name` varchar(100) DEFAULT NULL,\n" +
		"  `phone` varchar(10) DEFAULT NULL,\n  `street` text,\n  `zip` varchar(10) DEFAULT NULL,\n" +
		"  `date_of_birth` date DEFAULT NULL,\n  `last_ip` varchar(45) DEFAULT NULL,\n  `company_name` varchar(100),\n" +
		"  `phone_label` varchar(100) DEFAULT NULL,\n  `address_id` varchar(10) DEFAULT NULL,\n  `show_phone` varchar(10)\n) ENGINE=InnoDB;\n"
	dump := usersDump(table + "INSERT INTO `wp_crm` VALUES (1,'anna@real.test','Anna Real','0170123','Hauptstr. 1','10115','1980-02-03','1.1.1.1','Real GmbH','Telefon','abc','yes');\n")
	output := requireFiltered(t, Config{}, dump)
	// The column is too short for the whole placeholder, so it arrives cut to fit.
	assertContains(t, output, "(1,'user-5@example.invalid','User 5','+00 000 00','Example Street 1','12345','1970-01-01','1.1.1.1','Example Company','Telefon','abc','yes')")
	assertOmits(t, output, "Hauptstr", "10115", "1980", "Real GmbH", "Anna Real")
}

// Without an email column, "name" is as likely a product or a menu.
func TestFilterLeavesAGenericNameColumnWithoutAnEmailColumn(t *testing.T) {
	table := createTable("wp_terms2", "id", "name")
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_terms2` VALUES (1,'Blue Shirt');\n"))
	assertContains(t, output, "(1,'Blue Shirt')")
}

func TestFilterReplacesFormEntriesInKeyValueTables(t *testing.T) {
	table := createTable("wp_form_entry_meta", "id", "entry_id", "meta_key", "meta_value")
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_form_entry_meta` VALUES (1,9,'phone','0170 7654321'),(2,9,'first_name','Greta'),(3,9,'colour','blue');\n"))
	assertContains(t, output, "(1,9,'phone','"+placeholderPhone+"')", "(3,9,'colour','blue')")
	assertOmits(t, output, "7654321", "Greta")
}

// The key says more than any pattern, in serialized data as much as in JSON. The filter
// must keep the structure intact while it replaces the values.
func TestFilterReplacesKeysInsideStructuredData(t *testing.T) {
	table := createTable("wp_custom", "id", "serialized", "json")
	serialized := `a:5:{s:5:"phone";s:6:"555123";s:10:"first_name";s:5:"Greta";s:7:"api_key";s:6:"hunter";s:5:"label";s:4:"Shop";s:7:"version";s:7:"8.6.1.0";}`
	object := `O:4:"Lead":2:{s:6:"street";s:5:"Str 1";s:12:"` + "\x00*\x00" + `birthdate";s:10:"1980-02-03";}`
	escape := strings.NewReplacer(`"`, `\"`, "\x00", `\0`)
	json := `{"phone":"0170 7654321","address":{"city":"Berlin","zip":"10115"},"phone_label":"Telefon","note":"mail anna@real.test","id":12345,"mobile":"767","tel":4917012345}`
	dump := usersDump(table + "INSERT INTO `wp_custom` VALUES (1,'" + escape.Replace(serialized) + "','" + escape.Replace(json) + "'),(2,'" + escape.Replace(object) + "',NULL);\n")
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "555123", "Greta", "hunter", "7654321", "Berlin", "10115", "anna@real.test", "Str 1", "1980-02-03", "4917012345")
	assertContains(t, output,
		`s:5:\"label\";s:4:\"Shop\"`, `s:7:\"version\";s:7:\"8.6.1.0\"`, `s:7:\"api_key\";s:0:\"\"`,
		`s:5:\"phone\";s:15:\"`+placeholderPhone+`\"`,
		`\"phone_label\":\"Telefon\"`, `\"id\":12345`, `\"mobile\":\"767\"`, `\"tel\":0`, `\"city\":\"Example City\"`,
		`s:6:\"street\";s:16:\"Example Street 1\"`, `s:10:\"1970-01-01\"`,
	)
}

func TestFilterKeepsLengthsOfSerializedDataInsideJSONAndCustomObjects(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	inner := `a:1:{s:1:"e";s:14:"anna@real.test";}`
	quoted, err := json.Marshal(map[string]string{"s": inner})
	if err != nil {
		t.Fatal(err)
	}
	custom := `C:4:"Blob":14:{anna@real.test}`
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	dump := usersDump(table + "INSERT INTO `wp_custom` VALUES (1,'" + escape.Replace(string(quoted)) + "'),(2,'" + escape.Replace(custom) + "');\n")
	output := requireFiltered(t, Config{}, dump)
	assertContains(t, output, `s:22:\\\"user-5@example.invalid\\\"`, `C:4:\"Blob\":22:{user-5@example.invalid}`)
}

func TestFilterKeepsSerializedDataThatHoldsNothingByteForByte(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	statement := "INSERT INTO `wp_custom` VALUES (1,'a:2:{s:1:\\\"a\\\";i:1;s:1:\\\"b\\\";d:0.5;}'),(2,'{\\\"a\\\": [1, 2.5, true]}');\n"
	output := requireFiltered(t, Config{}, usersDump(table+statement))
	assertContains(t, output, statement)
}

func TestFilterLeavesRowsAndTablesOutWithoutBreakingStatements(t *testing.T) {
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	output := requireFiltered(t, Config{}, usersDump(usermeta+"INSERT INTO `wp_usermeta` VALUES (1,5,'session_tokens','x');\n"))
	if strings.Contains(output, "INSERT INTO `wp_usermeta`") {
		t.Errorf("a statement without rows would fail the import:\n%s", output)
	}
}

func TestFilterCountsAcrossAnExportOfStructuresToo(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{})
	if err != nil {
		t.Fatal(err)
	}
	second := "Success: Exported to STDOUT.\n-- MariaDB dump\n" + createTable("wp_woocommerce_sessions", "session_id", "session_value") + "-- Dump completed\nSuccess: Exported.\n"
	filter, err := newPseudonymizeFilter(spec, "wp_", 2)
	if err != nil {
		t.Fatal(err)
	}
	output := bytes.Buffer{}
	if _, err := filter.run(context.Background(), strings.NewReader(usersDump("")+second), &output); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	assertContains(t, output.String(), "CREATE TABLE `wp_woocommerce_sessions`", "wp_ssh_bridge_pseudonymized")
	assertOmits(t, output.String(), "Success")

	// One export announced as two means the second never arrived.
	filter, err = newPseudonymizeFilter(spec, "wp_", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := filter.run(context.Background(), strings.NewReader(usersDump("")), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "may be incomplete") {
		t.Fatalf("run() = %v", err)
	}
}

// Many statements through many workers must come out in their original order.
func TestFilterKeepsTheOrderOfStatements(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	dump := strings.Builder{}
	dump.WriteString(table)
	expected := make([]string, 0, 2000)
	for i := range 2000 {
		line := "(" + strconv.Itoa(i) + ",'mail stranger" + strconv.Itoa(i) + "@real.test')"
		dump.WriteString("INSERT INTO `wp_custom` VALUES " + line + ";\n")
		expected = append(expected, "("+strconv.Itoa(i)+",'")
	}
	output := requireFiltered(t, Config{}, usersDump(dump.String()))
	position := 0
	for _, want := range expected {
		index := strings.Index(output[position:], want)
		if index < 0 {
			t.Fatalf("statement %s is out of order", want)
		}
		position += index
	}
	assertOmits(t, output, "real.test", "stranger")
}

// Each value would take minutes if a pattern made the scan quadratic, which an export of
// hostile or merely unusual data must not be able to do.
func TestFilterStaysLinearOnHostileValues(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, unit := range []string{"@", "a@", "@a.", "a.", "%40", "(at)", " (at) ", "1 ", "1.", "+1", ":", "a:", "&", "&#64;", `\\u0040`, "'", "''", "A ", "Ab ", "DE89", "a:1:{", "s:1:", "{", "[", `\\`, "ü", "(", "0"} {
		value := strings.Repeat(unit, (1<<20)/len(unit))
		if _, _, err := runFilter(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n")); err != nil {
			t.Fatalf("%q: %v", unit, err)
		}
	}
}

// From an independent review: each of these left data behind or broke the value.
func TestFilterReadsTextAfterALetterEscape(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	names := usermeta + "INSERT INTO `wp_usermeta` VALUES (1,5,'first_name','Anna'),(2,5,'last_name','Müller');\n"
	for _, body := range []string{
		`{"m":"x\n+49 171 1234567"}`, `{"m":"x\n030 1234567"}`, `{"m":"x\n93.184.216.34"}`, `{"m":"x\nDE89370400440532013000"}`,
		`{"m":"x\nAnna Müller"}`, `{"m":"x\tstranger@real.test"}`, `{"m":"Kontakt:\nanna@real.test"}`,
		`not json \n+49 171 1234567`, `not json \nstranger@real.test`, `not json \n93.184.216.34`, `not json \nAnna Müller`,
	} {
		literal := string(appendSQLEscaped(nil, []byte(body)))
		output := requireFiltered(t, Config{}, usersDump(names+table+"INSERT INTO `wp_custom` VALUES (1,'"+literal+"');\n"))
		statement := output[strings.Index(output, "INSERT INTO `wp_custom`"):]
		assertOmits(t, statement, "1234567", "93.184", "0532", "Müller", "real.test", "nanna", "tstranger")
		if strings.HasPrefix(body, "{") {
			start := strings.Index(statement, "(1,'") + 4
			end := strings.Index(statement, "');")
			decoded := unescapeSQL([]byte(statement[start:end]))
			if !json.Valid(decoded) {
				t.Errorf("the filter broke the JSON of %s: %s", body, decoded)
			}
		}
	}
	// An address behind the escape belongs to the person, not to one called nanna.
	output := requireFiltered(t, Config{}, usersDump(table+`INSERT INTO `+"`wp_custom`"+` VALUES (1,'{\"m\":\"x\\nanna@real.test\"}');`+"\n"))
	assertContains(t, output, "user-5@example.invalid")
}

func TestFilterReplacesNumbersAndBytesInColumnsNamedPersonal(t *testing.T) {
	table := "CREATE TABLE `wp_crm` (\n  `id` int(11) NOT NULL,\n  `ip_address` int(10) unsigned,\n  `phone` bigint(20),\n  `zip` int(11),\n" +
		"  `IP` binary(16),\n  `user_ip` varbinary(4),\n  `company` int(11),\n  `is_mobile` tinyint(1)\n) ENGINE=InnoDB;\n"
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_crm` VALUES (7,1572395042,4917012345678,10115,0x00000000000000000000FFFF5DB8D822,0x5DB8D822,12345,1);\n"))
	// A company column of integers points to a record, and a flag is too short to mean anything.
	assertContains(t, output, "(7,0,0,0,0x00000000000000000000ffffc0000201,0xc0000201,12345,1)")
}

func TestFilterReplacesIPAddressesNextToSeparators(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, value := range []string{"limit_93.184.216.34", "visitor-93.184.216.34", "93.184.216.34_block", "/93.184.216.34", "example.test/93.184.216.34", "::ffff:93.184.216.34", "http://93.184.216.34/x"} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n"))
		assertOmits(t, output, "184.216")
		assertContains(t, output, "192.0.2.1")
	}
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'Chrome/120.15.20.30 Feed::add($x) ::ffff:10.0.0.1');\n"))
	assertContains(t, output, "Chrome/120.15.20.30 Feed::add($x) ::ffff:10.0.0.1")
}

func TestFilterFindsNamesInEveryAlphabet(t *testing.T) {
	notes := createTable("wp_notes", "id", "body")
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	dump := usersDump(usermeta + "INSERT INTO `wp_usermeta` VALUES (1,5,'first_name','Łukasz'),(2,5,'last_name','Kowalski'),(3,6,'first_name','Иван'),(4,6,'last_name','Петров');\n" +
		notes + "INSERT INTO `wp_notes` VALUES (1,'für Łukasz Kowalski und Иван Петров. Łukasz Kowalski''s Rechnung');\n")
	output := requireFiltered(t, Config{}, dump)
	assertOmits(t, output, "Kowalski", "Петров")
}

func TestFilterFollowsAKeyIntoItsStructure(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	escape := strings.NewReplacer(`"`, `\"`)
	for _, test := range []struct{ value, leak, stays string }{
		{`a:1:{s:5:"phone";a:1:{i:0;s:12:"017112345678";}}`, "017112345678", ""},
		{`{"phone":["017112345678"]}`, "017112345678", ""},
		{`{"address":{"line1":"Hauptstr. 1","country":"DE"},"other":{"line1":"Blue"}}`, "Hauptstr", `\"country\":\"DE\"`},
		{`{"phone":-4915112345678}`, "4915112345678", ""},
		{`{"list":[{"phone":"0171 1234567"},{"colour":"blue"}]}`, "1234567", "blue"},
	} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+escape.Replace(test.value)+"');\n"))
		assertOmits(t, output, test.leak)
		if test.stays != "" {
			assertContains(t, output, test.stays)
		}
	}
	output := requireFiltered(t, Config{}, usersDump(table+`INSERT INTO `+"`wp_custom`"+` VALUES (1,'{\"address\":{\"line1\":\"Hauptstr. 1\"},\"other\":{\"line1\":\"Blue\"}}');`+"\n"))
	assertContains(t, output, `\"other\":{\"line1\":\"Blue\"}`)
}

func TestFilterKeepsUniqueColumnsUnique(t *testing.T) {
	table := "CREATE TABLE `wp_crm` (\n  `id` int(11) NOT NULL,\n  `ip` varchar(45),\n  `phone` varchar(40),\n  PRIMARY KEY (`id`),\n  UNIQUE KEY `ip` (`ip`),\n  UNIQUE KEY `p` (`phone`(20))\n) ENGINE=InnoDB;\n"
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_crm` VALUES (1,'93.184.216.34','0170 1234567'),(2,'93.184.216.35','0170 1234568');\n"))
	assertOmits(t, output, "93.184", "1234567", "1234568", placeholderIPv4+"'", placeholderPhone+"'")
}

func TestFilterLeavesFileNamesWithADensityAlone(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	for _, value := range []string{"video@2x.mp4", "photo@2x.heic", "page@1.5x.html", "icon@3x.min.webp"} {
		output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+value+"');\n"))
		assertContains(t, output, "(1,'"+value+"')")
	}
	// A broken top-level domain goes whole, rather than trail after the pseudonym.
	output := requireFiltered(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'stranger@real.test1 x');\n"))
	assertOmits(t, output, "invalid1", "real.test")
}

func TestFilterReadsSerializedDataWithATrailingNewline(t *testing.T) {
	table := createTable("wp_custom", "id", "body")
	output := requireFiltered(t, Config{}, usersDump(table+`INSERT INTO `+"`wp_custom`"+` VALUES (1,'a:1:{s:1:\"e\";s:14:\"anna@real.test\";}\n');`+"\n"))
	assertContains(t, output, `s:22:\"user-5@example.invalid\"`)
}

func TestFilterKeepsAKeptAccountsUnclassifiedMeta(t *testing.T) {
	usermeta := createTable("wp_usermeta", "umeta_id", "user_id", "meta_key", "meta_value")
	output := requireFiltered(t, Config{PseudonymizeKeepUserIDs: "5"}, usersDump(usermeta+"INSERT INTO `wp_usermeta` VALUES (1,5,'note','call +49 171 1234567');\n"))
	assertContains(t, output, "call +49 171 1234567")
}

// Cancelling at the very end of the input must not pass for a finished dump.
func TestFilterFailsWhenCancelledAtTheEndOfTheInput(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for range 200 {
		filter, err := newPseudonymizeFilter(spec, "wp_", 1)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		reader := cancellingReader{Reader: strings.NewReader(usersDump("")), cancel: cancel}
		output := bytes.Buffer{}
		_, err = filter.run(ctx, &reader, &output)
		cancel()
		if err == nil && !strings.Contains(output.String(), pseudonymizedOption) {
			t.Fatal("a cancelled run reported success without its marker")
		}
		if err == nil && !strings.Contains(output.String(), "user-5@example.invalid") {
			t.Fatal("a cancelled run reported success with statements missing")
		}
	}
}

// cancellingReader cancels the run as its input ends.
type cancellingReader struct {
	io.Reader
	cancel context.CancelFunc
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil {
		r.cancel()
	}
	return n, err
}

func TestFilterErrorsNeverQuoteARow(t *testing.T) {
	_, _, err := runFilter(t, Config{}, usersTable+"REPLACE INTO `wp_users` VALUES (5,'secret-value');\n")
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("run() = %v", err)
	}
}

// Whatever a row holds, the filter must answer with an error or a dump, not a crash, and
// must never lose an address it can read.
func FuzzFilterValue(f *testing.F) {
	for _, seed := range []string{
		"anna@real.test", `a:1:{s:1:"e";s:14:"anna@real.test";}`, `{"phone":"0170 1234567"}`, `a:1:{s:99:"x";}`,
		`C:1:"x":3:{abc}`, "+49 170", "(((", "&#64;", `@`, "DE89 3704", "Anna Real", "::", "0x41", "'", `\`,
	} {
		f.Add(seed)
	}
	table := createTable("wp_custom", "id", "body")
	f.Fuzz(func(t *testing.T, value string) {
		literal := string(appendSQLEscaped(nil, []byte(value)))
		output, _, err := runFilter(t, Config{}, usersDump(table+"INSERT INTO `wp_custom` VALUES (1,'"+literal+"');\n"))
		if err != nil {
			t.Fatalf("a valid statement failed: %v", err)
		}
		if strings.Contains(value, "planted@real.test") && strings.Contains(output, "planted@real.test") {
			t.Fatalf("the address survived: %q", value)
		}
	})
}
