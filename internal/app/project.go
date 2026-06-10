package app

import (
	"bufio"
	"encoding/json"
	"errors"
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
	modeStandalone runtimeMode = "standalone"
)

// runtimeContext records whether commands should use DDEV provider integration or direct local tools.
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

// detectRuntime uses `ddev describe -j` as the source of truth for DDEV mode.
func detectRuntime(start string) runtimeContext {
	abs, err := filepath.Abs(start)
	if err != nil {
		abs = start
	}

	if desc, ok := ddevDescribe(abs); ok {
		root := firstNonEmpty(desc.AppRoot, desc.Approot)
		if root == "" {
			if found, err := findProjectRoot(abs); err == nil {
				root = found
			}
		}
		if root == "" {
			root = abs
		}
		return runtimeContext{Mode: modeDDEV, Root: root, DDEV: desc}
	}

	return runtimeContext{Mode: modeStandalone, Root: abs}
}

// findProjectRoot walks upward to locate the DDEV project root.
func findProjectRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(current, ".ddev", "config.yaml")); err == nil {
			return current, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("no DDEV project found; run from inside a project or pass --project-root")
		}
		current = parent
	}
}

// hasDDEVConfig reports whether the selected root is a DDEV project directory.
func hasDDEVConfig(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".ddev", "config.yaml"))
	return err == nil
}

// downloadsDir returns the operation scratch directory for the selected runtime.
func downloadsDir(root string) string {
	if hasDDEVConfig(root) {
		return filepath.Join(root, ".ddev", ".downloads")
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
	root := localWPRoot(projectRoot, cfg)
	rel, err := filepath.Rel(projectRoot, root)
	if err != nil || rel == "." {
		return "/var/www/html"
	}
	return "/var/www/html/" + filepath.ToSlash(rel)
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

	if desc, ok := ddevDescribe(projectRoot); ok && desc.PrimaryURL != "" {
		return trimTrailingSlash(desc.PrimaryURL)
	}
	return ""
}
