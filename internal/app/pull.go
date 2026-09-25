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
	return a.preflightProviderAuth(ctx, projectRoot, cfg)
}

// dbPull exports the upstream database and downloads it to the runtime scratch path.
func (a *App) dbPull(ctx context.Context, projectRoot string, cfg Config, useSCP bool) error {
	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	downloadDir := downloadsDir(projectRoot)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return err
	}
	if err := protectDownloadsDir(projectRoot, downloadDir); err != nil {
		return err
	}

	remoteTmp := trimTrailingSlash(defaultString(target.RemoteTmpDir, "/tmp"))
	remoteWP := trimTrailingSlash(target.RemotePath)
	projectName := firstNonEmpty(os.Getenv("DDEV_PROJECT"), filepath.Base(projectRoot), "wordpress")
	dumpID := randomID()
	remoteDump := fmt.Sprintf("%s/ddev-%s-%s-%s.sql", remoteTmp, projectName, time.Now().Format("20060102150405"), dumpID)
	remoteDumpGZ := remoteDump + ".gz"
	mariaDBSetup, mariaDBCleanup := remoteMariaDBCompatibilityCommands(remoteTmp, dumpID, a.needsRemoteMariaDBCompatibility(target))
	wpExport := "wp_ssh_wp"
	if override := a.remotePHPFunctionOverrideFor(target); override.required() {
		wpExport = override.command()
	}

	remoteCommand := strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { rm -f %s %s || true; %s; };", shellQuote(remoteDump), shellQuote(remoteDumpGZ), mariaDBCleanup),
		"trap cleanup INT TERM HUP EXIT;",
		"cd " + shellQuote(remoteWP) + ";",
		remoteWPCLIPrelude(target),
		mariaDBSetup,
		fmt.Sprintf("rm -f %s %s;", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		fmt.Sprintf("%s --allow-root db export %s;", wpExport, shellQuote(remoteDump)),
		fmt.Sprintf("gzip -f %s;", shellQuote(remoteDump)),
		mariaDBCleanup + ";",
		"trap - EXIT",
	}, " ")
	if err := a.runStep("Exporting pull source database", "Pull source database exported", func() error {
		return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, remoteCommand)
	}); err != nil {
		return err
	}
	remoteDumpCleanupNeeded := true
	defer func() {
		if !remoteDumpCleanupNeeded {
			return
		}
		if err := a.removeRemoteDatabaseDump(context.Background(), projectRoot, target, remoteDump, remoteDumpGZ); err != nil {
			a.UI.Warning("Could not remove remote database export: %s", err)
		}
	}()

	localDump := filepath.Join(downloadDir, "db.sql.gz")
	if err := os.Remove(localDump); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	localDumpDownloadComplete := false
	defer func() {
		if !localDumpDownloadComplete {
			_ = os.Remove(localDump)
		}
	}()
	if useSCP {
		if err := a.runStep("Downloading database export", "Database export downloaded", func() error {
			return a.downloadOverSSH(ctx, projectRoot, target, remoteDumpGZ, localDump)
		}); err != nil {
			return err
		}
	} else {
		args := append(rsyncArchiveArgs(), "-e", a.sshCommandString(target), sshTarget(target)+":"+remoteDumpGZ, localDump)
		if err := a.runStep("Downloading database export", "Database export downloaded", func() error {
			return a.runExternal(ctx, projectRoot, "rsync", args...)
		}); err != nil {
			return err
		}
	}
	localDumpDownloadComplete = true

	if err := a.runStep("Cleaning up remote database export", "Remote database export removed", func() error {
		return a.removeRemoteDatabaseDump(ctx, projectRoot, target, remoteDump, remoteDumpGZ)
	}); err != nil {
		return err
	}
	remoteDumpCleanupNeeded = false
	return nil
}

// removeRemoteDatabaseDump deletes the unique transfer files without relying on
// a successful export, upload, or download path.
func (a *App) removeRemoteDatabaseDump(ctx context.Context, projectRoot string, target RemoteTarget, remoteDump string, remoteDumpGZ string) error {
	return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, fmt.Sprintf("rm -f %s %s", shellQuote(remoteDump), shellQuote(remoteDumpGZ)))
}

// filesPull syncs the remote WordPress tree into the local project.
// When useSCP is true it uses a tar pipe over SSH instead of rsync.
func (a *App) filesPull(ctx context.Context, projectRoot string, cfg Config, preserveLocalWPConfig bool, clone bool, cleanTarget bool, useSCP bool) error {
	if useSCP {
		return a.filesPullTar(ctx, projectRoot, cfg, preserveLocalWPConfig, clone, cleanTarget)
	}

	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	destination := localWPRoot(projectRoot, cfg)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	args := rsyncArchiveArgs()
	// A normal pull always mirrors the source, so it deletes stale local files. A
	// clone is additive by default and only removes pre-existing target content when
	// --clean-target (cleanTarget) is set, so both transports behave consistently.
	if !clone || cleanTarget {
		args = append(args, "--delete")
	}
	args = append(args, "--safe-links")
	excludes, err := buildRsyncExcludes(projectRoot, cfg, preserveLocalWPConfig, clone)
	if err != nil {
		return err
	}
	for _, hide := range buildRsyncHideRules() {
		args = append(args, "--filter=H "+hide)
	}
	for _, exclude := range excludes {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, "-e", a.sshCommandString(target), sshTarget(target)+":"+trimTrailingSlash(target.RemotePath)+"/", destination+"/")

	return a.runStep("Syncing WordPress files from pull source", "WordPress files synced", func() error {
		return a.runExternalAllowRsyncVanished(ctx, projectRoot, args...)
	})
}

// filesPullTar syncs the remote WordPress tree using a tar pipe over SSH.
// The tar step itself only adds or updates files; when cleanTarget is set the destination
// was already emptied by cleanCloneTarget before this runs (there is no rsync --delete
// equivalent for the tar transport).
func (a *App) filesPullTar(ctx context.Context, projectRoot string, cfg Config, preserveLocalWPConfig bool, clone bool, cleanTarget bool) error {
	target := cfg.pullTarget()
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}

	destination := localWPRoot(projectRoot, cfg)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	excludes, err := buildRsyncExcludes(projectRoot, cfg, preserveLocalWPConfig, clone)
	if err != nil {
		return err
	}

	// Build the remote tar command. Exclude patterns use the same set as rsync
	// but without trailing slashes (which are rsync-specific).
	parts := []string{"tar", "-czf", "-"}
	for _, p := range buildTarExcludeArgs(excludes) {
		parts = append(parts, p)
	}
	// *.log is hidden by a rsync filter rule; use an explicit tar exclude instead.
	parts = append(parts, "--exclude="+shellQuote("*.log"))
	parts = append(parts, "-C", shellQuote(trimTrailingSlash(target.RemotePath)), ".")
	remoteCmd := strings.Join(parts, " ")

	if !cleanTarget {
		a.UI.Warning("scp/tar transport: stale local files not removed (no --delete equivalent)")
	}
	return a.runStep("Syncing WordPress files from pull source", "WordPress files synced", func() error {
		return a.tarPipeFromRemote(ctx, projectRoot, target, remoteCmd, destination)
	})
}

// cleanCloneTarget empties the clone destination before the scp/tar transport
// extracts into it, so pre-existing content on the target (for example a web host's
// default files) does not survive the clone. The rsync transport achieves the same
// via --delete, which the scp/tar transport lacks. A small set of operational entries
// (VCS metadata, DDEV/tool state, this tool's own config and binary) is preserved,
// mirroring the rsync clone excludes.
func (a *App) cleanCloneTarget(projectRoot string, cfg Config) error {
	destination := localWPRoot(projectRoot, cfg)
	keep := cloneCleanKeep(defaultBinaryPath())
	a.UI.Warning("clean-target: emptying %s before extract (preserved: %s)", destination, strings.Join(sortedKeep(keep), ", "))
	return a.runStep("Clearing clone target", "Clone target cleared", func() error {
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return err
		}
		return emptyDirExcept(destination, keep)
	})
}

// cloneCleanKeep is the set of top-level entries cleanCloneTarget must not delete:
// version-control and tooling state plus this tool's own config file and binary, which may
// live in the destination when it is the project root. It mirrors the rsync clone
// excludes (.git, .ddev, .wp-ssh, wp-config-ddev.php) so both transports preserve the same
// operational files.
func cloneCleanKeep(binaryPath string) map[string]bool {
	keep := map[string]bool{
		".git":               true,
		".ddev":              true,
		".wp-ssh":            true,
		".wp-ssh.yaml":       true,
		"wp-config-ddev.php": true,
	}
	if base := filepath.Base(binaryPath); base != "" && base != "." && base != string(filepath.Separator) {
		keep[base] = true
	}
	return keep
}

func sortedKeep(keep map[string]bool) []string {
	names := make([]string, 0, len(keep))
	for name := range keep {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// emptyDirExcept removes every direct child of dir except those named in keep. It refuses
// to operate on a filesystem root or the user's home directory as a guard against a
// misconfigured destination wiping far more than intended.
func emptyDirExcept(dir string, keep map[string]bool) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cleaned := filepath.Clean(abs)
	if cleaned == "" || filepath.Dir(cleaned) == cleaned {
		return fmt.Errorf("refusing to empty filesystem root %q", dir)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && filepath.Clean(home) == cleaned {
		return fmt.Errorf("refusing to empty home directory %q", dir)
	}
	entries, err := os.ReadDir(cleaned)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if keep[entry.Name()] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cleaned, entry.Name())); err != nil {
			return err
		}
	}
	return nil
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
func (a *App) postPull(ctx context.Context, adapter runtimeAdapter, cfg Config, clone bool) error {
	projectRoot := adapter.Root()
	// Clone writes the target DB credentials before import (see runPullPipeline),
	// so it only runs URL updates and keeps blocked plugins and the runtime's
	// dev-mode post-pull hooks are skipped.
	if !clone {
		for _, hook := range adapter.PostPullHooks() {
			if err := hook(ctx, a, projectRoot, cfg); err != nil {
				return err
			}
		}
	}
	if clone && preferredLocalURL(projectRoot, cfg) == "" {
		// A clone moves a live site. Without a configured target URL, the URL constants
		// would fall back to a development host such as https://localhost, so the clone
		// keeps the source URL and rewrites only what a domain mapping names.
		if len(cfg.PullDomainReplacements) == 0 {
			a.UI.Info("Keeping the source site URL: no target URL is configured. Set local_url, WP_SSH_PULL_LOCAL_URL, or --local-url, or add a pull domain mapping, to move the clone to another domain.")
			return nil
		}
		return a.replaceSiteURLs(ctx, projectRoot, cfg)
	}
	if err := a.updateWPConfigURLConstants(projectRoot, cfg, adapter.Mode()); err != nil {
		return err
	}
	if err := a.replaceSiteURLs(ctx, projectRoot, cfg); err != nil {
		return err
	}
	if clone {
		return nil
	}
	return a.removeBlockedPlugins(ctx, projectRoot, cfg, "")
}

// buildRsyncExcludes keeps parity with the original shell provider exclude set.
func buildRsyncExcludes(projectRoot string, cfg Config, preserveLocalWPConfig bool, clone bool) ([]string, error) {
	excludes := []string{
		".git/",
		".ddev/",
		".wp-ssh/",
		"wp-config-ddev.php",
		"wp-content/cache/",
		"wp-content/upgrade/",
		"wp-content/updraft/",
		"wp-content/ai1wm-backups/",
		"wp-content/languages/wpml/queue/",
	}

	if preserveLocalWPConfig {
		excludes = append(excludes, "wp-config.php")
	}

	if !cfg.CloneImages && !clone {
		excludes = append(excludes, "wp-content/uploads/")
	}

	// wp-env bind-mounts plugins/themes/mappings entries over wp-content. Pulled files in
	// those paths would be shadowed by the mounts, and rsync --delete against a live
	// mountpoint directory can fail with EBUSY, so both transports skip them.
	if isWPEnvRoot(projectRoot) {
		if status, ok := wpEnvStatus(projectRoot); ok {
			excludes = append(excludes, wpEnvContainerMounts(status.InstallPath)...)
		}
	}

	if clone {
		return excludes, nil
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

func buildRsyncHideRules() []string {
	// A basename-only rsync pattern matches recursively at any directory depth.
	return []string{"*.log"}
}

// rsyncArchiveArgs stays compatible with macOS' bundled rsync, which lacks -s/--protect-args.
func rsyncArchiveArgs() []string {
	return []string{"-az"}
}

// updateWPConfigURLConstants keeps hardcoded WordPress URL constants from overriding local URLs.
func (a *App) updateWPConfigURLConstants(projectRoot string, cfg Config, mode runtimeMode) error {
	// wp-env generates wp-config.php with WP_HOME and WP_SITEURL already pointing at the
	// local environment, and rewrites the file on every start, so edits here are both
	// unnecessary and discarded.
	if mode == modeWPEnv {
		return nil
	}

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
			Home:              "",
			SiteURL:           "",
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
		if define.value == "" {
			contents = removeWPConfigDefine(contents, define.name)
			continue
		}
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

func removeWPConfigDefine(contents string, name string) string {
	pattern := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	return pattern.ReplaceAllString(contents, "")
}

func replaceWPConfigDefine(contents string, name string, value string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^[ \t]*define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*.*?\);[ \t]*(?:\r?\n)?`)
	if !pattern.MatchString(contents) {
		return contents, false
	}
	return pattern.ReplaceAllLiteralString(contents, wpConfigDefineLine(name, value)), true
}

func wpConfigDefineLine(name string, value string) string {
	return "define('" + name + "', " + value + ");\n"
}

func phpStringLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

// applyCloneWPConfig writes target database settings into the copied wp-config.php.
func (a *App) applyCloneWPConfig(projectRoot string, cfg Config) error {
	if !cfg.hasAnyCloneDBCredential() {
		return nil
	}

	wpConfig := filepath.Join(localWPRoot(projectRoot, cfg), "wp-config.php")
	contents, err := os.ReadFile(wpConfig)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("wp-config.php not found at %s; clone needs a copied or existing target config", wpConfig)
		}
		return err
	}

	updated := updateWPConfigDBCredentialsContents(string(contents), cfg)
	if updated == string(contents) {
		return nil
	}
	return a.runStep("Writing clone database credentials", "Clone database credentials written", func() error {
		return os.WriteFile(wpConfig, []byte(updated), 0o644)
	})
}

// updateWPConfigDBCredentialsContents is pure so clone config rewrites are testable.
func updateWPConfigDBCredentialsContents(contents string, cfg Config) string {
	defines := []struct {
		name  string
		value string
	}{
		{name: "DB_NAME", value: phpStringLiteral(cfg.CloneDBName)},
		{name: "DB_USER", value: phpStringLiteral(cfg.CloneDBUser)},
		{name: "DB_PASSWORD", value: phpStringLiteral(cfg.CloneDBPassword)},
		{name: "DB_HOST", value: phpStringLiteral(cfg.CloneDBHost)},
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
	if cfg.CloneDBPrefix != "" {
		updated, replaced := replaceWPConfigTablePrefix(contents, phpStringLiteral(cfg.CloneDBPrefix))
		if replaced {
			contents = updated
		} else {
			insertions = append(insertions, wpConfigTablePrefixLine(phpStringLiteral(cfg.CloneDBPrefix)))
		}
	}
	if len(insertions) == 0 {
		return contents
	}
	return insertWPConfigSnippet(contents, strings.Join(insertions, ""))
}

func replaceWPConfigTablePrefix(contents string, value string) (string, bool) {
	pattern := regexp.MustCompile(`(?m)^[ \t]*\$table_prefix\s*=\s*.*?;[ \t]*(?:\r?\n)?`)
	if !pattern.MatchString(contents) {
		return contents, false
	}
	return pattern.ReplaceAllLiteralString(contents, wpConfigTablePrefixLine(value)), true
}

func wpConfigTablePrefixLine(value string) string {
	return "$table_prefix = " + value + ";\n"
}

func insertWPConfigSnippet(contents string, snippet string) string {
	snippet = "\n" + snippet
	stopEditing := regexp.MustCompile(`\n\s*/\* That(?:\\'|'|\x{2019})s all, stop editing! Happy publishing\. \*/`)
	if loc := stopEditing.FindStringIndex(contents); loc != nil {
		return contents[:loc[0]] + snippet + contents[loc[0]:]
	}
	return strings.TrimRight(contents, "\r\n") + snippet
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
		// A protocol-less configured mapping already replaces the hostname in both
		// complete URLs and bare domain values. Adding automatic URL variants for the
		// same hosts would rewrite the configured target a second time when the target
		// contains the source hostname (for example example.com.ddev.site).
		coveredByConfiguredHost := configuredHostReplacementCoversURLs(cfg.PullDomainReplacements, oldURL, newURL)
		if coveredByConfiguredHost {
			autoPairs = nil
		}
		if len(autoPairs) == 0 {
			if !coveredByConfiguredHost {
				a.UI.Warning("Skipping automatic WordPress URL replacement because the old or new URL is invalid.")
			}
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
	if urlBase(oldValue) == "" && urlBase(newValue) == "" {
		// Replacing the hostname once covers protocol-prefixed URLs as well as bare
		// multisite domain values. Separate http/https pairs would overlap and can
		// mutate a freshly-written target such as example.com.ddev.site again.
		return []replacementPair{{old: oldHost, new: newHost}}
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

// configuredHostReplacementCoversURLs reports whether an explicit protocol-less
// mapping already covers the automatically detected root URLs. Explicit mappings take
// precedence and intentionally preserve the URL scheme.
func configuredHostReplacementCoversURLs(replacements []DomainReplacement, oldURL string, newURL string) bool {
	oldParsed, oldOK := rootURL(oldURL)
	newParsed, newOK := rootURL(newURL)
	if !oldOK || !newOK {
		return false
	}
	for _, replacement := range replacements {
		if urlBase(replacement.Old) != "" || urlBase(replacement.New) != "" {
			continue
		}
		if strings.EqualFold(replacementHost(replacement.Old), oldParsed.Hostname()) && strings.EqualFold(replacementHost(replacement.New), newParsed.Hostname()) {
			return true
		}
	}
	return false
}

// rootURL accepts only complete site-root URLs because a host-only mapping must not
// suppress automatic replacements that carry meaningful path changes.
func rootURL(value string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, false
	}
	return parsed, true
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

// searchReplaceCommandArgs centralizes the WP-CLI flags used for serialized-safe URL changes.
func searchReplaceCommandArgs(oldValue string, newValue string) []string {
	return []string{"search-replace", oldValue, newValue, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--no-report"}
}

// runSearchReplace delegates serialized WordPress updates to WP-CLI.
func (a *App) runSearchReplace(ctx context.Context, projectRoot string, cfg Config, oldValue string, newValue string) error {
	if oldValue == "" || newValue == "" || oldValue == newValue {
		return nil
	}
	title := fmt.Sprintf("Replacing WordPress URLs: %s -> %s", oldValue, newValue)
	done := fmt.Sprintf("WordPress URL replacement finished (%s)", replacementLabel(oldValue))
	return a.runStep(title, done, func() error {
		return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, searchReplaceCommandArgs(oldValue, newValue)...)
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
		args := append([]string{"--url=" + siteURL}, searchReplaceCommandArgs(oldValue, newValue)...)
		return a.runWPWithFilteredWarnings(ctx, projectRoot, cfg, args...)
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

	// WP-CLI runs inside the wp-env container, where only the mounted WordPress tree is
	// visible. A local root outside that tree cannot be mapped, and the container-path
	// fallback would silently delete plugins from the wp-env install instead.
	if isWPEnvRoot(projectRoot) {
		if status, ok := wpEnvStatus(projectRoot); ok {
			local := localWPRoot(projectRoot, cfg)
			if _, ok := wpEnvContainerPath(status, local); !ok {
				return fmt.Errorf("local WordPress path %s is outside the wp-env tree at %s; wp plugin commands would run against the wp-env install instead. Clear the WordPress root override, or pass --integration standalone to use a host WP-CLI", local, status.wordPressRoot())
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

		// In wp-env, mounted plugin and theme directories are the developer's own working
		// tree, and `wp plugin delete` runs inside the container where those mounts live.
		// Deleting through it would remove host source files, so skip the step entirely.
		if isWPEnvRoot(projectRoot) {
			if mounts := wpEnvMountedSources(projectRoot); len(mounts) > 0 {
				a.UI.Warning("Skipping blocked-plugin removal: wp-env mounts %s over wp-content, and deleting through the container could remove your source files.", strings.Join(mounts, " and "))
				return "Blocked-plugin removal skipped for mounted wp-env sources", nil
			}
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
	if isDDEVRoot(root) {
		if containerPath, ok := containerProjectPath(root, tempPath); ok {
			importPath = containerPath
		}
	} else if isWPEnvRoot(root) {
		status, ok := wpEnvStatus(root)
		if !ok {
			return errors.New("wp-env status is unavailable, so the database dump cannot be mapped into the container; start the environment with wp-env start")
		}
		// Handing a host path to a command that runs inside the container yields a bare
		// file-not-found after the whole database has already been downloaded.
		containerPath, ok := wpEnvContainerPath(status, tempPath)
		if !ok {
			return fmt.Errorf("database dump at %s is outside the wp-env WordPress tree at %s, so the container cannot read it", tempPath, status.wordPressRoot())
		}
		importPath = containerPath
	}
	if err := a.runStep("Importing database into local WordPress", "Local database imported", func() error {
		return a.runWPWithFilteredWarnings(ctx, root, cfg, "db", "import", importPath)
	}); err != nil {
		return err
	}
	// The dump is a full production database. Do not leave it behind after a successful
	// import; in wp-env mode the scratch directory is inside the served document root.
	if err := os.Remove(dumpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.UI.Warning("Could not remove local database export: %s", err)
	}
	return nil
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

// localWPCommand selects DDEV's WP-CLI proxy only when DDEV describe succeeds, and
// wp-env's cli container when the project is a wp-env project.
func localWPCommand(projectRoot string, cfg Config, args ...string) (string, []string) {
	if isDDEVRoot(projectRoot) {
		fullArgs := append([]string{"wp", "--path=" + containerWPPath(projectRoot, cfg), "--allow-root", "--skip-plugins", "--skip-themes"}, args...)
		return "ddev", fullArgs
	}
	if isWPEnvRoot(projectRoot) {
		if status, ok := wpEnvStatus(projectRoot); ok {
			name, base := wpEnvCommand(projectRoot)
			containerPath := wpEnvContainerRoot
			if mapped, ok := wpEnvContainerPath(status, localWPRoot(projectRoot, cfg)); ok {
				containerPath = mapped
			}
			return name, wpEnvRunCLIArgs(base, containerPath, args...)
		}
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
	case "wp-env", "npx":
		return "wp-env"
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
