package app

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// providerInfo stays quiet so DDEV provider runs only show completed steps and errors.
func (a *App) providerInfo(cfg Config) error {
	return nil
}

// providerAuth verifies upstream key authentication with the actual SSH command.
func (a *App) providerAuth(ctx context.Context, projectRoot string, cfg Config) error {
	tested := false
	source := cfg.pullTarget()
	if source.configured() {
		if err := cfg.validatePullRequired(); err != nil {
			return err
		}
		if err := a.authenticateTarget(ctx, projectRoot, source, "pull source"); err != nil {
			return err
		}
		if err := a.ensureRemoteWPCLI(ctx, projectRoot, source, "pull source"); err != nil {
			return err
		}
		tested = true
	}
	target := cfg.pushTarget()
	if target.configured() {
		if err := cfg.validatePushRequired(); err != nil {
			return err
		}
		if err := a.authenticateTarget(ctx, projectRoot, target, "push target"); err != nil {
			return err
		}
		if err := a.ensureRemoteWPCLI(ctx, projectRoot, target, "push target"); err != nil {
			return err
		}
		tested = true
	}
	if tested {
		return a.ensureLocalWPCLI(ctx, projectRoot, cfg)
	}
	return cfg.validatePullRequired()
}

// authenticateTarget verifies SSH authentication before destructive sync steps run.
func (a *App) authenticateTarget(ctx context.Context, projectRoot string, target RemoteTarget, label string) error {
	return a.runStep("Logging into "+label+" over SSH", "Logged into "+label+" over SSH", func() error {
		return a.runSSHQuietSuccess(ctx, projectRoot, target, "true")
	})
}

// dbPull exports the upstream database and downloads it to the runtime scratch path.
func (a *App) dbPull(ctx context.Context, projectRoot string, cfg Config) error {
	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	downloadDir := downloadsDir(projectRoot)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return err
	}

	remoteTmp := trimTrailingSlash(defaultString(target.RemoteTmpDir, "/tmp"))
	remoteWP := trimTrailingSlash(target.RemotePath)
	projectName := firstNonEmpty(os.Getenv("DDEV_PROJECT"), filepath.Base(projectRoot), "wordpress")
	dumpID := randomID()
	remoteDump := fmt.Sprintf("%s/ddev-%s-%s-%s.sql", remoteTmp, projectName, time.Now().Format("20060102150405"), dumpID)
	remoteDumpGZ := remoteDump + ".gz"

	remoteCommand := strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { rm -f %s %s; };", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		"trap cleanup INT TERM HUP EXIT;",
		"cd " + shellQuote(remoteWP) + ";",
		remoteWPCLIPrelude(target),
		fmt.Sprintf("rm -f %s %s;", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		fmt.Sprintf("wp_ssh_wp --allow-root db export %s;", shellQuote(remoteDump)),
		fmt.Sprintf("gzip -f %s;", shellQuote(remoteDump)),
		"trap - EXIT",
	}, " ")
	if err := a.runStep("Exporting pull source database", "Pull source database exported", func() error {
		return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, remoteCommand)
	}); err != nil {
		return err
	}

	args := append(rsyncArchiveArgs(), "-e", sshCommandString(target), sshTarget(target)+":"+remoteDumpGZ, filepath.Join(downloadDir, "db.sql.gz"))
	if err := a.runStep("Downloading database export", "Database export downloaded", func() error {
		return a.runExternal(ctx, projectRoot, "rsync", args...)
	}); err != nil {
		return err
	}

	return a.runStep("Cleaning up remote database export", "Remote database export removed", func() error {
		return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, fmt.Sprintf("rm -f %s %s", shellQuote(remoteDump), shellQuote(remoteDumpGZ)))
	})
}

// filesPull rsyncs the remote WordPress tree directly into the local project.
func (a *App) filesPull(ctx context.Context, projectRoot string, cfg Config, preserveLocalWPConfig bool) error {
	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	destination := localWPRoot(projectRoot, cfg)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	args := append(rsyncArchiveArgs(), "--delete", "--safe-links")
	excludes, err := buildRsyncExcludes(projectRoot, cfg, preserveLocalWPConfig)
	if err != nil {
		return err
	}
	for _, exclude := range excludes {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, "-e", sshCommandString(target), sshTarget(target)+":"+trimTrailingSlash(target.RemotePath)+"/", destination+"/")

	return a.runStep("Syncing WordPress files from pull source", "WordPress files synced", func() error {
		return a.runExternalAllowRsyncVanished(ctx, projectRoot, args...)
	})
}

func (a *App) runExternalAllowRsyncVanished(ctx context.Context, projectRoot string, args ...string) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	err := a.runExternalWithWriters(ctx, projectRoot, "rsync", &stdout, &stderr, args...)
	if isRsyncVanishedError(err) {
		a.writeCapturedOutput("rsync", stdout.String(), false)
		a.writeCapturedOutput("rsync", stderr.String(), true)
		a.UI.Warning("Continuing after rsync warning: remote files vanished during transfer.")
		return nil
	}
	if err != nil {
		a.writeCapturedOutput("rsync", stdout.String(), false)
		a.writeCapturedOutput("rsync", stderr.String(), true)
	}
	return err
}

func isRsyncVanishedError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError) && exitError.ExitCode() == 24
}

// filesImport is intentionally a no-op because filesPull syncs directly.
func (a *App) filesImport() {
	_ = a.runStep("Checking file import", "File import skipped; files already synced", func() error {
		return nil
	})
}

// postPull applies local cleanup after a database or file pull.
func (a *App) postPull(ctx context.Context, adapter runtimeAdapter, cfg Config) error {
	projectRoot := adapter.Root()
	for _, hook := range adapter.PostPullHooks() {
		if err := hook(ctx, a, projectRoot, cfg); err != nil {
			return err
		}
	}
	if err := a.updateWPConfigURLConstants(projectRoot, cfg, adapter.Mode()); err != nil {
		return err
	}
	if err := a.replaceSiteURLs(ctx, projectRoot, cfg); err != nil {
		return err
	}
	return a.removeBlockedPlugins(ctx, projectRoot, cfg, "")
}

// buildRsyncExcludes keeps parity with the original shell provider exclude set.
func buildRsyncExcludes(projectRoot string, cfg Config, preserveLocalWPConfig bool) ([]string, error) {
	excludes := []string{
		".git/",
		".ddev/",
		"wp-config-ddev.php",
		"wp-content/cache/",
		"wp-content/debug.log",
		"wp-content/upgrade/",
		"wp-content/updraft/",
		"wp-content/ai1wm-backups/",
		"wp-content/languages/wpml/queue/",
	}

	if preserveLocalWPConfig {
		excludes = append(excludes, "wp-config.php")
	}

	if !cfg.CloneImages {
		excludes = append(excludes, "wp-content/uploads/")
	}

	plugins, err := readPluginList(projectRoot, cfg)
	if err != nil {
		return nil, err
	}
	for _, plugin := range plugins {
		pluginDir := normalizePluginSlug(plugin)
		if pluginDir == "" {
			continue
		}
		excludes = append(excludes, "wp-content/plugins/"+pluginDir+"/")
		excludes = append(excludes, "wp-content/plugins/"+pluginDir+".php")
	}

	return excludes, nil
}

// rsyncArchiveArgs stays compatible with macOS' bundled rsync, which lacks -s/--protect-args.
func rsyncArchiveArgs() []string {
	return []string{"-az"}
}

// updateWPConfigURLConstants keeps hardcoded WordPress URL constants from overriding local URLs.
func (a *App) updateWPConfigURLConstants(projectRoot string, cfg Config, mode runtimeMode) error {
	wpConfig := filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")
	contents, err := os.ReadFile(wpConfig)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	updated := updateWPConfigURLConstantsContents(string(contents), wpConfigURLConstantValues(projectRoot, cfg, mode))
	if updated == string(contents) {
		return nil
	}
	return a.runStep("Updating local WordPress URL constants", "Local WordPress URL constants updated", func() error {
		return os.WriteFile(wpConfig, []byte(updated), 0o644)
	})
}

type wpConfigURLConstants struct {
	Home              string
	SiteURL           string
	DomainCurrentSite string
}

func wpConfigURLConstantValues(projectRoot string, cfg Config, mode runtimeMode) wpConfigURLConstants {
	localURL := preferredLocalURL(projectRoot, cfg)
	localHost := replacementHost(localURL)
	if localHost == "" {
		localHost = "localhost"
	}
	if mode == modeDDEV {
		fallbackURL := phpStringLiteral(firstNonEmpty(localURL, "https://"+localHost))
		ddevURL := "getenv('DDEV_PRIMARY_URL_WITHOUT_PORT') ?: getenv('DDEV_PRIMARY_URL') ?: " + fallbackURL
		return wpConfigURLConstants{
			Home:              ddevURL,
			SiteURL:           ddevURL,
			DomainCurrentSite: "parse_url(" + ddevURL + ", PHP_URL_HOST)",
		}
	}
	if localURL == "" {
		localURL = "https://" + localHost
	}
	return wpConfigURLConstants{
		Home:              phpStringLiteral(localURL),
		SiteURL:           phpStringLiteral(localURL),
		DomainCurrentSite: phpStringLiteral(localHost),
	}
}

func preferredLocalURL(projectRoot string, cfg Config) string {
	if cfg.LocalURL != "" {
		return trimTrailingSlash(cfg.LocalURL)
	}
	for _, replacement := range cfg.PullDomainReplacements {
		if urlBase(replacement.New) != "" {
			return trimTrailingSlash(replacement.New)
		}
	}
	for _, replacement := range cfg.PushDomainReplacements {
		if urlBase(replacement.Old) != "" {
			return trimTrailingSlash(replacement.Old)
		}
	}
	return localSiteURL(projectRoot, cfg)
}

func updateWPConfigURLConstantsContents(contents string, values wpConfigURLConstants) string {
	defines := []struct {
		name  string
		value string
	}{
		{name: "WP_HOME", value: values.Home},
		{name: "WP_SITEURL", value: values.SiteURL},
		{name: "DOMAIN_CURRENT_SITE", value: values.DomainCurrentSite},
	}
	insertions := []string{}
	for _, define := range defines {
		updated, replaced := replaceWPConfigDefine(contents, define.name, define.value)
		if replaced {
			contents = updated
			continue
		}
		insertions = append(insertions, wpConfigDefineLine(define.name, define.value))
	}
	if len(insertions) == 0 {
		return contents
	}
	snippet := "\n" + strings.Join(insertions, "")
	stopEditing := regexp.MustCompile(`\n\s*/\* That(?:\\'|'|\x{2019})s all, stop editing! Happy publishing\. \*/`)
	if loc := stopEditing.FindStringIndex(contents); loc != nil {
		return contents[:loc[0]] + snippet + contents[loc[0]:]
	}
	return strings.TrimRight(contents, "\r\n") + snippet
}

func replaceWPConfigDefine(contents string, name string, value string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	if !pattern.MatchString(contents) {
		return contents, false
	}
	return pattern.ReplaceAllString(contents, wpConfigDefineLine(name, value)), true
}

func wpConfigDefineLine(name string, value string) string {
	return "define('" + name + "', " + value + ");\n"
}

func phpStringLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

// sanitizeWPConfig removes production DB constants and adds DDEV-safe settings.
func (a *App) sanitizeWPConfig(projectRoot string, cfg Config) error {
	wpConfig := filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")
	contents, err := os.ReadFile(wpConfig)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	updated := sanitizeWPConfigContents(string(contents))
	return a.runStep("Sanitizing local wp-config.php", "Local wp-config.php sanitized", func() error {
		return os.WriteFile(wpConfig, []byte(updated), 0o644)
	})
}

// sanitizeWPConfigContents is pure so config rewrite behavior is testable.
func sanitizeWPConfigContents(contents string) string {
	dbDefine := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]DB_[A-Z0-9_]+['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	contents = dbDefine.ReplaceAllString(contents, "")
	wpDebugDefine := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]WP_DEBUG['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	contents = wpDebugDefine.ReplaceAllString(contents, "")
	contents = ensureDDEVConfigIncludeAtTop(contents)

	cookieDomain := regexp.MustCompile(`define\(\s*['"]COOKIE_DOMAIN['"]\s*,\s*\$_SERVER\s*\[\s*['"]HTTP_HOST['"]\s*\]\s*\);`)
	contents = cookieDomain.ReplaceAllLiteralString(contents, "define('COOKIE_DOMAIN', $_SERVER['HTTP_HOST'] ?? '');")

	envType := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]WP_ENVIRONMENT_TYPE['"]\s*,\s*.*?\);`)
	if envType.MatchString(contents) {
		contents = envType.ReplaceAllString(contents, "define('WP_ENVIRONMENT_TYPE', 'development');")
	} else {
		snippet := "\ndefine('WP_ENVIRONMENT_TYPE', 'development');\n"
		stopEditing := regexp.MustCompile(`\n\s*/\* That(?:\\'|'|\x{2019})s all, stop editing! Happy publishing\. \*/`)
		if loc := stopEditing.FindStringIndex(contents); loc != nil {
			contents = contents[:loc[0]] + snippet + contents[loc[0]:]
		} else {
			contents = strings.TrimRight(contents, "\r\n") + snippet
		}
	}

	return contents
}

func ensureDDEVConfigIncludeAtTop(contents string) string {
	if hasDDEVConfigInclude(contents) {
		return contents
	}

	openingTag := regexp.MustCompile(`(?s)\A(\s*<\?php[^\r\n]*(?:\r?\n)?)`)
	if loc := openingTag.FindStringSubmatchIndex(contents); loc != nil {
		prefix := contents[:loc[3]]
		if !strings.HasSuffix(prefix, "\n") {
			prefix += "\n"
		}
		return prefix + ddevConfigIncludeSnippet + strings.TrimLeft(contents[loc[3]:], "\r\n")
	}

	return ddevConfigIncludeSnippet + strings.TrimLeft(contents, "\r\n")
}

func hasDDEVConfigInclude(contents string) bool {
	return strings.Contains(contents, "wp-config-ddev.php")
}

const ddevConfigIncludeSnippet = `// Include for DDEV-managed settings in wp-config-ddev.php.
$ddev_settings = __DIR__ . '/wp-config-ddev.php';
if (is_readable($ddev_settings) && !defined('DB_USER')) {
    require_once($ddev_settings);
}

`

// replaceSiteURLs updates single-site and multisite URLs after database import.
func (a *App) replaceSiteURLs(ctx context.Context, projectRoot string, cfg Config) error {
	if cfg.SkipSearchReplace {
		return nil
	}

	wpRoot := localWPRoot(projectRoot, cfg)
	if _, err := os.Stat(filepath.Join(wpRoot, "wp-config.php")); err != nil {
		return nil
	}

	pairs := []replacementPair{}
	for _, configured := range cfg.PullDomainReplacements {
		pairs = append(pairs, replacementPairsForConfiguredDomain(configured)...)
	}

	oldURL := firstNonEmpty(a.wpOutput(ctx, projectRoot, cfg, "option", "get", "home"), a.wpOutput(ctx, projectRoot, cfg, "option", "get", "siteurl"))
	newURL := localSiteURL(projectRoot, cfg)
	if oldURL != "" && newURL != "" {
		autoPairs := replacementPairsForURLs(oldURL, newURL)
		if len(autoPairs) == 0 {
			a.UI.Warning("Skipping automatic WordPress URL replacement because the old or new URL is invalid.")
		}
		pairs = append(pairs, autoPairs...)
	}
	if len(pairs) == 0 {
		a.UI.Warning("Skipping WordPress URL replacement because no configured replacement pairs exist and the old or new URL could not be detected.")
		return nil
	}
	pairs = uniqueReplacementPairs(pairs)

	if a.isMultisite(ctx, projectRoot, cfg) {
		if err := a.runMultisiteSearchReplace(ctx, projectRoot, cfg, pairs); err != nil {
			return err
		}
		for _, pair := range pairs {
			if err := a.replaceMultisiteDomains(ctx, projectRoot, cfg, pair.old, pair.new); err != nil {
				return err
			}
		}
		return nil
	}

	for _, pair := range pairs {
		if err := a.runSearchReplace(ctx, projectRoot, cfg, pair.old, pair.new); err != nil {
			return err
		}
	}
	return nil
}

type replacementPair struct {
	old string
	new string
}

func uniqueReplacementPairs(pairs []replacementPair) []replacementPair {
	seen := map[string]bool{}
	unique := []replacementPair{}
	for _, pair := range pairs {
		if pair.old == "" || pair.new == "" || pair.old == pair.new {
			continue
		}
		key := pair.old + "\x00" + pair.new
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, pair)
	}
	sort.SliceStable(unique, func(i int, j int) bool {
		return len(unique[i].old) > len(unique[j].old)
	})
	return unique
}

func replacementPairsForURLs(oldURL string, newURL string) []replacementPair {
	oldBase := urlBase(oldURL)
	newBase := urlBase(newURL)
	if oldBase == "" || newBase == "" {
		return nil
	}
	hostPart := strings.TrimPrefix(strings.TrimPrefix(oldBase, "http://"), "https://")
	newHost := replacementHost(newBase)
	return []replacementPair{
		{old: oldURL, new: newURL},
		{old: "http://" + hostPart, new: newBase},
		{old: "https://" + hostPart, new: newBase},
		{old: hostPart, new: newHost},
	}
}

func replacementPairsForConfiguredDomain(replacement DomainReplacement) []replacementPair {
	oldValue := strings.TrimSpace(replacement.Old)
	newValue := strings.TrimSpace(replacement.New)
	if oldValue == "" || newValue == "" {
		return nil
	}
	pairs := []replacementPair{{old: oldValue, new: newValue}}
	oldHost := replacementHost(oldValue)
	newHost := replacementHost(newValue)
	if oldHost == "" || newHost == "" {
		return pairs
	}
	if newBase := urlBase(newValue); newBase != "" {
		pairs = append(pairs,
			replacementPair{old: "http://" + oldHost, new: newBase},
			replacementPair{old: "https://" + oldHost, new: newBase},
			replacementPair{old: oldHost, new: newHost},
		)
		return pairs
	}
	pairs = append(pairs,
		replacementPair{old: "http://" + oldHost, new: "http://" + newHost},
		replacementPair{old: "https://" + oldHost, new: "https://" + newHost},
		replacementPair{old: oldHost, new: newHost},
	)
	return pairs
}

func replacementHost(value string) string {
	if host := urlHost(urlBase(value)); host != "" {
		return host
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "/ \t\r\n") {
		return ""
	}
	host, _, ok := strings.Cut(value, ":")
	if ok {
		return host
	}
	return value
}

func replacementLabel(value string) string {
	if host := replacementHost(value); host != "" {
		return host
	}
	return strings.TrimSpace(value)
}

// runSearchReplace delegates serialized WordPress updates to WP-CLI.
func (a *App) runSearchReplace(ctx context.Context, projectRoot string, cfg Config, oldValue string, newValue string) error {
	if oldValue == "" || newValue == "" || oldValue == newValue {
		return nil
	}
	title := fmt.Sprintf("Replacing WordPress URLs: %s -> %s", oldValue, newValue)
	done := fmt.Sprintf("WordPress URL replacement finished (%s)", replacementLabel(oldValue))
	return a.runStep(title, done, func() error {
		return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "search-replace", oldValue, newValue, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--report-changed-only")
	})
}

func (a *App) runMultisiteSearchReplace(ctx context.Context, projectRoot string, cfg Config, pairs []replacementPair) error {
	siteURLs := a.multisiteSiteURLs(ctx, projectRoot, cfg)
	if len(siteURLs) == 0 {
		for _, pair := range pairs {
			if err := a.runSearchReplace(ctx, projectRoot, cfg, pair.old, pair.new); err != nil {
				return err
			}
		}
		return nil
	}
	for _, siteURL := range siteURLs {
		for _, pair := range pairs {
			if err := a.runSearchReplaceForSite(ctx, projectRoot, cfg, siteURL, pair.old, pair.new); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) runSearchReplaceForSite(ctx context.Context, projectRoot string, cfg Config, siteURL string, oldValue string, newValue string) error {
	if oldValue == "" || newValue == "" || oldValue == newValue {
		return nil
	}
	title := fmt.Sprintf("Replacing WordPress URLs for %s: %s -> %s", siteURL, oldValue, newValue)
	done := fmt.Sprintf("WordPress URL replacement finished (%s)", replacementLabel(siteURL))
	return a.runStep(title, done, func() error {
		return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "--url="+siteURL, "search-replace", oldValue, newValue, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--report-changed-only")
	})
}

func (a *App) multisiteSiteURLs(ctx context.Context, projectRoot string, cfg Config) []string {
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "site", "list", "--field=url")
	if err != nil {
		return nil
	}
	siteURLs := []string{}
	for _, line := range strings.Split(output, "\n") {
		siteURL := strings.TrimSpace(line)
		if siteURL != "" {
			siteURLs = append(siteURLs, siteURL)
		}
	}
	return siteURLs
}

// replaceMultisiteDomains updates wp_site and wp_blogs domain columns when present.
func (a *App) replaceMultisiteDomains(ctx context.Context, projectRoot string, cfg Config, oldBase string, newBase string) error {
	oldDomain := replacementHost(oldBase)
	newDomain := replacementHost(newBase)
	if oldDomain == "" || newDomain == "" || oldDomain == newDomain {
		return nil
	}
	if !a.isMultisite(ctx, projectRoot, cfg) {
		return nil
	}

	prefix := a.wpOutput(ctx, projectRoot, cfg, "db", "prefix")
	if prefix == "" || regexp.MustCompile(`[^A-Za-z0-9_]`).MatchString(prefix) {
		return nil
	}

	for _, table := range []string{prefix + "site", prefix + "blogs"} {
		if !a.wpTableExists(ctx, projectRoot, cfg, table) {
			continue
		}
		query := fmt.Sprintf("UPDATE `%s` SET domain = %s WHERE domain = %s", table, sqlQuote(newDomain), sqlQuote(oldDomain))
		title := fmt.Sprintf("Replacing WordPress multisite domains in %s", table)
		if err := a.runStep(title, "WordPress multisite domains replaced in "+table, func() error {
			return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, "db", "query", query)
		}); err != nil {
			return err
		}
	}
	return nil
}

// isMultisite avoids network-table updates for regular WordPress installs.
func (a *App) isMultisite(ctx context.Context, projectRoot string, cfg Config) bool {
	if err := a.runWPSilent(ctx, projectRoot, cfg, "core", "is-installed", "--network"); err == nil {
		return true
	}
	value, err := a.wpOutputSilent(ctx, projectRoot, cfg, "config", "get", "MULTISITE")
	return err == nil && isTruthyConfigValue(value)
}

// wpTableExists checks table presence through WP-CLI.
func (a *App) wpTableExists(ctx context.Context, projectRoot string, cfg Config, table string) bool {
	output, err := a.wpOutputSilent(ctx, projectRoot, cfg, "db", "tables", "--all-tables-with-prefix", "--format=csv")
	if err != nil {
		return false
	}
	return lineSetContains(output, table)
}

// removeBlockedPlugins removes listed local-only plugins through WP-CLI when WordPress knows them.
func (a *App) removeBlockedPlugins(ctx context.Context, projectRoot string, cfg Config, overrideRoot string) error {
	if overrideRoot != "" {
		cfg.LocalWPPath = overrideRoot
		if filepath.IsAbs(overrideRoot) {
			if rel, ok := projectRelativePath(projectRoot, overrideRoot); ok {
				cfg.LocalWPPath = rel
			}
		}
	}

	return a.runStepResult("Cleaning up local-only blocked plugins", func() (string, error) {
		plugins, err := readPluginList(projectRoot, cfg)
		if err != nil {
			return "", err
		}

		statuses, err := a.listedPluginStatuses(ctx, projectRoot, cfg)
		if err != nil {
			return "", err
		}
		if len(statuses) == 0 {
			return "No local WordPress plugins found", nil
		}

		targets := blockedPluginRemovalTargets(plugins, statuses)
		if len(targets) == 0 {
			return "No local-only blocked plugins found", nil
		}

		if err := a.deleteBlockedPluginsWithWPCLI(ctx, projectRoot, cfg, targets, statuses); err != nil {
			return "", err
		}
		return fmt.Sprintf("Removed %d local-only blocked %s", len(targets), pluginNoun(len(targets))), nil
	})
}

// importLocalDB imports the downloaded gzip dump through the local WP-CLI runtime.
func (a *App) importLocalDB(ctx context.Context, root string, cfg Config) error {
	dumpPath := filepath.Join(downloadsDir(root), "db.sql.gz")
	file, err := os.Open(dumpPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tempFile, err := os.CreateTemp(downloadsDir(root), "db-*.sql")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := io.Copy(tempFile, gzipReader); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	importPath := tempPath
	if _, ok := ddevDescribe(root); ok {
		if containerPath, ok := containerProjectPath(root, tempPath); ok {
			importPath = containerPath
		}
	}
	return a.runStep("Importing database into local WordPress", "Local database imported", func() error {
		return a.runWPWithFilteredWarnings(ctx, root, cfg, "db", "import", importPath)
	})
}

// readPluginList returns embedded default plugins plus an optional custom block list.
func readPluginList(projectRoot string, cfg Config) ([]string, error) {
	plugins, err := parsePluginList(strings.NewReader(defaultPluginList), "embedded default plugin list")
	if err != nil {
		return nil, err
	}
	if cfg.PluginRemoveFile == "" {
		return plugins, nil
	}

	path := pluginListPath(projectRoot, cfg)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return plugins, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	custom, err := parsePluginList(file, path)
	if err != nil {
		return nil, err
	}
	return append(plugins, custom...), nil
}

func parsePluginList(reader io.Reader, source string) ([]string, error) {
	plugins := []string{}
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		plugin := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if plugin == "" {
			continue
		}
		if strings.HasPrefix(plugin, "/") || strings.Contains(plugin, "..") || strings.ContainsAny(plugin, " \t") || !validPluginPath.MatchString(plugin) {
			return nil, fmt.Errorf("invalid plugin entry %q in %s; use a plugin slug or basename", plugin, source)
		}
		plugins = append(plugins, plugin)
	}
	return plugins, scanner.Err()
}

// normalizePluginSlug converts plugin basenames and nested paths to plugin slugs.
func normalizePluginSlug(plugin string) string {
	plugin = strings.SplitN(plugin, "/", 2)[0]
	plugin = strings.TrimSuffix(plugin, ".php")
	return plugin
}

// listedPluginStatuses returns the plugin state reported by `wp plugin list`.
func (a *App) listedPluginStatuses(ctx context.Context, projectRoot string, cfg Config) (map[string]string, error) {
	statuses := map[string]string{}
	if _, err := os.Stat(filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")); err != nil {
		return statuses, nil
	}

	output, err := a.wpOutputErr(ctx, projectRoot, cfg, "plugin", "list", "--format=json", "--fields=name,status")
	if err != nil {
		return statuses, fmt.Errorf("list local plugins with WP-CLI: %w", err)
	}
	return parsePluginStatuses(output)
}

func parsePluginStatuses(output string) (map[string]string, error) {
	statuses := map[string]string{}
	var plugins []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	payload, err := jsonPayload(output)
	if err != nil {
		return statuses, err
	}
	if err := json.Unmarshal([]byte(payload), &plugins); err != nil {
		return statuses, err
	}
	for _, plugin := range plugins {
		if plugin.Name != "" {
			statuses[plugin.Name] = plugin.Status
		}
	}
	return statuses, nil
}

func jsonPayload(output string) (string, error) {
	for index, char := range output {
		if char != '[' && char != '{' {
			continue
		}
		var raw json.RawMessage
		if err := json.NewDecoder(strings.NewReader(output[index:])).Decode(&raw); err == nil {
			return string(raw), nil
		}
	}
	return "", fmt.Errorf("WP-CLI output did not contain JSON")
}

func blockedPluginRemovalTargets(plugins []string, statuses map[string]string) []string {
	seen := map[string]bool{}
	targets := []string{}
	for _, plugin := range plugins {
		slug := normalizePluginSlug(plugin)
		if slug == "" || seen[slug] {
			continue
		}
		if _, ok := statuses[slug]; !ok {
			continue
		}
		seen[slug] = true
		targets = append(targets, slug)
	}
	sort.Strings(targets)
	return targets
}

func (a *App) deleteBlockedPluginsWithWPCLI(ctx context.Context, projectRoot string, cfg Config, plugins []string, statuses map[string]string) error {
	if active := pluginsWithStatus(plugins, statuses, "active"); len(active) > 0 {
		args := append([]string{"plugin", "deactivate"}, active...)
		args = append(args, "--quiet")
		if err := a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, args...); err != nil {
			return err
		}
	}
	if networkActive := pluginsWithStatus(plugins, statuses, "active-network"); len(networkActive) > 0 {
		args := append([]string{"plugin", "deactivate"}, networkActive...)
		args = append(args, "--network", "--quiet")
		if err := a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, args...); err != nil {
			return err
		}
	}

	args := append([]string{"plugin", "delete"}, plugins...)
	args = append(args, "--quiet")
	return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, args...)
}

func pluginsWithStatus(plugins []string, statuses map[string]string, status string) []string {
	matching := []string{}
	for _, plugin := range plugins {
		if statuses[plugin] == status {
			matching = append(matching, plugin)
		}
	}
	return matching
}

func pluginNoun(count int) string {
	if count == 1 {
		return "plugin"
	}
	return "plugins"
}

func (a *App) runWPWithFilteredWarnings(ctx context.Context, projectRoot string, cfg Config, args ...string) error {
	name, fullArgs := localWPCommand(projectRoot, cfg, args...)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	filteredStderr := newDuplicateSummaryWriter(&stderr, isRepeatedWarningLine, "Warning: repeated similar warnings suppressed")
	err := a.runExternalWithWriters(ctx, projectRoot, name, &stdout, filteredStderr, fullArgs...)
	flushPrefixed(filteredStderr)
	if err != nil {
		label := commandLabel(name)
		a.writeCapturedOutput(label, stdout.String(), false)
		a.writeCapturedOutput(label, stderr.String(), true)
	}
	return err
}

func (a *App) runWPSilent(ctx context.Context, projectRoot string, cfg Config, args ...string) error {
	name, fullArgs := localWPCommand(projectRoot, cfg, args...)
	return a.runExternalWithWriters(ctx, projectRoot, name, io.Discard, io.Discard, fullArgs...)
}

// wpOutput returns trimmed WP-CLI output and suppresses command failures.
func (a *App) wpOutput(ctx context.Context, projectRoot string, cfg Config, args ...string) string {
	output, err := a.wpOutputErr(ctx, projectRoot, cfg, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// wpOutputErr returns raw WP-CLI output for callers that need error handling.
func (a *App) wpOutputErr(ctx context.Context, projectRoot string, cfg Config, args ...string) (string, error) {
	name, fullArgs := localWPCommand(projectRoot, cfg, args...)
	return a.outputExternal(ctx, projectRoot, name, fullArgs...)
}

// wpOutputSilent captures expected probe output without surfacing non-fatal stderr noise.
func (a *App) wpOutputSilent(ctx context.Context, projectRoot string, cfg Config, args ...string) (string, error) {
	name, fullArgs := localWPCommand(projectRoot, cfg, args...)
	return a.outputExternalWithStderr(ctx, projectRoot, name, io.Discard, fullArgs...)
}

// localWPCommand selects DDEV's WP-CLI proxy only when DDEV describe succeeds.
func localWPCommand(projectRoot string, cfg Config, args ...string) (string, []string) {
	if _, ok := ddevDescribe(projectRoot); ok {
		fullArgs := append([]string{"wp", "--path=" + containerWPPath(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes"}, args...)
		return "ddev", fullArgs
	}
	name, baseArgs := localWPCLICommand(projectRoot)
	fullArgs := append(baseArgs, "--path="+localWPRoot(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes")
	fullArgs = append(fullArgs, args...)
	return name, fullArgs
}

// runExternal runs a local executable without invoking a local shell.
func (a *App) runExternal(ctx context.Context, dir string, name string, args ...string) error {
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	err := a.runExternalWithWriters(ctx, dir, name, &stdout, &stderr, args...)
	if err != nil {
		label := commandLabel(name)
		a.writeCapturedOutput(label, stdout.String(), false)
		a.writeCapturedOutput(label, stderr.String(), true)
	}
	return err
}

func (a *App) runExternalWithWriters(ctx context.Context, dir string, name string, stdout io.Writer, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = a.Stdin
	return cmd.Run()
}

// outputExternal captures command output while preserving stderr for diagnostics.
func (a *App) outputExternal(ctx context.Context, dir string, name string, args ...string) (string, error) {
	stderr := bytes.Buffer{}
	output, err := a.outputExternalWithStderr(ctx, dir, name, &stderr, args...)
	if err != nil {
		return output, commandOutputError{err: err, stderr: stderr.String()}
	}
	return output, nil
}

func (a *App) outputExternalWithStderr(ctx context.Context, dir string, name string, stderr io.Writer, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	return stdout.String(), err
}

func commandLabel(name string) string {
	switch filepath.Base(name) {
	case "rsync":
		return "rsync"
	case "ddev":
		return "ddev"
	case "wp":
		return "wp"
	case "php", wpCLIPharName:
		return "wp"
	default:
		return "local"
	}
}

func flushPrefixed(writer io.Writer) {
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

func lineSetContains(output string, value string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == value {
			return true
		}
	}
	return false
}

func isTruthyConfigValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isRepeatedWarningLine(line string) bool {
	normalized := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(normalized, "warning:") || strings.HasPrefix(normalized, "php warning:")
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func trimTrailingSlash(value string) string {
	value = strings.TrimSpace(value)
	for len(value) > 1 && strings.HasSuffix(value, "/") {
		value = strings.TrimSuffix(value, "/")
	}
	return value
}

func urlBase(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func urlHost(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func sqlQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

func randomID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "local"
	}
	return hex.EncodeToString(buf)
}
