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

// providerInfo prints the upstream target shown by ddev pull before confirmation.
func (a *App) providerInfo(cfg Config) error {
	source := cfg.pullTarget()
	target := cfg.pushTarget()
	if source.User == "" || source.Host == "" || source.RemotePath == "" {
		fmt.Fprintf(a.Stdout, "Pull: %s@%s:%s\n", firstNonEmpty(source.User, "<ssh-user>"), firstNonEmpty(source.Host, "<host>"), firstNonEmpty(source.RemotePath, "<remote-path>"))
	} else {
		fmt.Fprintf(a.Stdout, "Pull: %s:%s\n", sshTarget(source), trimTrailingSlash(source.RemotePath))
	}
	if target.User == "" || target.Host == "" || target.RemotePath == "" {
		fmt.Fprintf(a.Stdout, "Push: %s@%s:%s\n", firstNonEmpty(target.User, "<ssh-user>"), firstNonEmpty(target.Host, "<host>"), firstNonEmpty(target.RemotePath, "<remote-path>"))
		return nil
	}
	fmt.Fprintf(a.Stdout, "Push: %s:%s\n", sshTarget(target), trimTrailingSlash(target.RemotePath))
	return nil
}

// providerAuth verifies local SSH agent state and upstream key authentication.
func (a *App) providerAuth(ctx context.Context, projectRoot string, cfg Config) error {
	tested := false
	source := cfg.pullTarget()
	if source.configured() {
		if err := cfg.validatePullRequired(); err != nil {
			return err
		}
		if err := a.requireSSHAgent(ctx, projectRoot); err != nil {
			return err
		}
		if err := a.runSSH(ctx, projectRoot, source, fmt.Sprintf("printf 'SSH key authentication works for %%s\\n' %s", shellQuote(sshTarget(source)))); err != nil {
			return err
		}
		tested = true
	}
	target := cfg.pushTarget()
	if target.configured() {
		if err := cfg.validatePushRequired(); err != nil {
			return err
		}
		if err := a.requireSSHAgent(ctx, projectRoot); err != nil {
			return err
		}
		if err := a.runSSH(ctx, projectRoot, target, fmt.Sprintf("printf 'SSH key authentication works for %%s\\n' %s", shellQuote(sshTarget(target)))); err != nil {
			return err
		}
		tested = true
	}
	if tested {
		return nil
	}
	return cfg.validatePullRequired()
}

// dbPull exports the upstream database and downloads it to DDEV's downloads path.
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

	fmt.Fprintln(a.Stdout, "Creating remote database export with WP-CLI...")
	remoteCommand := strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { rm -f %s %s; };", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		"trap cleanup INT TERM HUP EXIT;",
		"command -v wp >/dev/null;",
		"cd " + shellQuote(remoteWP) + ";",
		fmt.Sprintf("rm -f %s %s;", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		fmt.Sprintf("wp --allow-root db export %s;", shellQuote(remoteDump)),
		fmt.Sprintf("gzip -f %s;", shellQuote(remoteDump)),
		"trap - EXIT",
	}, " ")
	if err := a.runSSH(ctx, projectRoot, target, remoteCommand); err != nil {
		return err
	}

	fmt.Fprintln(a.Stdout, "Downloading database export...")
	args := []string{"-azs", "-e", sshCommandString(target), sshTarget(target) + ":" + remoteDumpGZ, filepath.Join(downloadDir, "db.sql.gz")}
	if err := a.runExternal(ctx, projectRoot, "rsync", args...); err != nil {
		return err
	}

	fmt.Fprintln(a.Stdout, "Deleting remote database export...")
	return a.runSSH(ctx, projectRoot, target, fmt.Sprintf("rm -f %s %s", shellQuote(remoteDump), shellQuote(remoteDumpGZ)))
}

// filesPull rsyncs the remote WordPress tree directly into the local project.
func (a *App) filesPull(ctx context.Context, projectRoot string, cfg Config) error {
	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	destination := localWPRoot(projectRoot, cfg)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	fmt.Fprintln(a.Stdout, "Syncing upstream WordPress files into the local DDEV environment...")
	args := []string{"-azs", "--delete", "--safe-links"}
	for _, exclude := range buildRsyncExcludes(projectRoot, cfg) {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, "-e", sshCommandString(target), sshTarget(target)+":"+trimTrailingSlash(target.RemotePath)+"/", destination+"/")

	err := a.runExternal(ctx, projectRoot, "rsync", args...)
	if err == nil {
		return nil
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 24 {
		fmt.Fprintln(a.Stderr, "Continuing after rsync warning: remote files vanished during transfer.")
		return nil
	}
	return err
}

// filesImport is intentionally a no-op because filesPull syncs directly.
func (a *App) filesImport() {
	fmt.Fprintln(a.Stdout, "Files are already synced into the local WordPress environment.")
}

// postPull applies local cleanup after DDEV imports the database.
func (a *App) postPull(ctx context.Context, projectRoot string, cfg Config) error {
	if err := a.sanitizeWPConfig(projectRoot, cfg); err != nil {
		return err
	}
	if err := a.replaceSiteURLs(ctx, projectRoot, cfg); err != nil {
		return err
	}
	return a.removeBlockedPlugins(ctx, projectRoot, cfg, "")
}

// buildRsyncExcludes keeps parity with the original shell provider exclude set.
func buildRsyncExcludes(projectRoot string, cfg Config) []string {
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

	if !cfg.CloneImages {
		excludes = append(excludes, "wp-content/uploads/")
	}

	for _, plugin := range readPluginListQuiet(projectRoot, cfg) {
		pluginDir := normalizePluginSlug(plugin)
		if pluginDir == "" {
			continue
		}
		excludes = append(excludes, "wp-content/plugins/"+pluginDir+"/")
		excludes = append(excludes, "wp-content/plugins/"+pluginDir+".php")
	}

	return excludes
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

	fmt.Fprintln(a.Stdout, "Sanitizing wp-config.php for DDEV-managed database settings...")
	updated := sanitizeWPConfigContents(string(contents))
	return os.WriteFile(wpConfig, []byte(updated), 0o644)
}

// sanitizeWPConfigContents is pure so config rewrite behavior is testable.
func sanitizeWPConfigContents(contents string) string {
	dbDefine := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]DB_(?:NAME|USER|PASSWORD|HOST|CHARSET|COLLATE)['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	contents = dbDefine.ReplaceAllString(contents, "")

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

	if !strings.Contains(contents, "wp-config-ddev.php") {
		snippet := `

// Include for DDEV-managed settings in wp-config-ddev.php.
$ddev_settings = __DIR__ . '/wp-config-ddev.php';
if (is_readable($ddev_settings) && !defined('DB_USER')) {
    require_once($ddev_settings);
}
`
		wpSettings := regexp.MustCompile(`\n\s*(?:require_once|require)\s+ABSPATH\s*\.\s*['"]wp-settings\.php['"]\s*;`)
		if loc := wpSettings.FindStringIndex(contents); loc != nil {
			contents = contents[:loc[0]] + snippet + contents[loc[0]:]
		} else {
			contents = strings.TrimRight(contents, "\r\n") + snippet + "\n"
		}
	}

	return contents
}

// replaceSiteURLs updates single-site and multisite URLs after database import.
func (a *App) replaceSiteURLs(ctx context.Context, projectRoot string, cfg Config) error {
	if cfg.SkipSearchReplace {
		return nil
	}
	if !inDDEVContainer() && !commandExists("ddev") {
		return nil
	}

	wpRoot := localWPRoot(projectRoot, cfg)
	if _, err := os.Stat(filepath.Join(wpRoot, "wp-config.php")); err != nil {
		return nil
	}

	oldURL := firstNonEmpty(a.wpOutput(ctx, projectRoot, cfg, "option", "get", "home"), a.wpOutput(ctx, projectRoot, cfg, "option", "get", "siteurl"))
	newURL := localSiteURL(projectRoot, cfg)
	if oldURL == "" || newURL == "" {
		fmt.Fprintln(a.Stderr, "Skipping WordPress URL replacement because the old or new URL could not be detected.")
		return nil
	}

	oldBase := urlBase(oldURL)
	newBase := urlBase(newURL)
	if oldBase == "" || newBase == "" {
		fmt.Fprintln(a.Stderr, "Skipping WordPress URL replacement because the old or new URL is invalid.")
		return nil
	}

	if err := a.runSearchReplace(ctx, projectRoot, cfg, oldURL, newURL); err != nil {
		return err
	}
	hostPart := strings.TrimPrefix(strings.TrimPrefix(oldBase, "http://"), "https://")
	if err := a.runSearchReplace(ctx, projectRoot, cfg, "http://"+hostPart, newBase); err != nil {
		return err
	}
	if err := a.runSearchReplace(ctx, projectRoot, cfg, "https://"+hostPart, newBase); err != nil {
		return err
	}
	return a.replaceMultisiteDomains(ctx, projectRoot, cfg, oldBase, newBase)
}

// runSearchReplace delegates serialized WordPress updates to WP-CLI inside DDEV.
func (a *App) runSearchReplace(ctx context.Context, projectRoot string, cfg Config, oldValue string, newValue string) error {
	if oldValue == "" || newValue == "" || oldValue == newValue {
		return nil
	}
	fmt.Fprintf(a.Stdout, "Replacing WordPress URLs: %s -> %s\n", oldValue, newValue)
	return a.runWP(ctx, projectRoot, cfg, "search-replace", oldValue, newValue, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--report-changed-only")
}

// replaceMultisiteDomains updates wp_site and wp_blogs domain columns when present.
func (a *App) replaceMultisiteDomains(ctx context.Context, projectRoot string, cfg Config, oldBase string, newBase string) error {
	oldDomain := urlHost(oldBase)
	newDomain := urlHost(newBase)
	if oldDomain == "" || newDomain == "" || oldDomain == newDomain {
		return nil
	}

	prefix := a.wpOutput(ctx, projectRoot, cfg, "db", "prefix")
	if prefix == "" || regexp.MustCompile(`[^A-Za-z0-9_]`).MatchString(prefix) {
		return nil
	}

	for _, table := range []string{prefix + "site", prefix + "blogs"} {
		exists, err := a.wpTableExists(ctx, projectRoot, cfg, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		fmt.Fprintf(a.Stdout, "Replacing WordPress multisite domains in %s: %s -> %s\n", table, oldDomain, newDomain)
		query := fmt.Sprintf("UPDATE `%s` SET domain = %s WHERE domain = %s", table, sqlQuote(newDomain), sqlQuote(oldDomain))
		if err := a.runWP(ctx, projectRoot, cfg, "db", "query", query); err != nil {
			return err
		}
	}
	return nil
}

// wpTableExists checks table presence through WP-CLI.
func (a *App) wpTableExists(ctx context.Context, projectRoot string, cfg Config, table string) (bool, error) {
	query := fmt.Sprintf("SHOW TABLES LIKE %s", sqlQuote(table))
	output, err := a.wpOutputErr(ctx, projectRoot, cfg, "db", "query", query, "--skip-column-names")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(output) == table, nil
}

// removeBlockedPlugins deactivates and deletes plugins listed in the editable block list.
func (a *App) removeBlockedPlugins(ctx context.Context, projectRoot string, cfg Config, overrideRoot string) error {
	wpRoot := localWPRoot(projectRoot, cfg)
	if overrideRoot != "" {
		if filepath.IsAbs(overrideRoot) {
			wpRoot = overrideRoot
		} else {
			wpRoot = filepath.Join(projectRoot, overrideRoot)
		}
	}

	pluginsRoot := filepath.Join(wpRoot, "wp-content", "plugins")
	if _, err := os.Stat(pluginsRoot); err != nil {
		return nil
	}

	plugins, err := readPluginList(projectRoot, cfg)
	if err != nil {
		return err
	}
	removalSlugs := map[string]bool{}
	for _, plugin := range plugins {
		slug := normalizePluginSlug(plugin)
		if slug != "" {
			removalSlugs[slug] = true
		}
	}
	if len(removalSlugs) == 0 {
		return nil
	}

	installed := a.installedPluginSlugs(ctx, projectRoot, cfg)
	slugs := make([]string, 0, len(removalSlugs))
	for slug := range removalSlugs {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		if installed[slug] {
			fmt.Fprintf(a.Stdout, "Removing local-only blocked plugin %s...\n", slug)
			_ = a.runWP(ctx, projectRoot, cfg, "plugin", "deactivate", slug)
		}
		if err := removePluginPath(filepath.Join(pluginsRoot, slug)); err != nil {
			return err
		}
		if err := removePluginPath(filepath.Join(pluginsRoot, slug+".php")); err != nil {
			return err
		}
	}
	return nil
}

// importStandaloneDB imports the downloaded gzip dump when DDEV is not orchestrating imports.
func (a *App) importStandaloneDB(ctx context.Context, root string, cfg Config) error {
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

	fmt.Fprintln(a.Stdout, "Importing database with WP-CLI...")
	return a.runWP(ctx, root, cfg, "db", "import", tempPath)
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

// readPluginListQuiet lets rsync excludes ignore invalid or missing lists until command validation.
func readPluginListQuiet(projectRoot string, cfg Config) []string {
	plugins, err := readPluginList(projectRoot, cfg)
	if err != nil {
		return nil
	}
	return plugins
}

// normalizePluginSlug converts plugin basenames and nested paths to plugin slugs.
func normalizePluginSlug(plugin string) string {
	plugin = strings.SplitN(plugin, "/", 2)[0]
	plugin = strings.TrimSuffix(plugin, ".php")
	return plugin
}

// installedPluginSlugs gets active local plugin names without failing cleanup.
func (a *App) installedPluginSlugs(ctx context.Context, projectRoot string, cfg Config) map[string]bool {
	installed := map[string]bool{}
	if _, err := os.Stat(filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")); err != nil {
		return installed
	}

	output, err := a.wpOutputErr(ctx, projectRoot, cfg, "plugin", "list", "--format=json", "--fields=name")
	if err != nil {
		return installed
	}
	var plugins []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(output), &plugins); err != nil {
		return installed
	}
	for _, plugin := range plugins {
		if plugin.Name != "" {
			installed[plugin.Name] = true
		}
	}
	return installed
}

// removePluginPath deletes blocked plugin files while avoiding symlinked package paths.
func removePluginPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	return os.RemoveAll(path)
}

// runWP executes WP-CLI through DDEV so database-aware cleanup stays inside containers.
func (a *App) runWP(ctx context.Context, projectRoot string, cfg Config, args ...string) error {
	name, fullArgs := localWPCommand(projectRoot, cfg, args...)
	return a.runExternal(ctx, projectRoot, name, fullArgs...)
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

// localWPCommand selects DDEV's WP-CLI proxy only when DDEV describe succeeds.
func localWPCommand(projectRoot string, cfg Config, args ...string) (string, []string) {
	if inDDEVContainer() {
		fullArgs := append([]string{"--path=" + containerWPPath(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes"}, args...)
		return "wp", fullArgs
	}
	if _, ok := ddevDescribe(projectRoot); ok {
		fullArgs := append([]string{"wp", "--path=" + containerWPPath(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes"}, args...)
		return "ddev", fullArgs
	}
	fullArgs := append([]string{"--path=" + localWPRoot(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes"}, args...)
	return "wp", fullArgs
}

// runExternal runs a local executable without invoking a local shell.
func (a *App) runExternal(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = a.Stdout
	cmd.Stderr = a.Stderr
	return cmd.Run()
}

// outputExternal captures command output while preserving stderr for diagnostics.
func (a *App) outputExternal(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = a.Stderr
	err := cmd.Run()
	return stdout.String(), err
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
