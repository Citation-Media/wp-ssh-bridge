package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// wpEnvContainerRoot is the fixed path where wp-env mounts WordPress inside its containers.
const wpEnvContainerRoot = "/var/www/html"

// wpEnvStatusTimeout bounds the `wp-env status --json` probe.
const wpEnvStatusTimeout = 60 * time.Second

// wpEnvDockerRuntime is the only wp-env runtime that supports `wp-env run`.
const wpEnvDockerRuntime = "docker"

// wpEnvRunningStatus is the `wp-env status --json` value for a started environment.
const wpEnvRunningStatus = "running"

// wpEnvWordPressDir is the directory below the wp-env install path that holds WordPress.
const wpEnvWordPressDir = "WordPress"

// WPEnvStatus contains the fields the CLI needs from `wp-env status --json`. Only fields
// with a stable type across wp-env versions are decoded: the reported ports are strings
// when running and null when stopped, and WP-CLI runs inside the container anyway, so the
// host-side MySQL port is never needed.
type WPEnvStatus struct {
	Status  string `json:"status"`
	Runtime string `json:"runtime"`
	URLs    struct {
		Development string `json:"development"`
	} `json:"urls"`
	InstallPath string `json:"installPath"`

	// mountedWordPressRoot is resolved from docker-compose.yml, not from the JSON.
	mountedWordPressRoot string
}

// wordPressRoot returns the host path that wp-env mounts at wpEnvContainerRoot. It is
// resolved from the generated docker-compose.yml at probe time, because a `core` entry in
// .wp-env.json makes wp-env mount a different directory than <installPath>/WordPress.
func (s WPEnvStatus) wordPressRoot() string {
	return s.mountedWordPressRoot
}

// defaultWordPressRoot is the mount source wp-env uses when .wp-env.json sets no `core`.
func (s WPEnvStatus) defaultWordPressRoot() string {
	if s.InstallPath == "" {
		return ""
	}
	return filepath.Join(s.InstallPath, wpEnvWordPressDir)
}

// composeListValues returns the scalar list-item values of the generated compose file.
// js-yaml emits strings beyond its line width as folded (>-) block scalars wrapped at
// spaces, so continuation lines are rejoined into one logical value; a mount source whose
// path contains a space would otherwise be unparseable.
func composeListValues(contents string) []string {
	values := []string{}
	lines := strings.Split(contents, "\n")
	for index := 0; index < len(lines); index++ {
		raw := lines[index]
		line := strings.TrimSpace(raw)
		if line != "-" && !strings.HasPrefix(line, "- ") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if item == ">" || item == ">-" || item == "|" || item == "|-" {
			indent := leadingSpaces(raw)
			parts := []string{}
			for index+1 < len(lines) {
				next := lines[index+1]
				if strings.TrimSpace(next) == "" || leadingSpaces(next) <= indent {
					break
				}
				parts = append(parts, strings.TrimSpace(next))
				index++
			}
			item = strings.Join(parts, " ")
		}
		item = strings.Trim(item, `"'`)
		if item != "" {
			values = append(values, item)
		}
	}
	return values
}

// wpEnvMountedWordPressRoot reads the host side of the wpEnvContainerRoot bind mount from
// the compose file wp-env generates, so a custom `core` source is honored. The development
// wordpress service is emitted before the tests services, so the first match is the
// development mount.
func wpEnvMountedWordPressRoot(installPath string) (string, bool) {
	contents, err := os.ReadFile(filepath.Join(installPath, "docker-compose.yml"))
	if err != nil {
		return "", false
	}

	suffix := ":" + wpEnvContainerRoot
	for _, value := range composeListValues(string(contents)) {
		if !strings.HasSuffix(value, suffix) {
			continue
		}
		if host := strings.TrimSuffix(value, suffix); host != "" && filepath.IsAbs(host) {
			return host, true
		}
	}
	return "", false
}

// wpEnvContainerMounts lists the container paths below wpEnvContainerRoot that wp-env
// bind-mounts individually (plugins, themes, mappings entries). On the host those paths
// are shadowed by the mounts, and rsync --delete against a live mountpoint can fail with
// EBUSY, so pulls exclude them.
func wpEnvContainerMounts(installPath string) []string {
	contents, err := os.ReadFile(filepath.Join(installPath, "docker-compose.yml"))
	if err != nil {
		return nil
	}

	prefix := wpEnvContainerRoot + "/"
	seen := map[string]bool{}
	mounts := []string{}
	for _, value := range composeListValues(string(contents)) {
		separator := strings.LastIndex(value, ":")
		if separator <= 0 {
			continue
		}
		containerPath := value[separator+1:]
		if !strings.HasPrefix(containerPath, prefix) {
			continue
		}
		relative := strings.TrimPrefix(containerPath, prefix)
		if relative == "" || seen[relative] {
			continue
		}
		seen[relative] = true
		mounts = append(mounts, relative)
	}
	return mounts
}

// running reports whether the environment is started and can serve WP-CLI commands.
func (s WPEnvStatus) running() bool {
	return s.Status == wpEnvRunningStatus
}

// supportsRun reports whether the active runtime implements `wp-env run`.
// The experimental Playground runtime does not, so WP-CLI cannot be reached there.
func (s WPEnvStatus) supportsRun() bool {
	return s.Runtime == "" || s.Runtime == wpEnvDockerRuntime
}

// wpEnvConfigNames are the project files that mark a wp-env project.
var wpEnvConfigNames = []string{".wp-env.json", ".wp-env.override.json"}

// findWPEnvRoot walks upward to locate the wp-env project root. The walk stops at the
// first directory that is a project of its own (a wp-ssh config or a DDEV config without a
// wp-env marker), so an ancestor .wp-env.json — a block playground above a site checkout,
// or a stray file in $HOME — cannot hijack a nested project.
func findWPEnvRoot(start string) (string, bool) {
	stopped := false
	root, ok := walkUp(start, func(dir string) bool {
		if hasWPEnvProjectMarker(dir) {
			return true
		}
		if fileExists(projectConfigPath(dir)) || fileExists(standaloneConfigPath(dir)) || hasDDEVConfig(dir) {
			stopped = true
			return true
		}
		return false
	})
	if !ok || stopped {
		return "", false
	}
	return root, true
}

// hasWPEnvProjectMarker gates wp-env detection so unrelated projects never shell out to it.
// Only an explicit config file counts: node_modules/.bin/wp-env is present in any repo that
// merely lists @wordpress/env as a dev dependency, including DDEV and standalone sites that
// must not be redirected into a wp-env WordPress tree.
func hasWPEnvProjectMarker(dir string) bool {
	for _, name := range wpEnvConfigNames {
		if fileExists(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// wpEnvLocalBinary returns the project-local wp-env executable path.
func wpEnvLocalBinary(dir string) string {
	return filepath.Join(dir, "node_modules", ".bin", "wp-env")
}

// wpEnvCommand resolves how to invoke wp-env, preferring a project-local install so the
// project's own @wordpress/env version is used instead of an unrelated global one.
func wpEnvCommand(projectRoot string) (string, []string) {
	if local := wpEnvLocalBinary(projectRoot); fileExists(local) {
		return local, nil
	}
	if commandExists("wp-env") {
		return "wp-env", nil
	}
	return "npx", []string{"--yes", "@wordpress/env"}
}

// wpEnvArgs builds a full wp-env argument list without aliasing the resolved base args.
func wpEnvArgs(base []string, args ...string) []string {
	full := make([]string, 0, len(base)+len(args))
	full = append(full, base...)
	return append(full, args...)
}

// wpEnvRunCLIArgs builds the `wp-env run cli` invocation used for local WP-CLI commands.
// wp-env writes its own progress banners to stderr, so WP-CLI stdout stays parseable.
func wpEnvRunCLIArgs(base []string, containerPath string, args ...string) []string {
	full := wpEnvArgs(base, "run", "cli", "wp", "--path="+containerPath, "--allow-root", "--skip-plugins", "--skip-themes")
	return append(full, args...)
}

type wpEnvStatusResult struct {
	status WPEnvStatus
	ok     bool
}

var wpEnvStatusCache = struct {
	sync.Mutex
	values map[string]wpEnvStatusResult
}{
	values: map[string]wpEnvStatusResult{},
}

// wpEnvStatus reads structured wp-env metadata for a project root that has a wp-env marker.
func wpEnvStatus(projectRoot string) (WPEnvStatus, bool) {
	key, err := filepath.Abs(projectRoot)
	if err != nil {
		key = projectRoot
	}

	wpEnvStatusCache.Lock()
	if cached, ok := wpEnvStatusCache.values[key]; ok {
		wpEnvStatusCache.Unlock()
		return cached.status, cached.ok
	}
	wpEnvStatusCache.Unlock()

	status, ok := readWPEnvStatus(key)

	wpEnvStatusCache.Lock()
	wpEnvStatusCache.values[key] = wpEnvStatusResult{status: status, ok: ok}
	wpEnvStatusCache.Unlock()
	return status, ok
}

// readWPEnvStatus runs `wp-env status --json`, which reports the install path, URL, and
// runtime even when the environment is stopped. A stopped environment still resolves so
// preflight can report why it cannot run instead of falling back to standalone mode.
func readWPEnvStatus(projectRoot string) (WPEnvStatus, bool) {
	// Resolve the actual project root so subdirectories and explicit --project-root values
	// inside a wp-env project probe the same environment the project root does.
	root, ok := findWPEnvRoot(projectRoot)
	if !ok {
		return WPEnvStatus{}, false
	}

	// The npx fallback resolves @wordpress/env over the network and `status` talks to the
	// Docker daemon; without a deadline a stalled proxy or wedged daemon hangs the CLI
	// before any output.
	ctx, cancel := context.WithTimeout(context.Background(), wpEnvStatusTimeout)
	defer cancel()

	name, base := wpEnvCommand(root)
	cmd := exec.CommandContext(ctx, name, wpEnvArgs(base, "status", "--json")...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return WPEnvStatus{}, false
	}

	var status WPEnvStatus
	if err := json.Unmarshal(bytes.TrimSpace(output), &status); err != nil {
		return WPEnvStatus{}, false
	}
	if status.InstallPath == "" {
		return WPEnvStatus{}, false
	}
	if root, ok := wpEnvMountedWordPressRoot(status.InstallPath); ok {
		status.mountedWordPressRoot = root
	} else {
		status.mountedWordPressRoot = status.defaultWordPressRoot()
	}
	return status, true
}

// wpEnvContainerPath maps a host path inside the wp-env WordPress tree to its container path.
func wpEnvContainerPath(status WPEnvStatus, hostPath string) (string, bool) {
	root := status.wordPressRoot()
	if root == "" {
		return "", false
	}
	rel, ok := projectRelativePath(root, hostPath)
	if !ok {
		return "", false
	}
	if rel == "." {
		return wpEnvContainerRoot, true
	}
	return wpEnvContainerRoot + "/" + rel, true
}

// wpEnvMountedSources reports the .wp-env.json keys that bind-mount host directories into
// wp-content. Those mounts shadow the matching directories in the WordPress tree, so files
// pulled into them on the host are never visible to WordPress.
func wpEnvMountedSources(projectRoot string) []string {
	keys := []string{}
	seen := map[string]bool{}
	for _, name := range wpEnvConfigNames {
		for _, key := range wpEnvMountKeysInFile(filepath.Join(projectRoot, name)) {
			if seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func wpEnvMountKeysInFile(path string) []string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var config struct {
		Plugins  []string                   `json:"plugins"`
		Themes   []string                   `json:"themes"`
		Mappings map[string]json.RawMessage `json:"mappings"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		return nil
	}

	keys := []string{}
	if len(config.Plugins) > 0 {
		keys = append(keys, "plugins")
	}
	if len(config.Themes) > 0 {
		keys = append(keys, "themes")
	}
	if len(config.Mappings) > 0 {
		keys = append(keys, "mappings")
	}
	return keys
}

// warnWPEnvMountedSources warns before a file pull writes into wp-content directories that
// wp-env shadows with bind mounts. rsync writes to the host tree, but the container reads
// the mount, so pulled files in those paths are invisible to WordPress.
func (a *App) warnWPEnvMountedSources(projectRoot string, opts configOptions) {
	if opts.SkipFiles {
		return
	}
	keys := wpEnvMountedSources(projectRoot)
	if len(keys) == 0 {
		return
	}
	a.UI.Warning("wp-env mounts %s over wp-content; files pulled into those paths stay hidden from WordPress. Use --skip-files to sync only the database.", strings.Join(keys, " and "))
}

// wpEnvDownloadsGuard denies web access to the scratch directory. In wp-env mode the
// staged database dump necessarily lives inside the tree wp-env mounts at /var/www/html
// and serves over HTTP, so without this the production dump is downloadable from the
// development URL. Apache's stock config only protects .ht* files.
const wpEnvDownloadsGuard = `# Generated by wp-ssh-bridge. Keeps staged database dumps off the web.
<IfModule mod_authz_core.c>
    Require all denied
</IfModule>
<IfModule !mod_authz_core.c>
    Order allow,deny
    Deny from all
</IfModule>
`

// protectDownloadsDir drops a deny-all .htaccess next to staged dumps when the scratch
// directory sits inside a web-served tree.
func protectDownloadsDir(root string, dir string) error {
	if !isWPEnvRoot(root) {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, ".htaccess"), []byte(wpEnvDownloadsGuard), 0o644)
}

// isWPEnvRoot reports whether wp-env routing applies to this project root. When detection
// has run it is authoritative, so a pinned or DDEV-resolved project is never redirected
// into a wp-env WordPress tree; otherwise it falls back to probing.
func isWPEnvRoot(root string) bool {
	switch modeForRoot(root) {
	case modeWPEnv:
		return true
	case modeDDEV, modeStandalone:
		return false
	}
	_, ok := wpEnvStatus(root)
	return ok
}
