package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Citation-Media/wp-ssh-bridge/internal/version"
)

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	cli := newApp(stdin, stdout, stderr)
	if err := cli.run(args); err != nil {
		fmt.Fprintf(stderr, "Error: %s\n", err)
		return 1
	}
	return 0
}

// run dispatches subcommands after handling global help.
func (a *App) run(args []string) error {
	if len(args) == 0 {
		a.printHelp()
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		a.printHelp()
		return nil
	case "version":
		return a.commandVersion(args[1:])
	case "init":
		return a.commandInit(args[1:])
	case "pull":
		return a.commandPull(args[1:])
	case "push":
		return a.commandPush(args[1:])
	case "provider":
		return a.commandProvider(args[1:])
	case "plugins":
		return a.commandPlugins(args[1:])
	case "domains":
		return a.commandDomains(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// printHelp shows the user-facing command surface.
func (a *App) printHelp() {
	fmt.Fprint(a.Stdout, `wp-ssh-bridge syncs WordPress databases and files through SSH-only WordPress hosts.

Usage:
  wp-ssh-bridge init [flags]                  Configure this project
  wp-ssh-bridge pull [flags]                  Pull database and files from the source host
  wp-ssh-bridge push [flags]                  Push database and files to the target host
  wp-ssh-bridge provider install [flags]      Regenerate DDEV provider files
  wp-ssh-bridge provider generate [flags]     Print generated DDEV YAML
  wp-ssh-bridge domains add --old A --new B   Add pull/push domain mappings
  wp-ssh-bridge plugins remove [wordpress-root]
  wp-ssh-bridge version [--short]

Common flags:
  --host string              Pull source SSH host; push alias for --push-host
  --port string              Pull source SSH port; push alias for --push-port
  --user string              Pull source SSH user; push alias for --push-user
  --config-file string       YAML config file path
  --remote-path string       Pull source WordPress root; push alias for --push-remote-path
  --push-host string         Push target SSH host
  --push-remote-path string  Push target WordPress root
  --local-wp-path string     Local WordPress root relative to the DDEV project
  --clone-images             Include wp-content/uploads
  --skip-db                  Pull/push files only
  --skip-files               Pull/push database only
  --skip-import              Pull only; download the database without importing it
  --skip-maintenance-mode    Skip enabling WordPress maintenance mode during write operations
  --silent                   Do not prompt; use saved config, environment, and flags

Run "wp-ssh-bridge init" to configure DDEV provider mode or standalone mode.
`)
}

// commandVersion prints build metadata injected by release builds.
func (a *App) commandVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	short := fs.Bool("short", false, "print only the semantic version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *short {
		fmt.Fprintln(a.Stdout, version.Version)
		return nil
	}
	fmt.Fprintln(a.Stdout, version.String())
	return nil
}

// commandInit writes project config, provider YAML, hook YAML, and plugin list.
func (a *App) commandInit(args []string) error {
	opts, err := parseConfigCommand("init", args, a.Stderr)
	if err != nil {
		return err
	}
	if err := opts.rejectOperationFlags("init"); err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	adapter := adapterForRuntime(runtime)

	if runtime.Mode == modeDDEV && runtime.DDEV.Type != "" && runtime.DDEV.Type != "wordpress" {
		return fmt.Errorf("wp-ssh-bridge only supports DDEV WordPress projects; detected %q", runtime.DDEV.Type)
	}
	if runtime.Mode == modeDDEV && runtime.DDEV.Type == "" {
		if projectType := ddevProjectType(runtime.Root); projectType != "" && projectType != "wordpress" {
			return fmt.Errorf("wp-ssh-bridge only supports DDEV WordPress projects; detected %q", projectType)
		}
	}

	cfg, err := loadConfigForRuntime(runtime, opts.ConfigFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)
	cfg = opts.apply(cfg)

	if !opts.Silent {
		prompter := newPrompter(a.Stdin, a.Stdout)
		if err := prompter.fillConfig(&cfg); err != nil {
			return err
		}
	}
	if err := cfg.validateInitValues(); err != nil {
		return err
	}

	if err := writeConfigForRuntime(runtime, opts.ConfigFile, cfg); err != nil {
		return err
	}
	if err := adapter.InstallProjectFiles(cfg, opts.Binary); err != nil {
		return err
	}

	a.UI.Success("Configured %s pull source for %s:%s", cfg.Provider, sshTarget(cfg.pullTarget()), trimTrailingSlash(cfg.RemotePath))
	a.UI.Success("Project files updated")
	return nil
}

// commandPull runs the direct pull pipeline using saved config and optional one-shot overrides.
func (a *App) commandPull(args []string) error {
	opts, err := parseConfigCommand("pull", args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	adapter := adapterForRuntime(runtime)
	cfg, err := loadConfigForRuntime(runtime, opts.ConfigFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)
	cfg = opts.apply(cfg)
	if adapter.Mode() == modeStandalone && !opts.Silent {
		prompter := newPrompter(a.Stdin, a.Stdout)
		if err := prompter.fillPullConfig(&cfg); err != nil {
			return err
		}
	}
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}
	if err := adapter.PreparePull(a, cfg, opts); err != nil {
		return err
	}

	return a.runPullPipeline(context.Background(), adapter, cfg, opts)
}

// commandPush runs the direct push pipeline using saved push target config and one-shot overrides.
func (a *App) commandPush(args []string) error {
	opts, err := parseConfigCommand("push", args, a.Stderr)
	if err != nil {
		return err
	}
	if opts.SkipImport {
		return errors.New("--skip-import only applies to pull")
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	adapter := adapterForRuntime(runtime)
	cfg, err := loadConfigForRuntime(runtime, opts.ConfigFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)
	cfg = opts.apply(cfg)
	cfg = opts.applyGenericAsPush(cfg)
	if adapter.Mode() == modeStandalone && !opts.Silent {
		prompter := newPrompter(a.Stdin, a.Stdout)
		if err := prompter.fillPushConfig(&cfg); err != nil {
			return err
		}
	}
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}
	if err := adapter.PreparePush(a, cfg, opts); err != nil {
		return err
	}

	return a.runPushPipeline(context.Background(), adapter, cfg, opts)
}

// commandProvider handles both generated-file commands and DDEV runtime callbacks.
func (a *App) commandProvider(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: wp-ssh-bridge provider {install|generate|info|auth|db-pull|files-pull|files-import|post-pull|db-push|files-push|post-push|sanitize-config|remove-blocked-plugins}")
	}

	switch args[0] {
	case "install":
		return a.commandProviderInstall(args[1:])
	case "generate":
		return a.commandProviderGenerate(args[1:])
	case "info", "auth", "db-pull", "files-pull", "files-import", "post-pull", "db-push", "files-push", "post-push":
		return a.commandProviderRuntime(args[0], args[1:])
	case "sanitize-config", "remove-blocked-plugins":
		// Deprecated: kept only for older generated provider files and due to be removed in a future release.
		return a.commandProviderRuntime(args[0], args[1:])
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
}

// commandProviderInstall refreshes the generated DDEV provider and hook files.
func (a *App) commandProviderInstall(args []string) error {
	opts, err := parseProviderInstallCommand(args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	adapter := adapterForRuntime(runtime)
	if runtime.Mode != modeDDEV {
		return errors.New("provider install requires DDEV mode; `ddev describe -j` did not succeed")
	}
	cfg, err := loadConfigForRuntime(runtime, opts.ConfigFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)
	if opts.Provider != "" {
		cfg.Provider = opts.Provider
	}
	if err := adapter.InstallProjectFiles(cfg, opts.Binary); err != nil {
		return err
	}
	a.UI.Success("Provider files installed")
	return nil
}

// commandProviderGenerate prints the generated provider or hook YAML.
func (a *App) commandProviderGenerate(args []string) error {
	fs := flag.NewFlagSet("provider generate", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	provider := fs.String("provider", defaultProviderName, "provider name")
	binary := fs.String("binary", binaryName, "binary path used by generated YAML")
	kind := fs.String("kind", "provider", "provider, hook, or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateProviderName(*provider); err != nil {
		return err
	}

	switch *kind {
	case "provider":
		fmt.Fprint(a.Stdout, providerYAML(*provider, *binary))
	case "hook":
		fmt.Fprint(a.Stdout, hookYAML(*binary))
	case "all":
		fmt.Fprint(a.Stdout, providerYAML(*provider, *binary))
		fmt.Fprintln(a.Stdout, "---")
		fmt.Fprint(a.Stdout, hookYAML(*binary))
	default:
		return fmt.Errorf("unknown --kind %q", *kind)
	}
	return nil
}

// commandProviderRuntime executes callbacks invoked by DDEV provider YAML.
func (a *App) commandProviderRuntime(name string, args []string) error {
	opts, err := parseRuntimeCommand(name, args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	adapter := adapterForRuntime(runtime)
	if runtime.Mode != modeDDEV {
		return errors.New("provider runtime commands require DDEV mode; `ddev describe -j` did not succeed")
	}
	cfg, err := loadConfigForRuntime(runtime, opts.ConfigFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)

	ctx := context.Background()
	switch name {
	case "info":
		return a.providerInfo(cfg)
	case "auth":
		return a.providerAuth(ctx, runtime.Root, cfg)
	case "db-pull":
		if err := a.preflightPull(ctx, adapter, cfg, configOptions{SkipFiles: true}); err != nil {
			return err
		}
		return a.dbPull(ctx, runtime.Root, cfg)
	case "files-pull":
		if err := a.preflightPull(ctx, adapter, cfg, configOptions{SkipDB: true, SkipImport: true}); err != nil {
			return err
		}
		return a.filesPull(ctx, runtime.Root, cfg, false)
	case "files-import":
		a.filesImport()
		return nil
	case "post-pull":
		return a.postPull(ctx, adapter, cfg)
	case "db-push":
		if err := a.preflightPush(ctx, runtime.Root, adapter.Mode(), cfg, configOptions{SkipFiles: true}); err != nil {
			return err
		}
		return a.dbPush(ctx, runtime.Root, cfg)
	case "files-push":
		if err := a.preflightPush(ctx, runtime.Root, adapter.Mode(), cfg, configOptions{SkipDB: true}); err != nil {
			return err
		}
		return a.filesPush(ctx, runtime.Root, cfg)
	case "post-push":
		return a.postPush(ctx, runtime.Root, cfg)
	case "sanitize-config":
		// Deprecated: use post-pull instead; this direct callback is due to be removed in a future release.
		return a.sanitizeWPConfig(runtime.Root, cfg)
	case "remove-blocked-plugins":
		// Deprecated: use post-pull or `plugins remove`; this callback is due to be removed in a future release.
		return a.removeBlockedPlugins(ctx, runtime.Root, cfg, opts.WordPressRoot)
	default:
		return fmt.Errorf("unknown provider runtime command %q", name)
	}
}

// commandPlugins exposes plugin cleanup without DDEV custom command shell files.
func (a *App) commandPlugins(args []string) error {
	if len(args) == 0 || args[0] != "remove" {
		return errors.New("usage: wp-ssh-bridge plugins remove [wordpress-root]")
	}
	opts, err := parseRuntimeCommand("plugins remove", args[1:], a.Stderr)
	if err != nil {
		return err
	}

	projectRoot, err := a.resolveProjectRoot(opts.ProjectRoot)
	if err != nil {
		return err
	}
	cfg, err := loadConfigFromRoot(projectRoot, opts.ConfigFile)
	if err != nil {
		return err
	}
	return a.removeBlockedPlugins(context.Background(), projectRoot, cfg, opts.WordPressRoot)
}

// commandDomains updates configured pull/push domain mappings after initial setup.
func (a *App) commandDomains(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: wp-ssh-bridge domains {add|list}")
	}
	switch args[0] {
	case "add", "configure":
		return a.commandDomainsAdd(args[1:])
	case "list":
		return a.commandDomainsList(args[1:])
	default:
		return fmt.Errorf("unknown domains command %q", args[0])
	}
}

func (a *App) commandDomainsAdd(args []string) error {
	fs := flag.NewFlagSet("domains add", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	projectRoot := fs.String("project-root", "", "DDEV project root")
	configFile := fs.String("config-file", "", "YAML config file path")
	oldValue := fs.String("old", "", "source domain or URL")
	newValue := fs.String("new", "", "target domain or URL")
	direction := fs.String("direction", "both", "pull, push, or both")
	binary := fs.String("binary", defaultBinaryPath(), "binary path used by generated provider files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	replacement := DomainReplacement{Old: strings.TrimSpace(*oldValue), New: strings.TrimSpace(*newValue)}
	if err := validateDomainReplacement(replacement); err != nil {
		return err
	}

	runtime := a.resolveRuntime(*projectRoot)
	adapter := adapterForRuntime(runtime)
	cfg, err := loadConfigForRuntime(runtime, *configFile)
	if err != nil {
		return err
	}
	adapter.ApplyConfigDefaults(&cfg)

	switch *direction {
	case "pull":
		cfg.PullDomainReplacements = addDomainReplacement(cfg.PullDomainReplacements, replacement)
	case "push":
		cfg.PushDomainReplacements = addDomainReplacement(cfg.PushDomainReplacements, replacement)
	case "both":
		cfg.PullDomainReplacements = addDomainReplacement(cfg.PullDomainReplacements, replacement)
		cfg.PushDomainReplacements = addDomainReplacement(cfg.PushDomainReplacements, invertDomainReplacement(replacement))
	default:
		return fmt.Errorf("--direction must be pull, push, or both")
	}

	if err := writeConfigForRuntime(runtime, *configFile, cfg); err != nil {
		return err
	}
	if runtime.Mode == modeDDEV {
		if err := adapter.InstallProjectFiles(cfg, *binary); err != nil {
			return err
		}
	}
	a.UI.Success("Domain replacement configured")
	return nil
}

func (a *App) commandDomainsList(args []string) error {
	fs := flag.NewFlagSet("domains list", flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	projectRoot := fs.String("project-root", "", "DDEV project root")
	configFile := fs.String("config-file", "", "YAML config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	runtime := a.resolveRuntime(*projectRoot)
	cfg, err := loadConfigForRuntime(runtime, *configFile)
	if err != nil {
		return err
	}
	printDomainReplacements(a.Stdout, "pull_domain_replacements", cfg.PullDomainReplacements)
	printDomainReplacements(a.Stdout, "push_domain_replacements", cfg.PushDomainReplacements)
	return nil
}

func validateDomainReplacement(replacement DomainReplacement) error {
	if replacement.Old == "" || replacement.New == "" {
		return errors.New("--old and --new are required")
	}
	if strings.ContainsAny(replacement.Old, "\r\n") || strings.ContainsAny(replacement.New, "\r\n") {
		return errors.New("domain replacement values must not contain newlines")
	}
	return nil
}

func addDomainReplacement(replacements []DomainReplacement, replacement DomainReplacement) []DomainReplacement {
	for _, existing := range replacements {
		if existing == replacement {
			return replacements
		}
	}
	return append(replacements, replacement)
}

func invertDomainReplacement(replacement DomainReplacement) DomainReplacement {
	return DomainReplacement{Old: replacement.New, New: replacement.Old}
}

func printDomainReplacements(writer io.Writer, label string, replacements []DomainReplacement) {
	fmt.Fprintf(writer, "%s:\n", label)
	if len(replacements) == 0 {
		fmt.Fprintln(writer, "  (none)")
		return
	}
	for _, replacement := range replacements {
		fmt.Fprintf(writer, "  %s -> %s\n", replacement.Old, replacement.New)
	}
}

// resolveProjectRoot handles explicit roots and DDEV upward discovery.
func (a *App) resolveProjectRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	return findProjectRoot(a.WorkDir)
}

// resolveRuntime chooses DDEV provider mode only when `ddev describe -j` succeeds.
func (a *App) resolveRuntime(explicit string) runtimeContext {
	start := a.WorkDir
	if explicit != "" {
		start = explicit
	}
	return detectRuntime(start)
}

// loadConfigForRuntime reads the correct config source for DDEV or standalone mode.
func loadConfigForRuntime(runtime runtimeContext, explicitPath string) (Config, error) {
	return loadConfigPath(configPathForRuntime(runtime, explicitPath))
}

// writeConfigForRuntime persists config without creating DDEV files in standalone mode.
func writeConfigForRuntime(runtime runtimeContext, explicitPath string, cfg Config) error {
	defaults := defaultConfigForRuntime(runtime)
	if runtime.Mode == modeDDEV {
		cfg = normalizeDDEVConfigPaths(runtime.Root, cfg)
		defaults = normalizeDDEVConfigPaths(runtime.Root, defaults)
	}
	return writeConfigFile(configPathForRuntime(runtime, explicitPath), cfg, defaults)
}

func loadConfigFromRoot(root string, explicitPath string) (Config, error) {
	runtime := detectRuntime(root)
	return loadConfigForRuntime(runtime, explicitPath)
}

func defaultConfigForRuntime(runtime runtimeContext) Config {
	defaults := defaultConfig()
	if runtime.Mode == modeDDEV {
		applyDDEVDefaults(runtime.Root, &defaults)
	}
	return defaults
}

func configPathForRuntime(runtime runtimeContext, explicitPath string) string {
	path := firstNonEmpty(explicitPath, os.Getenv("WP_SSH_CONFIG_FILE"))
	if path != "" {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(runtime.Root, path)
	}
	if runtime.Mode == modeDDEV {
		return projectConfigPath(runtime.Root)
	}
	return standaloneConfigPath(runtime.Root)
}

func normalizeDDEVConfigPaths(projectRoot string, cfg Config) Config {
	cfg.LocalWPPath = projectRelativeConfigPath(projectRoot, cfg.LocalWPPath)
	cfg.PluginRemoveFile = projectRelativeConfigPath(projectRoot, cfg.PluginRemoveFile)
	return cfg
}

func projectRelativeConfigPath(projectRoot string, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	if rel, ok := projectRelativePath(projectRoot, path); ok {
		return rel
	}
	return path
}

// confirmDirectOperation replaces DDEV's parent confirmation when the wrapper runs directly.
func (a *App) confirmDirectOperation(action string, target RemoteTarget, opts configOptions) error {
	if opts.Silent || opts.Yes {
		return nil
	}
	fmt.Fprintf(a.Stdout, "%s: %s:%s\n", operationTargetLabel(action), sshTarget(target), trimTrailingSlash(target.RemotePath))
	confirmed, err := newPrompter(a.Stdin, a.Stdout).promptBool("Continue", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New(action + " cancelled")
	}
	return nil
}

func operationTargetLabel(action string) string {
	if action == "push" {
		return "Push target"
	}
	return "Pull source"
}

// runPullPipeline executes the host-side pull pipeline without DDEV lifecycle headings.
func (a *App) runPullPipeline(ctx context.Context, adapter runtimeAdapter, cfg Config, opts configOptions) error {
	if opts.SkipDB && opts.SkipFiles {
		return nil
	}
	root := adapter.Root()

	if err := a.preflightPull(ctx, adapter, cfg, opts); err != nil {
		return err
	}
	if !opts.SkipDB {
		if err := a.dbPull(ctx, root, cfg); err != nil {
			return err
		}
	}
	if !opts.SkipFiles {
		if err := a.filesPull(ctx, root, cfg, adapter.Mode() == modeStandalone); err != nil {
			return err
		}
	}

	shouldImportDB := !opts.SkipDB && !opts.SkipImport
	if shouldImportDB && !opts.SkipMaintenanceMode {
		if err := a.enableLocalMaintenanceMode(ctx, root, cfg); err != nil {
			return err
		}
		defer a.disableLocalMaintenanceMode(context.Background(), root, cfg)
	}
	if shouldImportDB {
		if err := a.importLocalDB(ctx, root, cfg); err != nil {
			return err
		}
	}
	if !shouldImportDB {
		cfg.SkipSearchReplace = true
	}
	return a.postPull(ctx, adapter, cfg)
}

// runPushPipeline executes the host-side push pipeline without DDEV lifecycle headings.
func (a *App) runPushPipeline(ctx context.Context, adapter runtimeAdapter, cfg Config, opts configOptions) error {
	if opts.SkipDB && opts.SkipFiles {
		return nil
	}
	root := adapter.Root()

	if err := a.preflightPush(ctx, root, adapter.Mode(), cfg, opts); err != nil {
		return err
	}

	if !opts.SkipMaintenanceMode {
		target := cfg.pushTarget()
		if err := a.enableRemoteMaintenanceMode(ctx, root, target); err != nil {
			return err
		}
		defer a.disableRemoteMaintenanceMode(context.Background(), root, target)
	}

	if !opts.SkipDB {
		if err := a.dbPush(ctx, root, cfg); err != nil {
			return err
		}
	}
	if !opts.SkipFiles {
		if err := a.filesPush(ctx, root, cfg); err != nil {
			return err
		}
	}
	if opts.SkipDB {
		return nil
	}
	return a.postPush(ctx, root, cfg)
}

// configOptions tracks flags shared by init, pull, and push.
type configOptions struct {
	ProjectRoot         string
	ConfigFile          string
	Binary              string
	Silent              bool
	Yes                 bool
	SkipDB              bool
	SkipFiles           bool
	SkipImport          bool
	SkipMaintenanceMode bool
	Provider            string
	User                string
	Host                string
	Port                string
	RemotePath          string
	RemoteTmpDir        string
	PushUser            string
	PushHost            string
	PushPort            string
	PushRemotePath      string
	PushRemoteTmpDir    string
	PushURL             string
	LocalWPPath         string
	CloneImages         bool
	PluginRemoveFile    string
	LocalURL            string
	SkipSearchReplace   bool
}

// parseConfigCommand parses flags shared by user-facing setup and pull commands.
func parseConfigCommand(name string, args []string, stderr io.Writer) (configOptions, error) {
	opts := configOptions{Binary: defaultBinaryPath()}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.ProjectRoot, "project-root", "", "DDEV project root")
	fs.StringVar(&opts.ConfigFile, "config-file", "", "YAML config file path")
	fs.StringVar(&opts.Binary, "binary", opts.Binary, "binary path used by generated provider files")
	fs.BoolVar(&opts.Silent, "silent", false, "do not prompt; use saved config, environment, and flags")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm without prompting")
	fs.BoolVar(&opts.Yes, "y", false, "confirm without prompting")
	fs.BoolVar(&opts.SkipDB, "skip-db", false, "pull/push files only")
	fs.BoolVar(&opts.SkipFiles, "skip-files", false, "pull/push database only")
	fs.BoolVar(&opts.SkipImport, "skip-import", false, "pull only; download the database without importing it")
	fs.BoolVar(&opts.SkipMaintenanceMode, "skip-maintenance-mode", false, "skip enabling WordPress maintenance mode during write operations")
	fs.StringVar(&opts.Provider, "provider", "", "DDEV provider name")
	fs.StringVar(&opts.User, "user", "", "pull source SSH user; push alias for --push-user")
	fs.StringVar(&opts.Host, "host", "", "pull source SSH host; push alias for --push-host")
	fs.StringVar(&opts.Port, "port", "", "pull source SSH port; push alias for --push-port")
	fs.StringVar(&opts.RemotePath, "remote-path", "", "pull source WordPress root; push alias for --push-remote-path")
	fs.StringVar(&opts.RemoteTmpDir, "remote-tmp-dir", "", "pull source temporary directory; push alias for --push-remote-tmp-dir")
	fs.StringVar(&opts.PushUser, "push-user", "", "push target SSH user")
	fs.StringVar(&opts.PushHost, "push-host", "", "push target SSH host")
	fs.StringVar(&opts.PushPort, "push-port", "", "push target SSH port")
	fs.StringVar(&opts.PushRemotePath, "push-remote-path", "", "push target WordPress root")
	fs.StringVar(&opts.PushRemoteTmpDir, "push-remote-tmp-dir", "", "push target temporary directory")
	fs.StringVar(&opts.PushURL, "push-url", "", "push target public WordPress URL")
	fs.StringVar(&opts.LocalWPPath, "local-wp-path", "", "local WordPress root relative to project")
	fs.BoolVar(&opts.CloneImages, "clone-images", false, "include wp-content/uploads")
	fs.StringVar(&opts.PluginRemoveFile, "plugin-remove-file", "", "plugin block list path")
	fs.StringVar(&opts.LocalURL, "local-url", "", "local URL for search-replace")
	fs.BoolVar(&opts.SkipSearchReplace, "skip-search-replace", false, "skip URL search-replace")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	return opts, nil
}

// rejectOperationFlags catches pull/push runtime flags on commands that only configure files.
func (opts configOptions) rejectOperationFlags(command string) error {
	switch {
	case opts.SkipDB:
		return fmt.Errorf("--skip-db only applies to pull or push, not %s", command)
	case opts.SkipFiles:
		return fmt.Errorf("--skip-files only applies to pull or push, not %s", command)
	case opts.SkipImport:
		return fmt.Errorf("--skip-import only applies to pull, not %s", command)
	default:
		return nil
	}
}

// providerInstallOptions is intentionally narrow because provider install only writes generated files.
type providerInstallOptions struct {
	ProjectRoot string
	ConfigFile  string
	Binary      string
	Provider    string
}

// parseProviderInstallCommand accepts only the flags used while regenerating DDEV YAML.
func parseProviderInstallCommand(args []string, stderr io.Writer) (providerInstallOptions, error) {
	opts := providerInstallOptions{Binary: defaultBinaryPath()}
	fs := flag.NewFlagSet("provider install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.ProjectRoot, "project-root", "", "DDEV project root")
	fs.StringVar(&opts.ConfigFile, "config-file", "", "YAML config file path")
	fs.StringVar(&opts.Binary, "binary", opts.Binary, "binary path used by generated provider files")
	fs.StringVar(&opts.Provider, "provider", "", "DDEV provider name")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, errors.New("provider install does not accept positional arguments")
	}
	return opts, nil
}

// apply overlays command-line flags onto config values.
func (opts configOptions) apply(cfg Config) Config {
	if opts.Provider != "" {
		cfg.Provider = opts.Provider
	}
	if opts.User != "" {
		cfg.User = opts.User
	}
	if opts.Host != "" {
		cfg.Host = opts.Host
	}
	if opts.Port != "" {
		cfg.Port = opts.Port
	}
	if opts.RemotePath != "" {
		cfg.RemotePath = opts.RemotePath
	}
	if opts.RemoteTmpDir != "" {
		cfg.RemoteTmpDir = opts.RemoteTmpDir
	}
	if opts.PushUser != "" {
		cfg.PushUser = opts.PushUser
	}
	if opts.PushHost != "" {
		cfg.PushHost = opts.PushHost
	}
	if opts.PushPort != "" {
		cfg.PushPort = opts.PushPort
	}
	if opts.PushRemotePath != "" {
		cfg.PushRemotePath = opts.PushRemotePath
	}
	if opts.PushRemoteTmpDir != "" {
		cfg.PushRemoteTmpDir = opts.PushRemoteTmpDir
	}
	if opts.PushURL != "" {
		cfg.PushURL = opts.PushURL
	}
	if opts.LocalWPPath != "" {
		cfg.LocalWPPath = opts.LocalWPPath
	}
	if opts.PluginRemoveFile != "" {
		cfg.PluginRemoveFile = opts.PluginRemoveFile
	}
	if opts.LocalURL != "" {
		cfg.LocalURL = opts.LocalURL
	}
	if opts.CloneImages {
		cfg.CloneImages = true
	}
	if opts.SkipSearchReplace {
		cfg.SkipSearchReplace = true
	}
	return cfg
}

// applyGenericAsPush lets push commands use --user/--host/--remote-path as concise aliases.
func (opts configOptions) applyGenericAsPush(cfg Config) Config {
	if opts.User != "" {
		cfg.PushUser = opts.User
	}
	if opts.Host != "" {
		cfg.PushHost = opts.Host
	}
	if opts.Port != "" {
		cfg.PushPort = opts.Port
	}
	if opts.RemotePath != "" {
		cfg.PushRemotePath = opts.RemotePath
	}
	if opts.RemoteTmpDir != "" {
		cfg.PushRemoteTmpDir = opts.RemoteTmpDir
	}
	return cfg
}

// runtimeOptions tracks provider callback flags.
type runtimeOptions struct {
	ProjectRoot   string
	ConfigFile    string
	WordPressRoot string
}

// parseRuntimeCommand accepts a project root and optional WordPress root argument.
func parseRuntimeCommand(name string, args []string, stderr io.Writer) (runtimeOptions, error) {
	opts := runtimeOptions{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.ProjectRoot, "project-root", "", "DDEV project root")
	fs.StringVar(&opts.ConfigFile, "config-file", "", "YAML config file path")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 1 {
		return opts, fmt.Errorf("too many arguments for %s", name)
	}
	if fs.NArg() == 1 {
		opts.WordPressRoot = fs.Arg(0)
	}
	return opts, nil
}

// prompter handles interactive init input.
type prompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func newPrompter(in io.Reader, out io.Writer) prompter {
	return prompter{reader: bufio.NewReader(in), out: out}
}

// fillConfig asks only for values not already supplied by config, env, or flags.
func (p prompter) fillConfig(cfg *Config) error {
	var err error
	if err := p.fillPullConfigFields(cfg, false); err != nil {
		return err
	}
	configurePush, err := p.promptBool("Configure a push target", cfg.PushHost != "" || cfg.PushRemotePath != "")
	if err != nil {
		return err
	}
	if configurePush {
		return p.fillPushConfigFields(cfg, false)
	}
	return nil
}

// fillPullConfig collects source settings needed by pull and init.
func (p prompter) fillPullConfig(cfg *Config) error {
	return p.fillPullConfigFields(cfg, true)
}

func (p prompter) fillPullConfigFields(cfg *Config, requireTarget bool) error {
	var err error
	cfg.Provider, err = p.promptString("Provider name", defaultString(cfg.Provider, defaultProviderName), false)
	if err != nil {
		return err
	}
	cfg.User, err = p.promptTargetString("SSH user", cfg.User, requireTarget)
	if err != nil {
		return err
	}
	cfg.Host, err = p.promptTargetString("SSH host", cfg.Host, requireTarget)
	if err != nil {
		return err
	}
	cfg.Port, err = p.promptString("SSH port", defaultString(cfg.Port, "22"), false)
	if err != nil {
		return err
	}
	cfg.RemotePath, err = p.promptTargetString("Remote WordPress absolute path", cfg.RemotePath, requireTarget)
	if err != nil {
		return err
	}
	cfg.RemoteTmpDir, err = p.promptString("Remote temp dir", defaultString(cfg.RemoteTmpDir, "/tmp"), false)
	if err != nil {
		return err
	}
	cfg.LocalWPPath, err = p.promptString("Local WordPress path", cfg.LocalWPPath, false)
	if err != nil {
		return err
	}
	cfg.CloneImages, err = p.promptBool("Clone uploads/media", cfg.CloneImages)
	if err != nil {
		return err
	}
	cfg.SkipSearchReplace, err = p.promptBool("Skip URL search-replace", cfg.SkipSearchReplace)
	return err
}

// fillPushConfig collects target settings needed by push and init.
func (p prompter) fillPushConfig(cfg *Config) error {
	return p.fillPushConfigFields(cfg, true)
}

func (p prompter) fillPushConfigFields(cfg *Config, requireTarget bool) error {
	var err error
	cfg.Provider, err = p.promptString("Provider name", defaultString(cfg.Provider, defaultProviderName), false)
	if err != nil {
		return err
	}
	cfg.PushUser, err = p.promptTargetString("Push SSH user", cfg.PushUser, requireTarget)
	if err != nil {
		return err
	}
	cfg.PushHost, err = p.promptTargetString("Push SSH host", cfg.PushHost, requireTarget)
	if err != nil {
		return err
	}
	cfg.PushPort, err = p.promptString("Push SSH port", defaultString(cfg.PushPort, "22"), false)
	if err != nil {
		return err
	}
	cfg.PushRemotePath, err = p.promptTargetString("Push WordPress absolute path", cfg.PushRemotePath, requireTarget)
	if err != nil {
		return err
	}
	cfg.PushRemoteTmpDir, err = p.promptString("Push temp dir", defaultString(cfg.PushRemoteTmpDir, "/tmp"), false)
	if err != nil {
		return err
	}
	cfg.PushURL, err = p.promptString("Push target URL", cfg.PushURL, false)
	if err != nil {
		return err
	}
	cfg.LocalWPPath, err = p.promptString("Local WordPress path", cfg.LocalWPPath, false)
	if err != nil {
		return err
	}
	cfg.SkipSearchReplace, err = p.promptBool("Skip URL search-replace", cfg.SkipSearchReplace)
	return err
}

func (p prompter) promptTargetString(label string, current string, required bool) (string, error) {
	if required {
		return p.promptRequiredString(label, current)
	}
	return p.promptString(label, current, false)
}

// promptRequiredString accepts existing config, env, or flag values without another prompt.
func (p prompter) promptRequiredString(label string, current string) (string, error) {
	if current != "" {
		return current, nil
	}
	return p.promptString(label, current, true)
}

// promptString returns a trimmed answer or the provided default.
func (p prompter) promptString(label string, current string, required bool) (string, error) {
	for {
		if current != "" {
			fmt.Fprintf(p.out, "%s [%s]: ", label, current)
		} else {
			fmt.Fprintf(p.out, "%s: ", label)
		}
		line, err := p.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		answer := strings.TrimSpace(line)
		if answer == "" {
			answer = current
		}
		if answer != "" || !required {
			return answer, nil
		}
		fmt.Fprintln(p.out, "This value is required.")
	}
}

// promptBool returns a yes/no value with the current value as default.
func (p prompter) promptBool(label string, current bool) (bool, error) {
	defaultLabel := "n"
	if current {
		defaultLabel = "y"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, defaultLabel)
		line, err := p.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "" {
			return current, nil
		}
		switch answer {
		case "y", "yes", "true", "1":
			return true, nil
		case "n", "no", "false", "0":
			return false, nil
		default:
			fmt.Fprintln(p.out, "Enter yes or no.")
		}
	}
}

// ensureExecutablePath gives clearer failures when DDEV cannot find the host binary.
func ensureExecutablePath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is not on PATH", name)
	}
	return nil
}

// defaultBinaryPath makes project-local binaries work without requiring a global install.
func defaultBinaryPath() string {
	path, err := os.Executable()
	if err != nil || path == "" {
		return binaryName
	}
	return path
}
