package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// dbPush uploads the local database dump and imports it on the push target with WP-CLI.
func (a *App) dbPush(ctx context.Context, projectRoot string, cfg Config) error {
	target := cfg.pushTarget()
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}

	downloadDir := downloadsDir(projectRoot)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return err
	}

	if cfg.PushURL == "" {
		if remoteURL := a.remoteSiteURL(ctx, projectRoot, target); remoteURL != "" {
			if err := writePushURLCache(projectRoot, target, remoteURL); err != nil {
				return err
			}
		}
	} else if err := writePushURLCache(projectRoot, target, cfg.PushURL); err != nil {
		return err
	}

	localDump := filepath.Join(downloadDir, "db.sql.gz")
	if err := a.ensureLocalDBDump(ctx, projectRoot, cfg, localDump); err != nil {
		return err
	}

	remoteTmp := trimTrailingSlash(defaultString(target.RemoteTmpDir, "/tmp"))
	projectName := firstNonEmpty(os.Getenv("DDEV_PROJECT"), filepath.Base(projectRoot), "wordpress")
	remoteDumpGZ := fmt.Sprintf("%s/ddev-%s-push-%s-%s.sql.gz", remoteTmp, projectName, time.Now().Format("20060102150405"), randomID())
	remoteDump := strings.TrimSuffix(remoteDumpGZ, ".gz")

	args := append(rsyncArchiveArgs(), "-e", sshCommandString(target), localDump, sshTarget(target)+":"+remoteDumpGZ)
	if err := a.runStep("Uploading database export to push target", "Database export uploaded to push target", func() error {
		return a.runExternal(ctx, projectRoot, "rsync", args...)
	}); err != nil {
		return err
	}

	remoteCommand := strings.Join([]string{
		"set -eu;",
		fmt.Sprintf("cleanup() { rm -f %s %s; };", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
		"trap cleanup INT TERM HUP EXIT;",
		"cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + ";",
		remoteWPCLIPrelude(target),
		fmt.Sprintf("gzip -dc %s > %s;", shellQuote(remoteDumpGZ), shellQuote(remoteDump)),
		fmt.Sprintf("wp_ssh_wp --allow-root db import %s;", shellQuote(remoteDump)),
		"trap - EXIT;",
		fmt.Sprintf("rm -f %s %s", shellQuote(remoteDump), shellQuote(remoteDumpGZ)),
	}, " ")
	return a.runStep("Importing database on push target", "Push target database imported", func() error {
		return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, remoteCommand)
	})
}

// filesPush syncs the complete local WordPress app to the push target.
func (a *App) filesPush(ctx context.Context, projectRoot string, cfg Config) error {
	target := cfg.pushTarget()
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}

	source := localWPRoot(projectRoot, cfg)
	if _, err := os.Stat(source); err != nil {
		return err
	}

	if err := a.runSSHQuietSuccess(ctx, projectRoot, target, "mkdir -p "+shellQuote(trimTrailingSlash(target.RemotePath))); err != nil {
		return err
	}

	args := append(rsyncArchiveArgs(), "--delete", "--safe-links")
	for _, exclude := range buildPushRsyncExcludes() {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, "-e", sshCommandString(target), source+"/", sshTarget(target)+":"+trimTrailingSlash(target.RemotePath)+"/")
	return a.runStep("Syncing WordPress files to push target", "Push target files synced", func() error {
		return a.runExternal(ctx, projectRoot, "rsync", args...)
	})
}

// postPush performs remote URL replacement after DDEV finishes db/files push.
func (a *App) postPush(ctx context.Context, projectRoot string, cfg Config) error {
	if cfg.SkipSearchReplace {
		return nil
	}
	target := cfg.pushTarget()
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}

	oldURL := firstNonEmpty(a.wpOutput(ctx, projectRoot, cfg, "option", "get", "home"), a.wpOutput(ctx, projectRoot, cfg, "option", "get", "siteurl"), localSiteURL(projectRoot, cfg))
	newURL := firstNonEmpty(cfg.PushURL, readPushURLCache(projectRoot, target))
	if oldURL == "" || newURL == "" {
		a.UI.Warning("Skipping push URL replacement because the local or push target URL could not be detected. Set push_url or WP_SSH_PUSH_URL.")
		return nil
	}

	oldBase := urlBase(oldURL)
	newBase := urlBase(newURL)
	if oldBase == "" || newBase == "" {
		a.UI.Warning("Skipping push URL replacement because the local or push target URL is invalid.")
		return nil
	}

	hostPart := strings.TrimPrefix(strings.TrimPrefix(oldBase, "http://"), "https://")
	for _, pair := range uniqueReplacementPairs([]replacementPair{
		{old: oldURL, new: newURL},
		{old: "http://" + hostPart, new: newBase},
		{old: "https://" + hostPart, new: newBase},
	}) {
		if err := a.runRemoteSearchReplace(ctx, projectRoot, target, pair.old, pair.new); err != nil {
			return err
		}
	}
	return a.replaceRemoteMultisiteDomains(ctx, projectRoot, target, oldBase, newBase)
}

// ensureLocalDBDump creates the gzipped dump used by push when absent.
func (a *App) ensureLocalDBDump(ctx context.Context, projectRoot string, cfg Config, dumpPath string) error {
	if _, err := os.Stat(dumpPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dumpPath), 0o755); err != nil {
		return err
	}

	file, err := os.Create(dumpPath)
	if err != nil {
		return err
	}

	gzipWriter := gzip.NewWriter(file)

	name, args := localWPCommand(projectRoot, cfg, "db", "export", "-", "--add-drop-table")
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = projectRoot
	cmd.Stdout = gzipWriter
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr
	runErr := a.runStep("Exporting local database", "Local database exported", func() error {
		return cmd.Run()
	})
	closeErr := gzipWriter.Close()
	fileErr := file.Close()
	if runErr != nil {
		a.writeCapturedOutput(commandLabel(name), stderr.String(), true)
		_ = os.Remove(dumpPath)
		return runErr
	}
	if closeErr != nil {
		_ = os.Remove(dumpPath)
		return closeErr
	}
	if fileErr != nil {
		_ = os.Remove(dumpPath)
		return fileErr
	}
	return nil
}

// buildPushRsyncExcludes protects environment-specific config while pushing the full app.
func buildPushRsyncExcludes() []string {
	return []string{
		".ddev/",
		"wp-config.php",
		"wp-config-ddev.php",
	}
}

// remoteSiteURL detects the current target URL before a database import overwrites it.
func (a *App) remoteSiteURL(ctx context.Context, projectRoot string, target RemoteTarget) string {
	url := strings.TrimSpace(a.remoteWPOutput(ctx, projectRoot, target, "option", "get", "home"))
	if url != "" {
		return trimTrailingSlash(url)
	}
	url = strings.TrimSpace(a.remoteWPOutput(ctx, projectRoot, target, "option", "get", "siteurl"))
	return trimTrailingSlash(url)
}

// runRemoteSearchReplace delegates serialized URL changes to WP-CLI on the target.
func (a *App) runRemoteSearchReplace(ctx context.Context, projectRoot string, target RemoteTarget, oldValue string, newValue string) error {
	if oldValue == "" || newValue == "" || oldValue == newValue {
		return nil
	}
	title := fmt.Sprintf("Replacing push target URLs: %s -> %s", oldValue, newValue)
	return a.runStep(title, "Push target URL replacement finished", func() error {
		return a.runRemoteWPWithFilteredWarnings(ctx, projectRoot, target, "search-replace", oldValue, newValue, "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--report-changed-only")
	})
}

// replaceRemoteMultisiteDomains updates wp_site and wp_blogs domain columns remotely.
func (a *App) replaceRemoteMultisiteDomains(ctx context.Context, projectRoot string, target RemoteTarget, oldBase string, newBase string) error {
	oldDomain := urlHost(oldBase)
	newDomain := urlHost(newBase)
	if oldDomain == "" || newDomain == "" || oldDomain == newDomain {
		return nil
	}
	if !a.remoteIsMultisite(ctx, projectRoot, target) {
		return nil
	}

	prefix := strings.TrimSpace(a.remoteWPOutput(ctx, projectRoot, target, "db", "prefix"))
	if prefix == "" || regexp.MustCompile(`[^A-Za-z0-9_]`).MatchString(prefix) {
		return nil
	}

	for _, table := range []string{prefix + "site", prefix + "blogs"} {
		if !a.remoteTableExists(ctx, projectRoot, target, table) {
			continue
		}
		query := fmt.Sprintf("UPDATE `%s` SET domain = %s WHERE domain = %s", table, sqlQuote(newDomain), sqlQuote(oldDomain))
		title := fmt.Sprintf("Replacing push target multisite domains in %s", table)
		if err := a.runStep(title, "Push target multisite domains replaced in "+table, func() error {
			return a.runRemoteWPWithFilteredWarnings(ctx, projectRoot, target, "db", "query", query)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) remoteIsMultisite(ctx context.Context, projectRoot string, target RemoteTarget) bool {
	if err := a.runRemoteWPSilent(ctx, projectRoot, target, "core", "is-installed", "--network"); err == nil {
		return true
	}
	value := a.remoteWPOutputSilent(ctx, projectRoot, target, "config", "get", "MULTISITE")
	return isTruthyConfigValue(value)
}

func (a *App) remoteTableExists(ctx context.Context, projectRoot string, target RemoteTarget, table string) bool {
	output := a.remoteWPOutput(ctx, projectRoot, target, "db", "tables", "--all-tables-with-prefix", "--format=csv")
	return lineSetContains(output, table)
}

func (a *App) runRemoteWPWithFilteredWarnings(ctx context.Context, projectRoot string, target RemoteTarget, args ...string) error {
	return a.runSSHWithFilteredWarnings(ctx, projectRoot, target, remoteWPCommand(target, args...))
}

func (a *App) runRemoteWPSilent(ctx context.Context, projectRoot string, target RemoteTarget, args ...string) error {
	return a.runSSHSilent(ctx, projectRoot, target, remoteWPCommand(target, args...))
}

// remoteWPOutput captures WP-CLI output from the push target and suppresses failures.
func (a *App) remoteWPOutput(ctx context.Context, projectRoot string, target RemoteTarget, args ...string) string {
	output, err := a.outputSSH(ctx, projectRoot, target, remoteWPCommand(target, args...))
	if err != nil {
		return ""
	}
	return output
}

func (a *App) remoteWPOutputSilent(ctx context.Context, projectRoot string, target RemoteTarget, args ...string) string {
	output, err := a.outputSSHSilent(ctx, projectRoot, target, remoteWPCommand(target, args...))
	if err != nil {
		return ""
	}
	return output
}

// remoteWPCommand constructs the remote shell command needed to run WP-CLI over SSH.
func remoteWPCommand(target RemoteTarget, args ...string) string {
	parts := []string{
		"set -eu;",
		"cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + ";",
		remoteWPCLIPrelude(target),
		"wp_ssh_wp",
		shellQuote("--allow-root"),
		shellQuote("--skip-plugins"),
		shellQuote("--skip-themes"),
	}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// pushURLCachePath stores the detected target URL between db-push and post-push.
func pushURLCachePath(projectRoot string) string {
	return filepath.Join(downloadsDir(projectRoot), "wp-ssh-push-url.txt")
}

// writePushURLCache persists the remote URL detected before DB import.
func writePushURLCache(projectRoot string, target RemoteTarget, value string) error {
	if value == "" {
		return nil
	}
	path := pushURLCachePath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("target=%s:%s\nurl=%s\n", sshTarget(target), trimTrailingSlash(target.RemotePath), trimTrailingSlash(value))
	return os.WriteFile(path, []byte(body), 0o644)
}

// readPushURLCache returns the pre-import target URL when available.
func readPushURLCache(projectRoot string, target RemoteTarget) string {
	contents, err := os.ReadFile(pushURLCachePath(projectRoot))
	if err != nil {
		return ""
	}
	expected := fmt.Sprintf("%s:%s", sshTarget(target), trimTrailingSlash(target.RemotePath))
	values := map[string]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["target"] != expected {
		return ""
	}
	return trimTrailingSlash(values["url"])
}
