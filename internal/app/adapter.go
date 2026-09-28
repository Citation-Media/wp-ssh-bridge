package app

import (
	"context"
	"errors"
	"os"
)

type operationHook func(context.Context, *App, string, Config) error

// runtimeAdapter isolates environment-specific behavior from shared pull/push pipelines.
type runtimeAdapter interface {
	Mode() runtimeMode
	Root() string
	ApplyConfigDefaults(*Config)
	InstallProjectFiles(Config, string) error
	PreparePull(*App, Config, configOptions) error
	PreparePush(*App, Config, configOptions) error
	PostPullHooks() []operationHook
}

func adapterForRuntime(runtime runtimeContext) runtimeAdapter {
	switch runtime.Mode {
	case modeDDEV:
		return ddevAdapter{runtime: runtime}
	case modeWPEnv:
		return wpEnvAdapter{standaloneAdapter{runtime: runtime}}
	default:
		return standaloneAdapter{runtime: runtime}
	}
}

type standaloneAdapter struct {
	runtime runtimeContext
}

func (adapter standaloneAdapter) Mode() runtimeMode {
	return adapter.runtime.Mode
}

func (adapter standaloneAdapter) Root() string {
	return adapter.runtime.Root
}

func (adapter standaloneAdapter) ApplyConfigDefaults(*Config) {}

func (adapter standaloneAdapter) InstallProjectFiles(cfg Config, binary string) error {
	return installStandaloneFiles(adapter.runtime.Root, cfg)
}

func (adapter standaloneAdapter) PreparePull(app *App, cfg Config, opts configOptions) error {
	return persistPromptedConfig(adapter.runtime, opts, cfg)
}

func (adapter standaloneAdapter) PreparePush(app *App, cfg Config, opts configOptions) error {
	return persistPromptedConfig(adapter.runtime, opts, cfg)
}

// persistPromptedConfig saves what the user confirmed at the prompts, so the next run
// does not ask again. A --silent run prompted nothing, so it keeps flag and environment
// values one-shot, and the clone database password never moves from the environment or
// a flag into the config file, which is meant to be committed.
func persistPromptedConfig(runtime runtimeContext, opts configOptions, cfg Config) error {
	if opts.Silent {
		return nil
	}
	saved, err := readConfigFile(configPathForRuntime(runtime, opts.ConfigFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg.CloneDBPassword = saved.CloneDBPassword
	return writeConfigForRuntime(runtime, opts.ConfigFile, cfg)
}

func (adapter standaloneAdapter) PostPullHooks() []operationHook {
	return nil
}

// wpEnvAdapter runs local WP-CLI through `wp-env run cli` because wp-env keeps the
// database inside its Docker network, unreachable from the host. It behaves like the
// standalone adapter apart from the mounted-sources warning; push never reaches
// PreparePush because commandPush rejects wp-env via ensurePushAdapterSupported first.
type wpEnvAdapter struct {
	standaloneAdapter
}

func (adapter wpEnvAdapter) PreparePull(app *App, cfg Config, opts configOptions) error {
	app.warnWPEnvMountedSources(adapter.runtime.Root, opts)
	return persistPromptedConfig(adapter.runtime, opts, cfg)
}

type ddevAdapter struct {
	runtime runtimeContext
}

func (adapter ddevAdapter) Mode() runtimeMode {
	return adapter.runtime.Mode
}

func (adapter ddevAdapter) Root() string {
	return adapter.runtime.Root
}

func (adapter ddevAdapter) ApplyConfigDefaults(cfg *Config) {
	applyDDEVDefaults(adapter.runtime.Root, cfg)
}

func (adapter ddevAdapter) InstallProjectFiles(cfg Config, binary string) error {
	return installProviderFiles(adapter.runtime.Root, cfg, binary)
}

func (adapter ddevAdapter) PreparePull(app *App, cfg Config, opts configOptions) error {
	if err := adapter.InstallProjectFiles(cfg, opts.Binary); err != nil {
		return err
	}
	return app.confirmDirectOperation("pull", cfg.pullTarget(), opts)
}

func (adapter ddevAdapter) PreparePush(app *App, cfg Config, opts configOptions) error {
	if err := adapter.InstallProjectFiles(cfg, opts.Binary); err != nil {
		return err
	}
	return app.confirmDirectOperation("push", cfg.pushTarget(), opts)
}

func (adapter ddevAdapter) PostPullHooks() []operationHook {
	return []operationHook{
		func(ctx context.Context, app *App, projectRoot string, cfg Config) error {
			return app.sanitizeWPConfig(projectRoot, cfg)
		},
	}
}
