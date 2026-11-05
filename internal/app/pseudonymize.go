package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"
)

const (
	pseudonymizedOption = "wp_ssh_bridge_pseudonymized"
	// RFC 2606 reserves .invalid, so these addresses cannot reach anyone, even through a
	// development copy with a working mail setup.
	defaultPseudonymEmailDomain = "example.invalid"
)

var (
	validPseudonymEmailDomain = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	validPseudonymIdentifier  = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	pseudonymizedMarkerValue  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T`)
)

// PseudonymizeRule is a project's own rule for a table no adapter knows.
type PseudonymizeRule struct {
	Table string
	Drop  bool
	// Columns is comma-separated column=strategy pairs.
	Columns    string
	UserColumn string
}

func (rule PseudonymizeRule) tableRule() (tableRule, error) {
	if !validPseudonymIdentifier.MatchString(rule.Table) {
		return tableRule{}, fmt.Errorf("pseudonymize_rules: table %q must contain only letters, numbers, and underscores, without the table prefix", rule.Table)
	}
	if rule.Drop == (rule.Columns != "") {
		return tableRule{}, fmt.Errorf("pseudonymize_rules: table %q needs either columns or drop: true", rule.Table)
	}
	converted := tableRule{Name: rule.Table, Drop: rule.Drop}
	if rule.Drop {
		return converted, nil
	}
	converted.Columns = map[string]string{}
	identity := &identityRule{}
	if rule.UserColumn != "" {
		if !validPseudonymIdentifier.MatchString(rule.UserColumn) {
			return tableRule{}, fmt.Errorf("pseudonymize_rules: user_column %q of table %q must contain only letters, numbers, and underscores", rule.UserColumn, rule.Table)
		}
		identity.UserColumn = rule.UserColumn
	}
	for _, pair := range strings.Split(rule.Columns, ",") {
		column, strategy, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !validPseudonymIdentifier.MatchString(column) {
			return tableRule{}, fmt.Errorf("pseudonymize_rules: table %q has an invalid column entry %q; expected column=strategy", rule.Table, pair)
		}
		if !customRuleStrategies[strategy] {
			return tableRule{}, fmt.Errorf("pseudonymize_rules: table %q uses unknown strategy %q; choose one of %s", rule.Table, strategy, strings.Join(sortedStrategyNames(), ", "))
		}
		converted.Columns[column] = strategy
		// The email column names the person, so the row's other columns can follow it.
		if strategy == strategyEmail && identity.EmailColumn == "" {
			identity.EmailColumn = column
		}
	}
	// Left unset, so a rule on a table an adapter knows keeps how that adapter finds the person.
	if identity.UserColumn != "" || identity.EmailColumn != "" {
		converted.Identity = identity
	}
	return converted, nil
}

func sortedStrategyNames() []string {
	names := make([]string, 0, len(customRuleStrategies))
	for name := range customRuleStrategies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validatePseudonymize rejects values that end up in the filter's rules, so a pull reports a
// broken setting before it starts.
func (cfg Config) validatePseudonymize() error {
	if domain := cfg.PseudonymizeEmailDomain; domain != "" && (!validPseudonymEmailDomain.MatchString(domain) || !strings.Contains(domain, ".")) {
		return errors.New("pseudonymize_email_domain must be a hostname with a dot, for example example.invalid")
	}
	_, err := buildPseudonymizeSpec(cfg)
	return err
}

func (cfg Config) pseudonymizeKeepUserIDs() ([]int, error) {
	ids := []int{}
	for _, field := range strings.Split(cfg.PseudonymizeKeepUserIDs, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		id, err := strconv.Atoi(field)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("pseudonymize_keep_user_ids must be a comma-separated list of user IDs, not %q", cfg.PseudonymizeKeepUserIDs)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// keptField is one entry of pseudonymize_keep_fields: a column, or a key of a key/value table.
type keptField struct {
	table, name string
}

func (cfg Config) pseudonymizeKeepFields() ([]keptField, error) {
	fields := []keptField{}
	for _, entry := range strings.Split(cfg.PseudonymizeKeepFields, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		table, name, ok := strings.Cut(entry, ".")
		if !ok || name == "" || !validPseudonymIdentifier.MatchString(table) {
			return nil, fmt.Errorf("pseudonymize_keep_fields: %q must be table.column or table.meta_key, without the table prefix", entry)
		}
		fields = append(fields, keptField{table: table, name: name})
	}
	return fields, nil
}

// pseudonymizeKeepText reads the values to leave alone in free text. Email addresses have
// no place here: a pseudonymized copy can send mail, so they never stay by a setting that
// names a value. pseudonymize_keep_user_ids keeps an account's address on purpose.
func (cfg Config) pseudonymizeKeepText() ([]string, error) {
	texts := []string{}
	for _, entry := range strings.Split(cfg.PseudonymizeKeepText, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "@") || holdsAddress(entry) {
			return nil, errors.New("pseudonymize_keep_text cannot hold email addresses, in any spelling, which are always replaced; pseudonymize_keep_user_ids keeps an account as it is")
		}
		texts = append(texts, entry)
	}
	return texts, nil
}

// holdsAddress asks the scan itself, so the check knows every spelling the scan does.
func holdsAddress(text string) bool {
	registry, err := newIdentityRegistry(defaultPseudonymEmailDomain, nil)
	if err != nil {
		return true
	}
	scanner := &piiScanner{registry: registry, emailsOnly: true}
	scanner.scan([]byte(text))
	return scanner.emails > 0
}

// pseudonymizeSpec is everything the filter needs to know about a project.
type pseudonymizeSpec struct {
	EmailDomain string
	KeepUserIDs []int
	Tables      map[string]tableRule
	// Protected names, per table, the columns and keys that only the email scan may touch.
	Protected map[string]map[string]bool
	KeepText  []string
}

func buildPseudonymizeSpec(cfg Config) (pseudonymizeSpec, error) {
	keepIDs, err := cfg.pseudonymizeKeepUserIDs()
	if err != nil {
		return pseudonymizeSpec{}, err
	}
	adapters := pseudonymizeAdapters()
	project := pseudonymizeAdapter{}
	for _, rule := range cfg.PseudonymizeRules {
		converted, err := rule.tableRule()
		if err != nil {
			return pseudonymizeSpec{}, err
		}
		project.Tables = append(project.Tables, converted)
	}
	keepText, err := cfg.pseudonymizeKeepText()
	if err != nil {
		return pseudonymizeSpec{}, err
	}
	tables := mergeTableRules(append(adapters, project))
	protected := map[string]map[string]bool{}
	protect := func(table string, name string) {
		if protected[table] == nil {
			protected[table] = map[string]bool{}
		}
		protected[table][name] = true
	}
	for _, adapter := range adapters {
		for table, columns := range adapter.Protect {
			for _, column := range columns {
				protect(table, column)
			}
		}
	}
	keptFields, err := cfg.pseudonymizeKeepFields()
	if err != nil {
		return pseudonymizeSpec{}, err
	}
	for _, field := range keptFields {
		rule, known := tables[field.table]
		if known {
			// A setting that does nothing would pass for protection, or for a leak.
			if rule.Drop {
				return pseudonymizeSpec{}, fmt.Errorf("pseudonymize_keep_fields: %s.%s cannot stay, because a pull leaves that whole table out", field.table, field.name)
			}
			meta := rule.Meta
			_, ruled := rule.Columns[field.name]
			if meta != nil && !ruled {
				_, ruled = meta.Keys[field.name]
			}
			if ruled {
				return pseudonymizeSpec{}, fmt.Errorf("pseudonymize_keep_fields: %s.%s holds personal data by rule, so it cannot stay; pseudonymize_keep_user_ids keeps an account as it is", field.table, field.name)
			}
			if meta != nil && field.name != meta.KeyColumn && field.name != meta.ValueColumn && field.name != meta.ObjectColumn {
				meta.ProtectKeys = append(meta.ProtectKeys, field.name)
				continue
			}
		}
		protect(field.table, field.name)
	}
	return pseudonymizeSpec{
		EmailDomain: defaultString(cfg.PseudonymizeEmailDomain, defaultPseudonymEmailDomain),
		KeepUserIDs: keepIDs,
		Tables:      tables,
		Protected:   protected,
		KeepText:    keepText,
	}, nil
}

// pseudonymizedFileExcludes keeps files with personal data out of a pseudonymized pull,
// which would be pointless if they travelled next to the clean database. Plugin logs are
// already covered by the *.log rule.
func pseudonymizedFileExcludes() []string {
	excludes := []string{
		"*.sql",
		"*.sql.*",
		"wp-content/backup-db/",
		"wp-content/backups/",
		"wp-content/backups-dup-lite/",
		"wp-content/backups-dup-pro/",
		"wp-content/wpvividbackups/",
	}
	for _, directory := range []string{"backupbuddy_backups", "gravity_forms", "wpforms", "wc-logs", "woocommerce_uploads", "wp-personal-data-exports"} {
		excludes = append(excludes, "wp-content/uploads/"+directory+"/", "wp-content/uploads/sites/*/"+directory+"/")
	}
	return excludes
}

// pseudonymizeTableReport counts one table's work. Emails counts addresses found by the
// scan, Other everything else replaced.
type pseudonymizeTableReport struct {
	Rows    int
	Dropped int
	Emails  int
	Other   int
	Rule    int
	Missing []string
}

// reportPseudonymization summarizes the filter's work and names the tables no adapter
// covers where it found personal data: those may hold more that it cannot recognize, such
// as a name in free text, and need a project rule.
func (a *App) reportPseudonymization(report map[string]pseudonymizeTableReport) {
	rows, dropped := 0, 0
	uncovered, mismatched := []string{}, []string{}
	for table, counters := range report {
		rows += counters.Rows
		dropped += counters.Dropped
		if counters.Rule == 0 && counters.Emails+counters.Other > 0 {
			uncovered = append(uncovered, fmt.Sprintf("%s (%d)", table, counters.Emails+counters.Other))
		}
		for _, column := range counters.Missing {
			mismatched = append(mismatched, table+"."+column)
		}
	}
	sort.Strings(uncovered)
	sort.Strings(mismatched)
	a.UI.Info("Pseudonymized while downloading: %d rows read in %d tables, %d rows left out.", rows, len(report), dropped)
	if len(uncovered) > 0 {
		a.UI.Warning("Personal data was replaced in tables no pseudonymization rule covers: %s. These tables may hold more that the scan cannot recognize; add them to pseudonymize_rules.", strings.Join(uncovered, ", "))
	}
	if len(mismatched) > 0 {
		a.UI.Warning("Pseudonymization rules name columns these tables do not have, so nothing was replaced there: %s. Check pseudonymize_rules, or the plugin changed its schema.", strings.Join(mismatched, ", "))
	}
}

// realDataConfirmation is what a person types to pull real data.
const realDataConfirmation = "real data"

// confirmRealDataPull stands between a setting and the real data. A flag, a variable, or a
// line in a config file is one edit away for anything that runs this command, including an
// agent that merely wants the pull to work, so the decision needs a person at a terminal.
// --yes and --silent do not answer it: they speak for the routine, this is the exception.
// It is no boundary against deliberate access, since this SSH access reaches the database
// without this tool.
func (a *App) confirmRealDataPull(cfg Config) error {
	if !cfg.SkipPseudonymize {
		return nil
	}
	if !a.stdinIsTerminal() {
		return errors.New("pseudonymization is switched off, and pulling real personal data needs a person to confirm it in a terminal. Run this pull yourself, or remove --skip-pseudonymize, skip_pseudonymize, and WP_SSH_SKIP_PSEUDONYMIZE to pull pseudonyms")
	}
	fmt.Fprintf(a.Stdout, "Pseudonymization is off: this pull transfers real personal data from %s to this machine.\nType %q to continue: ", sshTarget(cfg.pullTarget()), realDataConfirmation)
	answer, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(answer) != realDataConfirmation {
		return errors.New("pull cancelled: real personal data was not confirmed")
	}
	return nil
}

func (a *App) stdinIsTerminal() bool {
	if a.stdinTerminal != nil {
		return a.stdinTerminal()
	}
	file, ok := a.Stdin.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// warnPseudonymizationOff says so on every run, because the real data then sits on this
// machine and it is easy to forget a setting made long ago.
func (a *App) warnPseudonymizationOff(cfg Config) {
	if cfg.SkipPseudonymize {
		a.UI.Warning("Pseudonymization is off: this pull transfers real personal data to this machine.")
	}
}

// refusePseudonymizedPush stops a push that would replace the target's real users,
// customers, and orders with pseudonyms, which no later command can undo.
func (a *App) refusePseudonymizedPush(ctx context.Context, root string, cfg Config) error {
	if cfg.AllowPseudonymizedPush {
		return nil
	}
	// db query needs no working WordPress, so a site that fails to load cannot pass as a
	// database without the marker. Without a readable prefix, the option is all there is.
	var output string
	var err error
	if prefix := a.localTablePrefix(ctx, root, cfg); prefix != "" {
		output, err = a.wpOutputSilent(ctx, root, cfg, "db", "query", "SELECT option_value FROM `"+prefix+"options` WHERE option_name = '"+pseudonymizedOption+"'", "--skip-column-names")
		if err != nil {
			return fmt.Errorf("could not check whether the local database holds pseudonyms, so the push stops: %w; pass --allow-pseudonymized-push when the target's data may be overwritten", err)
		}
	} else if output, err = a.wpOutputSilent(ctx, root, cfg, "option", "get", pseudonymizedOption); err != nil {
		return nil
	}
	lines := nonEmptyLines(output)
	if len(lines) == 0 || !pseudonymizedMarkerValue.MatchString(lines[len(lines)-1]) {
		return nil
	}
	return fmt.Errorf("the local database holds pseudonyms from a pull on %s, and pushing it would replace the target's real users, customers, and orders; use --skip-db to push files only, or --allow-pseudonymized-push when the target's data may be overwritten", lines[len(lines)-1])
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
