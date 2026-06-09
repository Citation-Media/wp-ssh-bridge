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

	"github.com/Citation-Media/ddev-wp-ssh/internal/version"
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
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// printHelp shows the user-facing command surface.
func (a *App) printHelp() {
	fmt.Fprint(a.Stdout, `ddev-wp-ssh pulls and pushes WordPress databases and files through SSH-only upstreams.

Usage:
  ddev-wp-ssh init [flags]
  ddev-wp-ssh pull [flags]
  ddev-wp-ssh push [flags]
  ddev-wp-ssh provider install [flags]
  ddev-wp-ssh provider generate [flags]
  ddev-wp-ssh plugins remove [wordpress-root]
  ddev-wp-ssh version

Common flags:
  --host string              Upstream SSH host
  --port string              Upstream SSH port
  --user string              Upstream SSH user
  --remote-path string       Upstream WordPress root
  --push-host string         Push target SSH host
  --push-remote-path string  Push target WordPress root
  --local-wp-path string     Local WordPress root relative to the DDEV project
  --clone-images             Include wp-content/uploads
  --silent                   Do not prompt; use saved config, env, and flags

Run "ddev-wp-ssh init" to configure DDEV provider mode or standalone mode.
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

	runtime := a.resolveRuntime(opts.ProjectRoot)

	if runtime.Mode == modeDDEV && runtime.DDEV.Type != "" && runtime.DDEV.Type != "wordpress" {
		return fmt.Errorf("ddev-wp-ssh only supports DDEV WordPress projects; detected %q", runtime.DDEV.Type)
	}
	if runtime.Mode == modeDDEV && runtime.DDEV.Type == "" {
		if projectType := ddevProjectType(runtime.Root); projectType != "" && projectType != "wordpress" {
			return fmt.Errorf("ddev-wp-ssh only supports DDEV WordPress projects; detected %q", projectType)
		}
	}

	cfg, err := loadConfigForRuntime(runtime)
	if err != nil {
		return err
	}
	if runtime.Mode == modeDDEV {
		applyDDEVDefaults(runtime.Root, &cfg)
	}
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

	if err := writeConfigForRuntime(runtime, cfg); err != nil {
		return err
	}
	if runtime.Mode == modeDDEV {
		if err := installProviderFiles(runtime.Root, cfg, opts.Binary); err != nil {
			return err
		}
	} else {
		if err := installStandaloneFiles(runtime.Root, cfg); err != nil {
			return err
		}
	}

	fmt.Fprintf(a.Stdout, "Configured %s pull source for %s:%s\n", cfg.Provider, sshTarget(cfg.pullTarget()), trimTrailingSlash(cfg.RemotePath))
	fmt.Fprintln(a.Stdout, "Managed files:")
	if runtime.Mode == modeDDEV {
		fmt.Fprintln(a.Stdout, generatedFilesReport(runtime.Root, cfg))
	} else {
		fmt.Fprintln(a.Stdout, standaloneFilesReport(runtime.Root, cfg))
	}
	return nil
}

// commandPull runs ddev pull using saved config and optional one-shot overrides.
func (a *App) commandPull(args []string) error {
	opts, err := parseConfigCommand("pull", args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	cfg, err := loadConfigForRuntime(runtime)
	if err != nil {
		return err
	}
	if runtime.Mode == modeDDEV {
		applyDDEVDefaults(runtime.Root, &cfg)
	}
	cfg = opts.apply(cfg)
	if runtime.Mode == modeStandalone && !opts.Silent {
		prompter := newPrompter(a.Stdin, a.Stdout)
		if err := prompter.fillPullConfig(&cfg); err != nil {
			return err
		}
	}
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}
	if runtime.Mode == modeStandalone {
		if err := writeConfigForRuntime(runtime, cfg); err != nil {
			return err
		}
		return a.standalonePull(context.Background(), runtime.Root, cfg)
	}

	if err := installProviderFiles(runtime.Root, cfg, opts.Binary); err != nil {
		return err
	}
	ddevArgs := []string{"pull", defaultString(cfg.Provider, defaultProviderName), "--environment=" + cfg.envArgs()}
	if opts.Silent || opts.Yes {
		ddevArgs = append(ddevArgs, "-y")
	}
	if opts.SkipDB {
		ddevArgs = append(ddevArgs, "--skip-db")
	}
	if opts.SkipFiles {
		ddevArgs = append(ddevArgs, "--skip-files")
	}
	if opts.SkipImport {
		ddevArgs = append(ddevArgs, "--skip-import")
	}

	return a.runExternal(context.Background(), runtime.Root, "ddev", ddevArgs...)
}

// commandPush runs ddev push using saved push target config and one-shot overrides.
func (a *App) commandPush(args []string) error {
	opts, err := parseConfigCommand("push", args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	cfg, err := loadConfigForRuntime(runtime)
	if err != nil {
		return err
	}
	if runtime.Mode == modeDDEV {
		applyDDEVDefaults(runtime.Root, &cfg)
	}
	cfg = opts.apply(cfg)
	cfg = opts.applyGenericAsPush(cfg)
	if runtime.Mode == modeStandalone && !opts.Silent {
		prompter := newPrompter(a.Stdin, a.Stdout)
		if err := prompter.fillPushConfig(&cfg); err != nil {
			return err
		}
	}
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}
	if runtime.Mode == modeStandalone {
		if err := writeConfigForRuntime(runtime, cfg); err != nil {
			return err
		}
		return a.standalonePush(context.Background(), runtime.Root, cfg)
	}

	if err := installProviderFiles(runtime.Root, cfg, opts.Binary); err != nil {
		return err
	}
	ddevArgs := []string{"push", defaultString(cfg.Provider, defaultProviderName), "--environment=" + cfg.envArgs()}
	if opts.Silent || opts.Yes {
		ddevArgs = append(ddevArgs, "-y")
	}
	if opts.SkipDB {
		ddevArgs = append(ddevArgs, "--skip-db")
	}
	if opts.SkipFiles {
		ddevArgs = append(ddevArgs, "--skip-files")
	}

	return a.runExternal(context.Background(), runtime.Root, "ddev", ddevArgs...)
}

// commandProvider handles both generated-file commands and DDEV runtime callbacks.
func (a *App) commandProvider(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ddev-wp-ssh provider {install|generate|info|auth|db-pull|files-pull|files-import|post-pull|db-push|files-push|post-push|sanitize-config|remove-blocked-plugins}")
	}

	switch args[0] {
	case "install":
		return a.commandProviderInstall(args[1:])
	case "generate":
		return a.commandProviderGenerate(args[1:])
	case "info", "auth", "db-pull", "files-pull", "files-import", "post-pull", "db-push", "files-push", "post-push", "sanitize-config", "remove-blocked-plugins":
		return a.commandProviderRuntime(args[0], args[1:])
	default:
		return fmt.Errorf("unknown provider command %q", args[0])
	}
}

// commandProviderInstall refreshes the generated DDEV provider and hook files.
func (a *App) commandProviderInstall(args []string) error {
	opts, err := parseConfigCommand("provider install", args, a.Stderr)
	if err != nil {
		return err
	}

	runtime := a.resolveRuntime(opts.ProjectRoot)
	if runtime.Mode != modeDDEV {
		return errors.New("provider install requires DDEV mode; `ddev describe -j` did not succeed")
	}
	cfg, err := loadConfigForRuntime(runtime)
	if err != nil {
		return err
	}
	applyDDEVDefaults(runtime.Root, &cfg)
	cfg = opts.apply(cfg)
	if err := installProviderFiles(runtime.Root, cfg, opts.Binary); err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, generatedFilesReport(runtime.Root, cfg))
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
	if runtime.Mode != modeDDEV {
		return errors.New("provider runtime commands require DDEV mode; `ddev describe -j` did not succeed")
	}
	cfg, err := loadConfigForRuntime(runtime)
	if err != nil {
		return err
	}
	applyDDEVDefaults(runtime.Root, &cfg)

	ctx := context.Background()
	switch name {
	case "info":
		return a.providerInfo(cfg)
	case "auth":
		return a.providerAuth(ctx, runtime.Root, cfg)
	case "db-pull":
		return a.dbPull(ctx, runtime.Root, cfg)
	case "files-pull":
		return a.filesPull(ctx, runtime.Root, cfg)
	case "files-import":
		a.filesImport()
		return nil
	case "post-pull":
		return a.postPull(ctx, runtime.Root, cfg)
	case "db-push":
		return a.dbPush(ctx, runtime.Root, cfg)
	case "files-push":
		return a.filesPush(ctx, runtime.Root, cfg)
	case "post-push":
		return a.postPush(ctx, runtime.Root, cfg)
	case "sanitize-config":
		return a.sanitizeWPConfig(runtime.Root, cfg)
	case "remove-blocked-plugins":
		return a.removeBlockedPlugins(ctx, runtime.Root, cfg, opts.WordPressRoot)
	default:
		return fmt.Errorf("unknown provider runtime command %q", name)
	}
}

// commandPlugins exposes plugin cleanup without DDEV custom command shell files.
func (a *App) commandPlugins(args []string) error {
	if len(args) == 0 || args[0] != "remove" {
		return errors.New("usage: ddev-wp-ssh plugins remove [wordpress-root]")
	}
	opts, err := parseRuntimeCommand("plugins remove", args[1:], a.Stderr)
	if err != nil {
		return err
	}

	projectRoot, err := a.resolveProjectRoot(opts.ProjectRoot)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(projectRoot)
	if err != nil {
		return err
	}
	return a.removeBlockedPlugins(context.Background(), projectRoot, cfg, opts.WordPressRoot)
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
func loadConfigForRuntime(runtime runtimeContext) (Config, error) {
	if runtime.Mode == modeDDEV {
		return loadConfig(runtime.Root)
	}
	return loadStandaloneConfig(runtime.Root)
}

// writeConfigForRuntime persists config without creating DDEV files in standalone mode.
func writeConfigForRuntime(runtime runtimeContext, cfg Config) error {
	if runtime.Mode == modeDDEV {
		return writeConfigFile(projectConfigPath(runtime.Root), cfg)
	}
	return writeConfigFile(standaloneConfigPath(runtime.Root), cfg)
}

// standaloneFilesReport lists files owned by standalone mode.
func standaloneFilesReport(root string, cfg Config) string {
	paths := []string{
		standaloneConfigPath(root),
	}
	if cfg.PluginRemoveFile != "" {
		paths = append(paths, pluginListPath(root, cfg))
	}
	return strings.Join(paths, "\n")
}

// standalonePull executes the direct pull pipeline outside DDEV provider mode.
func (a *App) standalonePull(ctx context.Context, root string, cfg Config) error {
	if err := a.dbPull(ctx, root, cfg); err != nil {
		return err
	}
	if err := a.filesPull(ctx, root, cfg); err != nil {
		return err
	}
	if err := a.importStandaloneDB(ctx, root, cfg); err != nil {
		return err
	}
	return a.postPull(ctx, root, cfg)
}

// standalonePush executes the direct push pipeline outside DDEV provider mode.
func (a *App) standalonePush(ctx context.Context, root string, cfg Config) error {
	if err := a.dbPush(ctx, root, cfg); err != nil {
		return err
	}
	if err := a.filesPush(ctx, root, cfg); err != nil {
		return err
	}
	return a.postPush(ctx, root, cfg)
}

// configOptions tracks flags shared by init, pull, and provider install.
type configOptions struct {
	ProjectRoot       string
	Binary            string
	Silent            bool
	Yes               bool
	SkipDB            bool
	SkipFiles         bool
	SkipImport        bool
	Provider          string
	User              string
	Host              string
	Port              string
	RemotePath        string
	RemoteTmpDir      string
	PushUser          string
	PushHost          string
	PushPort          string
	PushRemotePath    string
	PushRemoteTmpDir  string
	PushURL           string
	LocalWPPath       string
	CloneImages       bool
	PluginRemoveFile  string
	LocalURL          string
	SkipSearchReplace bool
}

// parseConfigCommand parses flags shared by user-facing setup and pull commands.
func parseConfigCommand(name string, args []string, stderr io.Writer) (configOptions, error) {
	opts := configOptions{Binary: defaultBinaryPath()}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.ProjectRoot, "project-root", "", "DDEV project root")
	fs.StringVar(&opts.Binary, "binary", opts.Binary, "binary path used by generated provider files")
	fs.BoolVar(&opts.Silent, "silent", false, "do not prompt")
	fs.BoolVar(&opts.Yes, "yes", false, "skip DDEV confirmation")
	fs.BoolVar(&opts.Yes, "y", false, "skip DDEV confirmation")
	fs.BoolVar(&opts.SkipDB, "skip-db", false, "skip database operation")
	fs.BoolVar(&opts.SkipFiles, "skip-files", false, "skip file operation")
	fs.BoolVar(&opts.SkipImport, "skip-import", false, "download without importing")
	fs.StringVar(&opts.Provider, "provider", "", "DDEV provider name")
	fs.StringVar(&opts.User, "user", "", "upstream SSH user")
	fs.StringVar(&opts.Host, "host", "", "upstream SSH host")
	fs.StringVar(&opts.Port, "port", "", "upstream SSH port")
	fs.StringVar(&opts.RemotePath, "remote-path", "", "upstream WordPress root")
	fs.StringVar(&opts.RemoteTmpDir, "remote-tmp-dir", "", "upstream temporary directory")
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
	WordPressRoot string
}

// parseRuntimeCommand accepts a project root and optional WordPress root argument.
func parseRuntimeCommand(name string, args []string, stderr io.Writer) (runtimeOptions, error) {
	opts := runtimeOptions{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.ProjectRoot, "project-root", "", "DDEV project root")
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
