package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// DDEVDescription contains the fields the CLI needs from `ddev describe -j`.
type DDEVDescription struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	PrimaryURL string `json:"primary_url"`
	AppRoot    string `json:"app_root"`
	Approot    string `json:"approot"`
}

type runtimeMode string

const (
	modeDDEV       runtimeMode = "ddev"
	modeWPEnv      runtimeMode = "wp-env"
	modeStandalone runtimeMode = "standalone"
)

// runtimeContext records whether commands should use DDEV provider integration, wp-env
// container commands, or direct local tools. wp-env state is deliberately not snapshotted
// here: consumers read the memoized wpEnvStatus so they cannot see stale data.
type runtimeContext struct {
	Mode runtimeMode
	Root string
	DDEV DDEVDescription
}

type ddevDescribeResult struct {
	desc DDEVDescription
	ok   bool
}

var ddevDescribeCache = struct {
	sync.Mutex
	values map[string]ddevDescribeResult
}{
	values: map[string]ddevDescribeResult{},
}

// resolvedModeCache records the mode detectRuntime settled on for a project root. Path and
// command helpers consult it instead of re-probing, so a pinned integration is honored
// everywhere and DDEV/wp-env precedence cannot differ between helpers.
var resolvedModeCache = struct {
	sync.Mutex
	values map[string]runtimeMode
}{
	values: map[string]runtimeMode{},
}

func rememberRuntimeMode(root string, mode runtimeMode) {
	key, err := filepath.Abs(root)
	if err != nil {
		key = root
	}
	resolvedModeCache.Lock()
	resolvedModeCache.values[key] = mode
	resolvedModeCache.Unlock()
}

// modeForRoot returns the resolved mode, or "" when detection has not run for this root.
func modeForRoot(root string) runtimeMode {
	key, err := filepath.Abs(root)
	if err != nil {
		key = root
	}
	resolvedModeCache.Lock()
	defer resolvedModeCache.Unlock()
	return resolvedModeCache.values[key]
}

// integrationEnvVar pins the runtime mode without editing the project config file.
const integrationEnvVar = "WP_SSH_INTEGRATION"

// parseIntegration validates an explicitly pinned runtime mode.
func parseIntegration(value string) (runtimeMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case string(modeDDEV):
		return modeDDEV, nil
	case string(modeWPEnv), "wpenv":
		return modeWPEnv, nil
	case string(modeStandalone):
		return modeStandalone, nil
	default:
		return "", fmt.Errorf("unknown integration %q; use ddev, wp-env, or standalone", value)
	}
}

// findIntegrationInConfig reads a pinned integration without running detection first.
// This is not circular: DDEV config always lives at .ddev/wp-ssh.yaml and every other mode
// at .wp-ssh.yaml, so both candidates can be probed by filename alone. An explicit config
// file — the --config-file flag or WP_SSH_CONFIG_FILE — is the sole source when given, so
// the pin always comes from the same file the rest of the config is loaded from.
func findIntegrationInConfig(start string, explicitConfig string) string {
	if explicit := firstNonEmpty(explicitConfig, os.Getenv("WP_SSH_CONFIG_FILE")); explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			// The loader resolves a relative config path against the project root, which is
			// unknown before detection. Walking up from the start directory reads the same
			// file whenever the command runs inside the project.
			if dir, ok := walkUp(start, func(dir string) bool {
				return fileExists(filepath.Join(dir, explicit))
			}); ok {
				path = filepath.Join(dir, explicit)
			}
		}
		value, _ := readSimpleYAMLValue(path, "integration")
		return value
	}

	found := ""
	// Stop at the first directory that owns a config file. Continuing past it would let an
	// unrelated ancestor .wp-ssh.yaml pin — and, since pins are strict, hard-fail — every
	// project nested beneath it.
	walkUp(start, func(dir string) bool {
		owned := false
		for _, candidate := range []string{projectConfigPath(dir), standaloneConfigPath(dir)} {
			if !fileExists(candidate) {
				continue
			}
			owned = true
			if value, err := readSimpleYAMLValue(candidate, "integration"); err == nil && value != "" {
				found = value
			}
		}
		return owned
	})
	return found
}

// detectRuntime resolves the runtime mode, preferring an explicit pin over discovery.
// Both `ddev describe` and `wp-env status` are subprocesses, so each is gated behind a
// filesystem marker check first. DDEV always needs .ddev/config.yaml at the project root,
// so its absence above the working directory rules out DDEV mode without spawning ddev.
//
// A pinned integration is strict: it fails instead of silently falling back to standalone
// mode, which would otherwise write files to a different local WordPress root.
func detectRuntime(start string, explicit string, explicitConfig string) (runtimeContext, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}

	pinned, err := parseIntegration(firstNonEmpty(explicit, os.Getenv(integrationEnvVar), findIntegrationInConfig(abs, explicitConfig)))
	if err != nil {
		return runtimeContext{}, err
	}

	ddevMarkerFound := false
	if pinned == "" || pinned == modeDDEV {
		ddevRoot, rootErr := findProjectRoot(abs)
		ddevMarkerFound = rootErr == nil
		if ddevMarkerFound {
			if desc, ok := ddevDescribe(abs); ok {
				root := firstNonEmpty(desc.AppRoot, desc.Approot, ddevRoot, abs)
				rememberRuntimeMode(root, modeDDEV)
				return runtimeContext{Mode: modeDDEV, Root: root, DDEV: desc}, nil
			}
		}
		if pinned == modeDDEV {
			if !ddevMarkerFound {
				return runtimeContext{}, errors.New("integration is pinned to ddev, but no .ddev/config.yaml was found above the working directory")
			}
			return runtimeContext{}, errors.New("integration is pinned to ddev, but `ddev describe -j` did not succeed; start the project with ddev start")
		}
	}

	if pinned == "" || pinned == modeWPEnv {
		if root, ok := findWPEnvRoot(abs); ok {
			if _, ok := wpEnvStatus(root); ok {
				rememberRuntimeMode(root, modeWPEnv)
				return runtimeContext{Mode: modeWPEnv, Root: root}, nil
			}
			// The project is definitely a wp-env project. Falling through to standalone
			// mode here would point the local WordPress root at the repository itself, and
			// a pull would then rsync the remote tree over the working copy with --delete.
			message := "this is a wp-env project but `wp-env status --json` did not succeed; start it with wp-env start, or install wp-env (npm install --save-dev @wordpress/env). Pass --integration standalone or set WP_SSH_INTEGRATION=standalone to override"
			if ddevMarkerFound {
				message += ". The repository also contains a DDEV project; if DDEV is intended, run ddev start or pass --integration ddev"
			}
			return runtimeContext{}, errors.New(message)
		}
		if pinned == modeWPEnv {
			return runtimeContext{}, errors.New("integration is pinned to wp-env, but no .wp-env.json was found above the working directory")
		}
	}

	rememberRuntimeMode(abs, modeStandalone)
	return runtimeContext{Mode: modeStandalone, Root: abs}, nil
}

// walkUp ascends from start toward the filesystem root until probe accepts a directory.
func walkUp(start string, probe func(dir string) bool) (string, bool) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}

	for {
		if probe(current) {
			return current, true
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

// findProjectRoot walks upward to locate the DDEV project root.
func findProjectRoot(start string) (string, error) {
	if root, ok := walkUp(start, hasDDEVConfig); ok {
		return root, nil
	}
	return "", errors.New("no DDEV project found; run from inside a project or pass --project-root")
}

// hasDDEVConfig reports whether the selected root is a DDEV project directory.
func hasDDEVConfig(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".ddev", "config.yaml"))
	return err == nil
}

// downloadsDir returns the operation scratch directory for the selected runtime.
func downloadsDir(root string) string {
	switch modeForRoot(root) {
	case modeDDEV:
		return filepath.Join(root, ".ddev", ".downloads")
	case modeStandalone:
		return filepath.Join(root, ".wp-ssh", ".downloads")
	case modeWPEnv:
		// Must come before the hasDDEVConfig probe: in a repo carrying both configs the
		// scratch dir has to live inside the tree wp-env mounts, or the container cannot
		// read the staged dump.
		if status, ok := wpEnvStatus(root); ok {
			return filepath.Join(status.wordPressRoot(), ".wp-ssh", ".downloads")
		}
		return filepath.Join(root, ".wp-ssh", ".downloads")
	}
	if hasDDEVConfig(root) {
		return filepath.Join(root, ".ddev", ".downloads")
	}
	// wp-env keeps WordPress outside the project directory and only mounts that tree into
	// its containers, so scratch files must live inside it to be readable by `wp-env run`.
	// The .wp-ssh/ rsync exclude keeps a file pull from deleting them.
	if status, ok := wpEnvStatus(root); ok {
		return filepath.Join(status.wordPressRoot(), ".wp-ssh", ".downloads")
	}
	return filepath.Join(root, ".wp-ssh", ".downloads")
}

// ddevDocroot returns the configured docroot relative to the DDEV project root.
func ddevDocroot(projectRoot string) string {
	if value := os.Getenv("DDEV_DOCROOT"); value != "" && value != "." {
		return strings.Trim(value, "/")
	}

	value, err := readSimpleYAMLValue(filepath.Join(projectRoot, ".ddev", "config.yaml"), "docroot")
	if err != nil {
		return ""
	}
	return strings.Trim(value, "/")
}

// ddevProjectType reads the local DDEV project type when config.yaml is available.
func ddevProjectType(projectRoot string) string {
	if desc, ok := ddevDescribe(projectRoot); ok && desc.Type != "" {
		return desc.Type
	}

	value, err := readSimpleYAMLValue(filepath.Join(projectRoot, ".ddev", "config.yaml"), "type")
	if err != nil {
		return ""
	}
	return value
}

// applyDDEVDefaults sets local project defaults before config is written or used.
func applyDDEVDefaults(projectRoot string, cfg *Config) {
	if cfg.Provider == "" {
		cfg.Provider = defaultProviderName
	}
	if cfg.RemoteTmpDir == "" {
		cfg.RemoteTmpDir = "/tmp"
	}
	if cfg.PushRemoteTmpDir == "" {
		cfg.PushRemoteTmpDir = "/tmp"
	}
	if cfg.LocalWPPath == "" {
		cfg.LocalWPPath = ddevDocroot(projectRoot)
	}
	if cfg.LocalURL != "" {
		return
	}
	if desc, ok := ddevDescribe(projectRoot); ok && desc.PrimaryURL != "" {
		cfg.LocalURL = trimTrailingSlash(desc.PrimaryURL)
	}
}

// ddevDescribe reads structured DDEV metadata when the project can be described.
func ddevDescribe(projectRoot string) (DDEVDescription, bool) {
	key, err := filepath.Abs(projectRoot)
	if err != nil {
		key = projectRoot
	}

	ddevDescribeCache.Lock()
	if cached, ok := ddevDescribeCache.values[key]; ok {
		ddevDescribeCache.Unlock()
		return cached.desc, cached.ok
	}
	ddevDescribeCache.Unlock()

	cmd := exec.Command("ddev", "describe", "-j")
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		ddevDescribeCache.Lock()
		ddevDescribeCache.values[key] = ddevDescribeResult{}
		ddevDescribeCache.Unlock()
		return DDEVDescription{}, false
	}

	var desc DDEVDescription
	if err := json.Unmarshal(output, &desc); err != nil {
		ddevDescribeCache.Lock()
		ddevDescribeCache.values[key] = ddevDescribeResult{}
		ddevDescribeCache.Unlock()
		return DDEVDescription{}, false
	}
	ddevDescribeCache.Lock()
	ddevDescribeCache.values[key] = ddevDescribeResult{desc: desc, ok: true}
	ddevDescribeCache.Unlock()
	return desc, true
}

// readSimpleYAMLValue reads top-level scalar config values used by DDEV config.yaml.
func readSimpleYAMLValue(path string, key string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		currentKey, raw, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(currentKey) != key {
			continue
		}
		value := strings.TrimSpace(raw)
		value = strings.Trim(value, `"'`)
		return value, nil
	}
	return "", scanner.Err()
}

// localWPRoot returns the host-side WordPress root path.
func localWPRoot(projectRoot string, cfg Config) string {
	localPath := cfg.LocalWPPath
	if localPath == "" || localPath == "." {
		// The wp-env install path contains a machine-specific hash, so it is resolved on
		// demand instead of being persisted into the project config file. Only consult it
		// in wp-env mode: a DDEV or standalone project that merely carries a .wp-env.json
		// must keep its own WordPress root.
		if isWPEnvRoot(projectRoot) {
			if status, ok := wpEnvStatus(projectRoot); ok {
				if root := status.wordPressRoot(); root != "" {
					return root
				}
			}
		}
		localPath = ddevDocroot(projectRoot)
	}
	if localPath == "" || localPath == "." {
		return projectRoot
	}
	if filepath.IsAbs(localPath) {
		return filepath.Clean(localPath)
	}
	return filepath.Join(projectRoot, localPath)
}

// containerWPPath returns the container-side WordPress root path used with ddev wp.
func containerWPPath(projectRoot string, cfg Config) string {
	if path, ok := containerProjectPath(projectRoot, localWPRoot(projectRoot, cfg)); ok {
		return path
	}
	return "/var/www/html"
}

// containerProjectPath maps a host project path to DDEV's container mount path.
func containerProjectPath(projectRoot string, hostPath string) (string, bool) {
	rel, ok := projectRelativePath(projectRoot, hostPath)
	if !ok {
		return "", false
	}
	if rel == "." {
		return "/var/www/html", true
	}
	return "/var/www/html/" + rel, true
}

// projectRelativePath returns a slash-separated path only when hostPath is inside projectRoot.
func projectRelativePath(projectRoot string, hostPath string) (string, bool) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", false
	}
	path, err := filepath.Abs(hostPath)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	return filepath.ToSlash(rel), true
}

// localSiteURL detects the local DDEV URL for post-pull search replacement.
func localSiteURL(projectRoot string, cfg Config) string {
	if cfg.LocalURL != "" {
		return trimTrailingSlash(cfg.LocalURL)
	}
	if value := os.Getenv("DDEV_PRIMARY_URL_WITHOUT_PORT"); value != "" {
		return trimTrailingSlash(value)
	}
	if value := os.Getenv("DDEV_PRIMARY_URL"); value != "" {
		return trimTrailingSlash(value)
	}

	if isDDEVRoot(projectRoot) {
		if desc, ok := ddevDescribe(projectRoot); ok && desc.PrimaryURL != "" {
			return trimTrailingSlash(desc.PrimaryURL)
		}
	}
	if isWPEnvRoot(projectRoot) {
		if status, ok := wpEnvStatus(projectRoot); ok && status.URLs.Development != "" {
			return trimTrailingSlash(status.URLs.Development)
		}
	}
	return ""
}

// isDDEVRoot reports whether DDEV routing applies to this project root. When detection has
// run it is authoritative, so a project pinned to wp-env or standalone is never routed
// through `ddev wp`; otherwise it falls back to probing.
func isDDEVRoot(root string) bool {
	switch modeForRoot(root) {
	case modeDDEV:
		return true
	case modeWPEnv, modeStandalone:
		return false
	}
	_, ok := ddevDescribe(root)
	return ok
}
