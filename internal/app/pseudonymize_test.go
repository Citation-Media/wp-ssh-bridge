package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePseudonymizeRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"single-label domain", Config{PseudonymizeEmailDomain: "localhost"}, "pseudonymize_email_domain"},
		{"domain with a quote", Config{PseudonymizeEmailDomain: "a'.test"}, "pseudonymize_email_domain"},
		{"non-numeric keep list", Config{PseudonymizeKeepUserIDs: "1,admin"}, "pseudonymize_keep_user_ids"},
		{"table with the wrong characters", Config{PseudonymizeRules: []PseudonymizeRule{{Table: "a`b", Drop: true}}}, "must contain only letters"},
		{"neither columns nor drop", Config{PseudonymizeRules: []PseudonymizeRule{{Table: "a"}}}, "either columns or drop"},
		{"both columns and drop", Config{PseudonymizeRules: []PseudonymizeRule{{Table: "a", Drop: true, Columns: "x=blank"}}}, "either columns or drop"},
		{"unknown strategy", Config{PseudonymizeRules: []PseudonymizeRule{{Table: "a", Columns: "x=nope"}}}, "unknown strategy"},
		{"malformed column", Config{PseudonymizeRules: []PseudonymizeRule{{Table: "a", Columns: "x"}}}, "expected column=strategy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.validatePseudonymize()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePseudonymize() = %v, want an error mentioning %q", err, test.want)
			}
		})
	}
	if err := (Config{PseudonymizeEmailDomain: "dev.example.test", PseudonymizeKeepUserIDs: "1, 7"}).validatePseudonymize(); err != nil {
		t.Fatalf("validatePseudonymize() rejected valid values: %v", err)
	}
}

func TestPseudonymizeConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".wp-ssh.yaml")
	cfg := Config{
		SkipPseudonymize:        true,
		PseudonymizeEmailDomain: "dev.example.test",
		PseudonymizeKeepUserIDs: "1,7",
		AllowPseudonymizedPush:  true,
		PseudonymizeRules: []PseudonymizeRule{
			{Table: "crm", Columns: "email=email,notes=text", UserColumn: "user_id"},
			{Table: "forms", Drop: true},
		},
	}
	if err := writeConfigFile(path, cfg, Config{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := readConfigFile(path)
	if err != nil {
		t.Fatalf("readConfigFile() error = %v", err)
	}
	if !loaded.SkipPseudonymize || !loaded.AllowPseudonymizedPush ||
		loaded.PseudonymizeEmailDomain != cfg.PseudonymizeEmailDomain || loaded.PseudonymizeKeepUserIDs != cfg.PseudonymizeKeepUserIDs {
		t.Errorf("settings did not survive the round trip: %+v", loaded)
	}
	if len(loaded.PseudonymizeRules) != 2 || loaded.PseudonymizeRules[0] != cfg.PseudonymizeRules[0] || loaded.PseudonymizeRules[1] != cfg.PseudonymizeRules[1] {
		t.Errorf("rules did not survive the round trip: %+v", loaded.PseudonymizeRules)
	}
}

// Adapters must add to what core says about a shared table rather than replace it, or
// activating WooCommerce support would silently lose core's rules for usermeta.
func TestMergeTableRulesCombinesAdaptersOnSharedTables(t *testing.T) {
	merged := mergeTableRules(pseudonymizeAdapters())
	usermeta := merged["usermeta"]
	if usermeta.Meta == nil {
		t.Fatal("usermeta has no meta rule")
	}
	if usermeta.Meta.Keys["first_name"] != strategyFirstName || usermeta.Meta.Keys["billing_city"] != strategyCity {
		t.Errorf("usermeta keys were not merged: %v", usermeta.Meta.Keys)
	}
	if usermeta.Meta.KeyColumn != "meta_key" || !usermeta.Global {
		t.Errorf("merging lost core's settings: %+v", usermeta)
	}
	posts := merged["posts"]
	if len(posts.Collect) != 3 {
		t.Errorf("posts should collect private types for core, WooCommerce, and EDD, got %+v", posts.Collect)
	}
}

// The exact tables and columns the shipped plugin versions store personal data in. A
// rule naming a column that does not exist would silently do nothing.
func TestAdaptersCoverOrderTablesOfWooCommerceAndEDD(t *testing.T) {
	merged := mergeTableRules(pseudonymizeAdapters())
	required := map[string][]string{
		"wc_orders":                    {"billing_email", "ip_address", "user_agent", "customer_note", "transaction_id"},
		"wc_order_addresses":           {"first_name", "last_name", "company", "address_1", "address_2", "city", "postcode", "email", "phone"},
		"wc_customer_lookup":           {"username", "first_name", "last_name", "email", "city", "postcode"},
		"edd_customers":                {"email", "name"},
		"edd_customer_addresses":       {"name", "address", "address2", "city", "postal_code"},
		"edd_customer_email_addresses": {"email"},
		"edd_orders":                   {"email", "ip", "payment_key"},
		"edd_order_addresses":          {"name", "address", "address2", "city", "postal_code"},
		"edd_logs_file_downloads":      {"ip", "user_agent"},
		"users":                        {"user_login", "user_nicename", "user_email", "display_name", "user_pass", "user_url", "user_activation_key"},
		"comments":                     {"comment_author", "comment_author_email", "comment_author_url", "comment_author_IP", "comment_agent", "comment_content"},
		"signups":                      {"user_login", "user_email", "activation_key", "meta"},
		"registration_log":             {"email", "IP"},
	}
	for table, columns := range required {
		rule, ok := merged[table]
		if !ok {
			t.Errorf("no rule for %s", table)
			continue
		}
		for _, column := range columns {
			if _, ok := rule.Columns[column]; !ok {
				t.Errorf("%s: no strategy for %s", table, column)
			}
		}
	}
	for _, table := range []string{"woocommerce_sessions", "woocommerce_payment_tokens", "woocommerce_payment_tokenmeta", "woocommerce_api_keys", "woocommerce_log", "edd_sessions", "edd_payment_tokens", "edd_logs", "edd_logs_emails", "edd_logs_api_requests"} {
		if !merged[table].Drop {
			t.Errorf("%s should be left out of the export", table)
		}
	}
}

func TestSpecCarriesTheSettings(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{PseudonymizeEmailDomain: "dev.example.test", PseudonymizeKeepUserIDs: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.EmailDomain != "dev.example.test" || len(spec.KeepUserIDs) != 1 || spec.Tables["users"].Name != "users" {
		t.Errorf("spec = %+v", spec)
	}
}

// runExportCommands runs the generated export in a real sh, with WP-CLI replaced by a
// function, because exit status handling across a pipe is what these commands exist for.
func runExportCommands(t *testing.T, wp string) ([]byte, string, error) {
	t.Helper()
	command := "set -eu; " + wp + "; " + remoteDumpStreamCommands("wp_ssh_wp", []string{"users", "posts"}, []string{"sessions", "logs"})
	cmd := exec.Command("sh", "-c", command)
	stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

const fakeExportWP = `wp_ssh_wp() { case "$*" in
  *"config get"*) echo abc_ ;;
  *"db query"*) printf 'abc_comments\tBASE TABLE\nabc_posts\tBASE TABLE\nabc_2_posts\tBASE TABLE\nabc_users\tBASE TABLE\nabc_shop_options\tBASE TABLE\nabc_shop_users\tBASE TABLE\nabc_options\tBASE TABLE\nabc_sessions\tBASE TABLE\nabc_2_sessions\tBASE TABLE\nabc_logs\tBASE TABLE\n' ;;
  *"db export"*) EXPORT ;;
esac; }`

func TestExportCommandsOrderLookupTablesFirstAndLeaveRowsBehind(t *testing.T) {
	wp := strings.NewReplacer("EXPORT", `echo "export $*" >&2`).Replace(fakeExportWP)
	_, stderr, err := runExportCommands(t, wp)
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "--tables=abc_users,abc_posts,abc_2_posts,abc_comments,abc_options\n") {
		t.Errorf("tables are not ordered for the filter:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--no-data --tables=abc_sessions,abc_2_sessions,abc_logs\n") {
		t.Errorf("dropped tables should export their structure only:\n%s", stderr)
	}
	if !strings.Contains(stderr, exportStatusMarker+"0") {
		t.Errorf("no status on stderr:\n%s", stderr)
	}
}

func TestExportCommandsAnnounceTheStreamAheadOfIt(t *testing.T) {
	wp := strings.NewReplacer("EXPORT", `echo "-- Dump completed"`).Replace(fakeExportWP)
	stdout, stderr, err := runExportCommands(t, wp)
	if err != nil {
		t.Fatalf("export failed: %v\n%s", err, stderr)
	}
	preamble, compressed, ok := bytes.Cut(stdout, []byte(dumpStreamMarker+"2\n"))
	if !ok {
		t.Fatalf("no announcement in %q", stdout)
	}
	if !bytes.Contains(preamble, []byte(tablePrefixMarker+"abc_")) {
		t.Errorf("preamble = %q", preamble)
	}
	if !bytes.HasPrefix(compressed, []byte{0x1f, 0x8b}) {
		t.Errorf("what follows is not gzip: %q", compressed[:min(len(compressed), 8)])
	}
}

func TestExportCommandsReportAFailedExport(t *testing.T) {
	wp := strings.NewReplacer("EXPORT", `echo partial; return 3`).Replace(fakeExportWP)
	_, stderr, _ := runExportCommands(t, wp)
	if !strings.Contains(stderr, exportStatusMarker+"3") {
		t.Errorf("a failed export went unnoticed:\n%s", stderr)
	}
}

// Without the prefix the filter could not find a single rule, so nothing must be exported.
func TestExportCommandsStopWithoutAPrefix(t *testing.T) {
	wp := `wp_ssh_wp() { case "$*" in *"db export"*) echo "export ran" >&2 ;; esac; }`
	_, stderr, err := runExportCommands(t, wp)
	if err == nil || strings.Contains(stderr, "export ran") {
		t.Fatalf("err = %v, stderr = %s", err, stderr)
	}
	if !strings.Contains(stderr, "cannot pseudonymize") {
		t.Errorf("stderr = %s", stderr)
	}
}

// The whole way a dump travels on this side: text, then gzip, then the filter, then gzip.
func TestFilterDumpStreamReadsThePreambleAndWritesGzip(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{})
	if err != nil {
		t.Fatal(err)
	}
	compressed := bytes.Buffer{}
	writer := gzip.NewWriter(&compressed)
	dump := "-- MariaDB dump\n" + usersTable + "INSERT INTO `wp_users` VALUES " + annaRow + ";\n-- Dump completed\n"
	if _, err := writer.Write([]byte(strings.ReplaceAll(dump, "wp_", "abc_"))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	stream := io.MultiReader(strings.NewReader("banner from a login script\n"+tablePrefixMarker+"abc_\n"+dumpStreamMarker+"1\n"), &compressed)
	output := bytes.Buffer{}
	preamble, report, err := filterDumpStream(context.Background(), stream, &output, spec)
	if err != nil {
		t.Fatalf("filterDumpStream() error = %v", err)
	}
	if !strings.Contains(preamble, "banner") || report["abc_users"].Rows != 1 {
		t.Errorf("preamble = %q, report = %+v", preamble, report)
	}
	reader, err := gzip.NewReader(&output)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(plain), "user-5@example.invalid", pseudonymizedOption)
	assertOmits(t, string(plain), "Anna@Real.test")
}

func TestFilterDumpStreamFailsOnACutOffStream(t *testing.T) {
	spec, err := buildPseudonymizeSpec(Config{})
	if err != nil {
		t.Fatal(err)
	}
	compressed := bytes.Buffer{}
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(strings.Repeat("-- MariaDB dump\n", 1000)))
	_ = writer.Close()
	cut := compressed.Bytes()[:compressed.Len()/2]
	for _, stream := range []io.Reader{
		strings.NewReader("no announcement\n"),
		strings.NewReader(dumpStreamMarker + "1\n"),
		io.MultiReader(strings.NewReader(tablePrefixMarker+"abc_\n"+dumpStreamMarker+"1\n"), bytes.NewReader(cut)),
		io.MultiReader(strings.NewReader(tablePrefixMarker+"abc_\n"+dumpStreamMarker+"1\n"), strings.NewReader("not gzip at all")),
	} {
		if _, _, err := filterDumpStream(context.Background(), stream, io.Discard, spec); err == nil {
			t.Errorf("a broken stream passed")
		}
	}
}

func TestPseudonymizedFilesStayBehindOnPullButNotOnClone(t *testing.T) {
	dir := t.TempDir()
	excludes, err := buildRsyncExcludes(dir, Config{CloneImages: true}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(excludes, "\n")
	for _, want := range []string{"*.sql.*", "wp-content/uploads/wc-logs/", "wp-content/uploads/sites/*/gravity_forms/", "wp-content/uploads/wp-personal-data-exports/"} {
		if !strings.Contains(joined, want) {
			t.Errorf("excludes miss %s:\n%s", want, joined)
		}
	}
	excludes, err = buildRsyncExcludes(dir, Config{SkipPseudonymize: true}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(excludes, "\n"), "wc-logs") {
		t.Errorf("a clone with real data must copy these files: %v", excludes)
	}
}

func TestReportNamesUncoveredTablesHoldingEmails(t *testing.T) {
	stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	app.reportPseudonymization(map[string]pseudonymizeTableReport{
		"abc_users": {Rows: 2, Rule: 1}, "abc_crm": {Rows: 4, Emails: 3, Other: 1}, "abc_clean": {Rows: 1},
	})
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "abc_crm (4)") || strings.Contains(output, "abc_clean") {
		t.Errorf("output = %s", output)
	}
}

func installPushProbeWP(t *testing.T, dir string, body string) {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n  *'config get table_prefix'*) echo abc_ ;;\n  *'db query'*) " + body + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "wp"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPushStopsWhenTheLocalDatabaseHoldsPseudonyms(t *testing.T) {
	dir := t.TempDir()
	installPushProbeWP(t, dir, `printf 'PHP Notice: stray\n2026-11-04T10:00:00Z\n'`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	err := app.refusePseudonymizedPush(context.Background(), dir, Config{})
	if err == nil || !strings.Contains(err.Error(), "2026-11-04T10:00:00Z") {
		t.Fatalf("refusePseudonymizedPush() = %v", err)
	}
	if err := app.refusePseudonymizedPush(context.Background(), dir, Config{AllowPseudonymizedPush: true}); err != nil {
		t.Fatalf("the override was ignored: %v", err)
	}
}

func TestPushContinuesWithoutTheMarker(t *testing.T) {
	dir := t.TempDir()
	installPushProbeWP(t, dir, `exit 0`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err := app.refusePseudonymizedPush(context.Background(), dir, Config{}); err != nil {
		t.Fatalf("refusePseudonymizedPush() = %v", err)
	}
}

// An unreadable database must not pass as one without the marker.
func TestPushStopsWhenTheMarkerCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	installPushProbeWP(t, dir, `exit 1`)
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	err := app.refusePseudonymizedPush(context.Background(), dir, Config{})
	if err == nil || !strings.Contains(err.Error(), "could not check") {
		t.Fatalf("refusePseudonymizedPush() = %v", err)
	}
}
