package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// primeWPEnvStatus seeds the wp-env status cache so path and command helpers can be tested
// without a running environment. Tests using it must not run in parallel.
func primeWPEnvStatus(t *testing.T, root string, status WPEnvStatus) {
	t.Helper()
	key, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}

	wpEnvStatusCache.Lock()
	wpEnvStatusCache.values[key] = wpEnvStatusResult{status: status, ok: true}
	wpEnvStatusCache.Unlock()

	resolvedModeCache.Lock()
	resolvedModeCache.values[key] = modeWPEnv
	resolvedModeCache.Unlock()

	t.Cleanup(func() {
		wpEnvStatusCache.Lock()
		delete(wpEnvStatusCache.values, key)
		wpEnvStatusCache.Unlock()
		resolvedModeCache.Lock()
		delete(resolvedModeCache.values, key)
		resolvedModeCache.Unlock()
	})
}

func runningWPEnvStatus(installPath string) WPEnvStatus {
	status := WPEnvStatus{
		Status:      wpEnvRunningStatus,
		Runtime:     wpEnvDockerRuntime,
		InstallPath: installPath,
	}
	status.URLs.Development = "http://localhost:8899"
	status.mountedWordPressRoot = status.defaultWordPressRoot()
	return status
}

// TestWPEnvStatusDecodesRealOutput pins the decoder against the exact `wp-env status --json`
// payloads emitted by a running and a stopped environment. A stopped environment reports
// null ports and URLs but still reports the install path, so detection must survive it.
func TestWPEnvStatusDecodesRealOutput(t *testing.T) {
	t.Parallel()

	running := `{"status":"running","runtime":"docker","urls":{"development":"http://localhost:8899","phpmyadmin":null},"ports":{"development":"8899","mysql":"32802"},"config":{"multisite":false,"xdebug":"off"},"configPath":"/src/project","installPath":"/home/dev/.wp-env/wp-env-project-03f9a59b"}`
	stopped := `{"status":"stopped","runtime":"docker","urls":{"development":null,"phpmyadmin":null},"ports":{"development":null,"mysql":null},"config":{"multisite":false,"xdebug":"off"},"configPath":"/src/project","installPath":"/home/dev/.wp-env/wp-env-project-03f9a59b"}`

	var status WPEnvStatus
	if err := json.Unmarshal([]byte(running), &status); err != nil {
		t.Fatalf("decoding running status: %v", err)
	}
	if !status.running() || !status.supportsRun() {
		t.Fatalf("running status decoded as %#v", status)
	}
	if status.URLs.Development != "http://localhost:8899" {
		t.Fatalf("development URL = %q", status.URLs.Development)
	}
	if status.InstallPath != "/home/dev/.wp-env/wp-env-project-03f9a59b" {
		t.Fatalf("install path = %q", status.InstallPath)
	}

	var idle WPEnvStatus
	if err := json.Unmarshal([]byte(stopped), &idle); err != nil {
		t.Fatalf("decoding stopped status: %v", err)
	}
	if idle.running() {
		t.Fatal("stopped status decoded as running")
	}
	if idle.defaultWordPressRoot() == "" {
		t.Fatal("stopped status lost the install path, so preflight cannot explain the failure")
	}
}

func TestWPEnvStatusAccessors(t *testing.T) {
	t.Parallel()

	status := runningWPEnvStatus("/home/dev/.wp-env/wp-env-demo-abc123")
	if got, want := status.wordPressRoot(), filepath.Join("/home/dev/.wp-env/wp-env-demo-abc123", "WordPress"); got != want {
		t.Fatalf("wordPressRoot() = %q, want %q", got, want)
	}
	if !status.running() {
		t.Fatal("running() = false for a started environment")
	}
	if !status.supportsRun() {
		t.Fatal("supportsRun() = false for the docker runtime")
	}

	stopped := WPEnvStatus{Status: "stopped", Runtime: wpEnvDockerRuntime, InstallPath: "/tmp/x"}
	if stopped.running() {
		t.Fatal("running() = true for a stopped environment")
	}

	// The Playground runtime runs WordPress in WebAssembly and has no `wp-env run`.
	playground := WPEnvStatus{Status: wpEnvRunningStatus, Runtime: "playground", InstallPath: "/tmp/x"}
	if playground.supportsRun() {
		t.Fatal("supportsRun() = true for the playground runtime")
	}

	if (WPEnvStatus{}).wordPressRoot() != "" {
		t.Fatal("wordPressRoot() should be empty without an install path")
	}
}

func TestWPEnvContainerPath(t *testing.T) {
	t.Parallel()

	status := runningWPEnvStatus("/home/dev/.wp-env/wp-env-demo-abc123")
	root := status.wordPressRoot()

	if got, ok := wpEnvContainerPath(status, root); !ok || got != wpEnvContainerRoot {
		t.Fatalf("wpEnvContainerPath(root) = %q, %v", got, ok)
	}

	dump := filepath.Join(root, ".wp-ssh", ".downloads", "db-1.sql")
	got, ok := wpEnvContainerPath(status, dump)
	if !ok {
		t.Fatal("wpEnvContainerPath() rejected a path inside the WordPress tree")
	}
	if want := wpEnvContainerRoot + "/.wp-ssh/.downloads/db-1.sql"; got != want {
		t.Fatalf("wpEnvContainerPath(dump) = %q, want %q", got, want)
	}

	if _, ok := wpEnvContainerPath(status, "/somewhere/else/db.sql"); ok {
		t.Fatal("wpEnvContainerPath() accepted a path outside the WordPress tree")
	}
	if _, ok := wpEnvContainerPath(WPEnvStatus{}, "/tmp/db.sql"); ok {
		t.Fatal("wpEnvContainerPath() accepted a status without an install path")
	}
}

func TestWPEnvRunCLIArgsDoesNotAliasBase(t *testing.T) {
	t.Parallel()

	base := []string{"--yes", "@wordpress/env"}
	first := wpEnvRunCLIArgs(base, wpEnvContainerRoot, "db", "import", "/var/www/html/db.sql")
	second := wpEnvRunCLIArgs(base, wpEnvContainerRoot, "option", "get", "home")

	if len(base) != 2 || base[0] != "--yes" || base[1] != "@wordpress/env" {
		t.Fatalf("wpEnvRunCLIArgs() mutated its base args: %#v", base)
	}
	joined := strings.Join(first, " ")
	for _, want := range []string{"run cli wp", "--path=" + wpEnvContainerRoot, "--allow-root", "--skip-plugins", "--skip-themes", "db import /var/www/html/db.sql"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wpEnvRunCLIArgs() missing %q in %q", want, joined)
		}
	}
	if strings.Contains(strings.Join(second, " "), "db import") {
		t.Fatalf("wpEnvRunCLIArgs() leaked args between calls: %#v", second)
	}
}

func TestFindWPEnvRootWalksUp(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "src", "blocks")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := findWPEnvRoot(nested); ok {
		t.Fatal("findWPEnvRoot() matched a directory without a wp-env marker")
	}

	if err := os.WriteFile(filepath.Join(root, ".wp-env.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, ok := findWPEnvRoot(nested)
	if !ok {
		t.Fatal("findWPEnvRoot() did not walk up to the project root")
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}
	if want, err := filepath.EvalSymlinks(root); err == nil && found != want {
		t.Fatalf("findWPEnvRoot() = %q, want %q", found, want)
	}
}

func TestHasWPEnvProjectMarkerRequiresConfigFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if hasWPEnvProjectMarker(dir) {
		t.Fatal("hasWPEnvProjectMarker() = true for an empty directory")
	}

	// A dev dependency is not a marker: any repo listing @wordpress/env has this binary,
	// including DDEV and standalone sites that must keep their own WordPress root.
	binary := wpEnvLocalBinary(dir)
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasWPEnvProjectMarker(dir) {
		t.Fatal("hasWPEnvProjectMarker() accepted a bare node_modules/.bin/wp-env")
	}

	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(override, ".wp-env.override.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasWPEnvProjectMarker(override) {
		t.Fatal("hasWPEnvProjectMarker() ignored .wp-env.override.json")
	}
}

func TestWPEnvCommandPrefersProjectLocalBinary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binary := wpEnvLocalBinary(dir)
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	name, args := wpEnvCommand(dir)
	if name != binary || len(args) != 0 {
		t.Fatalf("wpEnvCommand() = %q %#v, want the project-local binary", name, args)
	}

	// Without a local install the fallback must still be invocable.
	name, args = wpEnvCommand(t.TempDir())
	if name == "npx" {
		if len(args) != 2 || args[0] != "--yes" || args[1] != "@wordpress/env" {
			t.Fatalf("wpEnvCommand() npx fallback args = %#v", args)
		}
	} else if name != "wp-env" {
		t.Fatalf("wpEnvCommand() fallback = %q, want wp-env or npx", name)
	}
}

func TestWPEnvMountedSources(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if got := wpEnvMountedSources(dir); len(got) != 0 {
		t.Fatalf("wpEnvMountedSources() = %#v for a directory without config", got)
	}

	config := `{"core": null, "plugins": ["."], "mappings": {"wp-content/mu-plugins": "./mu"}}`
	if err := os.WriteFile(filepath.Join(dir, ".wp-env.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	got := wpEnvMountedSources(dir)
	if strings.Join(got, ",") != "plugins,mappings" {
		t.Fatalf("wpEnvMountedSources() = %#v, want plugins and mappings", got)
	}

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, ".wp-env.json"), []byte(`{"core": null, "plugins": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := wpEnvMountedSources(empty); len(got) != 0 {
		t.Fatalf("wpEnvMountedSources() = %#v for empty mount lists", got)
	}
}

func TestWPEnvDownloadsDirAndLocalWPRoot(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	status := runningWPEnvStatus(install)
	primeWPEnvStatus(t, root, status)

	// Scratch files must land inside the tree wp-env mounts into its containers.
	wantDownloads := filepath.Join(status.wordPressRoot(), ".wp-ssh", ".downloads")
	if got := downloadsDir(root); got != wantDownloads {
		t.Fatalf("downloadsDir() = %q, want %q", got, wantDownloads)
	}

	if got := localWPRoot(root, Config{}); got != status.wordPressRoot() {
		t.Fatalf("localWPRoot() = %q, want %q", got, status.wordPressRoot())
	}

	// An explicit local_wp_path still wins so unusual layouts stay configurable.
	custom := filepath.Join(install, "custom")
	if got := localWPRoot(root, Config{LocalWPPath: custom}); got != custom {
		t.Fatalf("localWPRoot() with explicit path = %q, want %q", got, custom)
	}

	if got := localSiteURL(root, Config{}); got != "http://localhost:8899" {
		t.Fatalf("localSiteURL() = %q, want the wp-env development URL", got)
	}
}

func TestWPEnvLocalWPCommandRunsInContainer(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	status := runningWPEnvStatus(install)
	primeWPEnvStatus(t, root, status)

	name, args := localWPCommand(root, Config{}, "db", "import", "/var/www/html/.wp-ssh/.downloads/db.sql")
	if name != "npx" && name != "wp-env" && !strings.HasSuffix(name, filepath.Join("node_modules", ".bin", "wp-env")) {
		t.Fatalf("localWPCommand() = %q, want a wp-env invocation", name)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"run cli wp", "--path=" + wpEnvContainerRoot, "db import /var/www/html/.wp-ssh/.downloads/db.sql"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("localWPCommand() missing %q in %q", want, joined)
		}
	}
	if commandLabel(name) != "wp-env" {
		t.Fatalf("commandLabel(%q) = %q, want wp-env", name, commandLabel(name))
	}
}

func TestWPEnvSkipsWPConfigURLConstants(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	status := runningWPEnvStatus(install)
	primeWPEnvStatus(t, root, status)

	wpRoot := status.wordPressRoot()
	if err := os.MkdirAll(wpRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// wp-env regenerates wp-config.php on every start, so the CLI must leave it alone.
	original := "<?php\ndefine( 'WP_HOME', 'https://production.example.com' );\n"
	wpConfig := filepath.Join(wpRoot, "wp-config.php")
	if err := os.WriteFile(wpConfig, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err := app.updateWPConfigURLConstants(root, Config{}, modeWPEnv); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(wpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != original {
		t.Fatalf("updateWPConfigURLConstants() rewrote wp-env's wp-config.php:\n%s", contents)
	}
}

func TestMigrateRejectsWPEnvProjects(t *testing.T) {
	t.Parallel()

	err := ensureMigrateAdapterSupported(wpEnvAdapter{standaloneAdapter{runtime: runtimeContext{Mode: modeWPEnv}}})
	if err == nil {
		t.Fatal("ensureMigrateAdapterSupported() allowed migrate in a wp-env project")
	}
	if !strings.Contains(err.Error(), "wp-env") {
		t.Fatalf("ensureMigrateAdapterSupported() error = %q, want it to name wp-env", err)
	}

	if err := ensureMigrateAdapterSupported(standaloneAdapter{}); err != nil {
		t.Fatalf("ensureMigrateAdapterSupported() rejected standalone mode: %v", err)
	}
}

// TestDetectRuntimeSkipsProbesWithoutMarkers covers the marker gating that keeps every CLI
// invocation from spawning `ddev describe` and `wp-env status`. A directory with neither
// marker must resolve to standalone mode from filesystem checks alone.
func TestDetectRuntimeSkipsProbesWithoutMarkers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := findProjectRoot(dir); err == nil {
		t.Skip("temp dir is inside a DDEV project")
	}

	runtime, err := detectRuntime(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != modeStandalone {
		t.Fatalf("detectRuntime() = %q, want standalone for a bare directory", runtime.Mode)
	}
	if hasWPEnvProjectMarker(dir) {
		t.Fatal("bare temp dir reported a wp-env marker")
	}
}

// TestPinnedIntegrationIsStrict covers the pin failing loudly instead of falling back to
// standalone mode, which would sync files into a different local WordPress root.
func TestPinnedIntegrationIsStrict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := findProjectRoot(dir); err == nil {
		t.Skip("temp dir is inside a DDEV project")
	}

	if _, err := detectRuntime(dir, "wp-env", ""); err == nil {
		t.Fatal("pinning wp-env in a non-wp-env directory silently fell back")
	}
	if _, err := detectRuntime(dir, "ddev", ""); err == nil {
		t.Fatal("pinning ddev in a non-DDEV directory silently fell back")
	}

	// Pinning standalone skips both probes entirely.
	runtime, err := detectRuntime(dir, "standalone", "")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Mode != modeStandalone {
		t.Fatalf("pinned standalone resolved to %q", runtime.Mode)
	}

	if _, err := detectRuntime(dir, "nonsense", ""); err == nil {
		t.Fatal("an unknown integration value was accepted")
	}
}

func TestParseIntegration(t *testing.T) {
	t.Parallel()

	cases := map[string]runtimeMode{
		"":           "",
		"ddev":       modeDDEV,
		"DDEV":       modeDDEV,
		"wp-env":     modeWPEnv,
		"wpenv":      modeWPEnv,
		" wp-env ":   modeWPEnv,
		"standalone": modeStandalone,
	}
	for input, want := range cases {
		got, err := parseIntegration(input)
		if err != nil {
			t.Fatalf("parseIntegration(%q) errored: %v", input, err)
		}
		if got != want {
			t.Fatalf("parseIntegration(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := parseIntegration("docker"); err == nil {
		t.Fatal("parseIntegration() accepted an unknown value")
	}
}

// TestFindIntegrationInConfig covers reading the pin before detection has run.
func TestFindIntegrationInConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	nested := filepath.Join(dir, "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findIntegrationInConfig(nested, ""); got != "" {
		t.Fatalf("findIntegrationInConfig() = %q for a project without config", got)
	}

	if err := os.WriteFile(standaloneConfigPath(dir), []byte("integration: \"wp-env\"\npull_host: \"example.com\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findIntegrationInConfig(nested, ""); got != "wp-env" {
		t.Fatalf("findIntegrationInConfig() = %q, want wp-env", got)
	}
}

// TestIntegrationRoundTripsThroughConfig covers `init --integration` persisting the pin so
// later runs skip detection instead of probing again.
func TestIntegrationRoundTripsThroughConfig(t *testing.T) {
	dir := t.TempDir()
	path := standaloneConfigPath(dir)

	// apply() runs on every pull/push, and those write config back, so it must NOT carry
	// a one-shot --integration flag into the file. Only init persists it.
	if got := (configOptions{Integration: "wp-env"}.apply(Config{Host: "example.com"})); got.Integration != "" {
		t.Fatalf("apply() persisted a one-shot integration flag: %q", got.Integration)
	}
	cfg := Config{Host: "example.com", Integration: "wp-env"}

	if err := writeConfigFile(path, cfg, defaultConfig()); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfigPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Integration != "wp-env" {
		t.Fatalf("loaded integration = %q, want wp-env", loaded.Integration)
	}
	if got := findIntegrationInConfig(dir, ""); got != "wp-env" {
		t.Fatalf("findIntegrationInConfig() = %q, want wp-env", got)
	}
}

func TestAdapterForRuntimeSelectsWPEnv(t *testing.T) {
	t.Parallel()

	adapter := adapterForRuntime(runtimeContext{Mode: modeWPEnv, Root: "/tmp/project"})
	if _, ok := adapter.(wpEnvAdapter); !ok {
		t.Fatalf("adapterForRuntime() = %T, want wpEnvAdapter", adapter)
	}
	if adapter.Mode() != modeWPEnv {
		t.Fatalf("adapter.Mode() = %q", adapter.Mode())
	}
	// wp-env has no pull/push lifecycle, so no provider hooks may be registered.
	if len(adapter.PostPullHooks()) != 0 {
		t.Fatal("wpEnvAdapter registered post-pull hooks")
	}
}

func TestPushRejectsWPEnvProjects(t *testing.T) {
	t.Parallel()

	err := ensurePushAdapterSupported(wpEnvAdapter{standaloneAdapter{runtime: runtimeContext{Mode: modeWPEnv}}})
	if err == nil {
		t.Fatal("ensurePushAdapterSupported() allowed push from a wp-env tree")
	}
	if !strings.Contains(err.Error(), "wp-env") {
		t.Fatalf("error = %q, want it to name wp-env", err)
	}
	for _, adapter := range []runtimeAdapter{standaloneAdapter{}, ddevAdapter{}} {
		if err := ensurePushAdapterSupported(adapter); err != nil {
			t.Fatalf("ensurePushAdapterSupported() rejected %T: %v", adapter, err)
		}
	}
}

// TestWPEnvMountedWordPressRootHonorsCoreOverride covers a .wp-env.json `core` entry, which
// makes wp-env mount a directory other than <installPath>/WordPress at /var/www/html.
func TestWPEnvMountedWordPressRootHonorsCoreOverride(t *testing.T) {
	t.Parallel()

	install := t.TempDir()
	custom := filepath.Join(t.TempDir(), "wordpress-develop", "build")
	compose := "services:\n  wordpress:\n    volumes:\n      - >-\n        " + custom + ":/var/www/html\n      - 'other:/wordpress-phpunit'\n"
	if err := os.WriteFile(filepath.Join(install, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok := wpEnvMountedWordPressRoot(install)
	if !ok {
		t.Fatal("wpEnvMountedWordPressRoot() did not find the /var/www/html mount")
	}
	if got != custom {
		t.Fatalf("wpEnvMountedWordPressRoot() = %q, want %q", got, custom)
	}

	if _, ok := wpEnvMountedWordPressRoot(t.TempDir()); ok {
		t.Fatal("wpEnvMountedWordPressRoot() succeeded without a compose file")
	}
}

// TestFindIntegrationInConfigStopsAtOwningProject covers the pin not leaking downward from
// an unrelated ancestor config, which would strictly fail every nested project.
func TestFindIntegrationInConfigStopsAtOwningProject(t *testing.T) {
	ancestor := t.TempDir()
	project := filepath.Join(ancestor, "site")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standaloneConfigPath(ancestor), []byte("integration: \"ddev\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standaloneConfigPath(project), []byte("pull_host: \"example.com\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := findIntegrationInConfig(project, ""); got != "" {
		t.Fatalf("findIntegrationInConfig() = %q; an ancestor config must not pin a project that owns its own config", got)
	}
}

// TestResolvedModeSuppressesWPEnvRouting covers a DDEV or standalone project that merely
// carries a .wp-env.json keeping its own WordPress root instead of being redirected.
func TestResolvedModeSuppressesWPEnvRouting(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	primeWPEnvStatus(t, root, runningWPEnvStatus(install))

	if !isWPEnvRoot(root) {
		t.Fatal("isWPEnvRoot() = false for a resolved wp-env project")
	}

	key, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedModeCache.Lock()
	resolvedModeCache.values[key] = modeDDEV
	resolvedModeCache.Unlock()

	if isWPEnvRoot(root) {
		t.Fatal("isWPEnvRoot() = true in DDEV mode; wp-env must not hijack the WordPress root")
	}
	if got := localWPRoot(root, Config{}); got == filepath.Join(install, "WordPress") {
		t.Fatal("localWPRoot() returned the wp-env tree while resolved as DDEV")
	}
	if got := downloadsDir(root); got != filepath.Join(root, ".ddev", ".downloads") {
		t.Fatalf("downloadsDir() = %q, want the DDEV scratch dir", got)
	}
}

// TestDownloadsDirWPEnvModeBeatsLeftoverDDEVConfig covers the documented ambiguous repo:
// with both configs present and the runtime resolved as wp-env, the scratch dir must live
// inside the mounted tree or the container cannot read the staged dump.
func TestDownloadsDirWPEnvModeBeatsLeftoverDDEVConfig(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ddev", "config.yaml"), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := runningWPEnvStatus(install)
	primeWPEnvStatus(t, root, status)

	want := filepath.Join(status.wordPressRoot(), ".wp-ssh", ".downloads")
	if got := downloadsDir(root); got != want {
		t.Fatalf("downloadsDir() = %q, want %q despite leftover .ddev/config.yaml", got, want)
	}
}

// TestFindWPEnvRootStopsAtOwningProject covers an ancestor .wp-env.json not hijacking a
// nested project that owns its own config.
func TestFindWPEnvRootStopsAtOwningProject(t *testing.T) {
	t.Parallel()

	ancestor := t.TempDir()
	if err := os.WriteFile(filepath.Join(ancestor, ".wp-env.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	site := filepath.Join(ancestor, "site")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standaloneConfigPath(site), []byte("pull_host: \"example.com\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := findWPEnvRoot(site); ok {
		t.Fatal("findWPEnvRoot() walked past a project that owns its own .wp-ssh.yaml")
	}

	ddevSite := filepath.Join(ancestor, "ddev-site")
	if err := os.MkdirAll(filepath.Join(ddevSite, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ddevSite, ".ddev", "config.yaml"), []byte("name: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := findWPEnvRoot(ddevSite); ok {
		t.Fatal("findWPEnvRoot() walked past a DDEV project boundary")
	}

	// A wp-env project root owning both marker and config still resolves to itself.
	if err := os.WriteFile(standaloneConfigPath(ancestor), []byte("pull_host: \"example.com\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(ancestor, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	found, ok := findWPEnvRoot(sub)
	if !ok {
		t.Fatal("findWPEnvRoot() rejected a genuine wp-env root that also owns config")
	}
	if resolved, err := filepath.EvalSymlinks(found); err == nil {
		found = resolved
	}
	if want, err := filepath.EvalSymlinks(ancestor); err == nil && found != want {
		t.Fatalf("findWPEnvRoot() = %q, want %q", found, want)
	}
}

// TestComposeListValuesJoinsFoldedScalars pins the js-yaml folding behavior: long strings
// wrap at spaces inside >- block scalars, so a mount path containing a space arrives split
// across physical lines.
func TestComposeListValuesJoinsFoldedScalars(t *testing.T) {
	t.Parallel()

	compose := "services:\n" +
		"  wordpress:\n" +
		"    volumes:\n" +
		"      - >-\n" +
		"        /Users/anna/Client\n" +
		"        Projects/wordpress-develop/build:/var/www/html\n" +
		"      - 'user-home:/home/anna'\n"
	values := composeListValues(compose)
	if len(values) != 2 {
		t.Fatalf("composeListValues() = %#v, want 2 values", values)
	}
	if values[0] != "/Users/anna/Client Projects/wordpress-develop/build:/var/www/html" {
		t.Fatalf("folded scalar not rejoined: %q", values[0])
	}

	install := t.TempDir()
	if err := os.WriteFile(filepath.Join(install, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := wpEnvMountedWordPressRoot(install)
	if !ok || got != "/Users/anna/Client Projects/wordpress-develop/build" {
		t.Fatalf("wpEnvMountedWordPressRoot() = %q, %v", got, ok)
	}
}

// TestWPEnvContainerMountsListsWPContentBinds covers deriving pull excludes from the
// compose file: individual wp-content mounts are listed, the root mount and unrelated
// mounts are not, and duplicates across dev/tests services collapse.
func TestWPEnvContainerMountsListsWPContentBinds(t *testing.T) {
	t.Parallel()

	install := t.TempDir()
	compose := "services:\n" +
		"  wordpress:\n" +
		"    volumes:\n" +
		"      - '/hosts/WordPress:/var/www/html'\n" +
		"      - '/hosts/my-plugin:/var/www/html/wp-content/plugins/my-plugin'\n" +
		"      - '/hosts/mu:/var/www/html/wp-content/mu-plugins'\n" +
		"      - '/hosts/phpunit:/wordpress-phpunit'\n" +
		"  tests-wordpress:\n" +
		"    volumes:\n" +
		"      - '/hosts/tests-WordPress:/var/www/html'\n" +
		"      - '/hosts/my-plugin:/var/www/html/wp-content/plugins/my-plugin'\n"
	if err := os.WriteFile(filepath.Join(install, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	mounts := wpEnvContainerMounts(install)
	if strings.Join(mounts, ",") != "wp-content/plugins/my-plugin,wp-content/mu-plugins" {
		t.Fatalf("wpEnvContainerMounts() = %#v", mounts)
	}
}

// TestBuildRsyncExcludesSkipsWPEnvMounts covers the pull excluding bind-mounted paths so
// rsync --delete never touches a live mountpoint and shadowed files are not downloaded.
func TestBuildRsyncExcludesSkipsWPEnvMounts(t *testing.T) {
	root := t.TempDir()
	install := t.TempDir()
	compose := "services:\n  wordpress:\n    volumes:\n" +
		"      - '" + install + "/WordPress:/var/www/html'\n" +
		"      - '/hosts/my-plugin:/var/www/html/wp-content/plugins/my-plugin'\n"
	if err := os.WriteFile(filepath.Join(install, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	primeWPEnvStatus(t, root, runningWPEnvStatus(install))

	excludes, err := buildRsyncExcludes(root, Config{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(excludes, "\n")
	if !strings.Contains(joined, "wp-content/plugins/my-plugin") {
		t.Fatalf("buildRsyncExcludes() missing the mounted plugin path:\n%s", joined)
	}
}
