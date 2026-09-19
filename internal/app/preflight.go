package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type preflightRemoteNeeds struct {
	Label                         string
	Target                        RemoteTarget
	NeedWPCLI                     bool
	NeedMariaDBCompatibilityCheck bool
	NeedPathReadable              bool
	NeedPathWritable              bool
	AllowCreatePath               bool
	NeedTmpWritable               bool
}

type preflightPlan struct {
	Operation             string
	ProjectRoot           string
	Mode                  runtimeMode
	NeedSSH               bool
	NeedRsync             bool
	NeedLocalWPCLI        bool
	NeedLocalPathReadable string
	NeedLocalPathWritable string
	Config                Config
	Remotes               []preflightRemoteNeeds
}

func (a *App) preflightPull(ctx context.Context, adapter runtimeAdapter, cfg Config, opts configOptions) error {
	if err := cfg.validatePullRequired(); err != nil {
		return err
	}
	if opts.SkipDB && opts.SkipFiles {
		return nil
	}

	shouldImportDB := !opts.SkipDB && !opts.SkipImport
	plan := preflightPlan{
		Operation:      "pull",
		ProjectRoot:    adapter.Root(),
		Mode:           adapter.Mode(),
		NeedSSH:        true,
		NeedRsync:      false, // transport detection in pipeline handles rsync vs scp/tar
		NeedLocalWPCLI: shouldImportDB,
		Remotes: []preflightRemoteNeeds{
			{
				Label:                         "pull source",
				Target:                        cfg.pullTarget(),
				NeedWPCLI:                     !opts.SkipDB,
				NeedMariaDBCompatibilityCheck: !opts.SkipDB,
				NeedPathReadable:              true,
				NeedTmpWritable:               !opts.SkipDB,
			},
		},
	}
	if !opts.SkipFiles {
		plan.NeedLocalPathWritable = localWPRoot(adapter.Root(), cfg)
	}
	return a.runPreflight(ctx, plan, cfg)
}

func (a *App) preflightPush(ctx context.Context, root string, mode runtimeMode, cfg Config, opts configOptions) error {
	if err := cfg.validatePushRequired(); err != nil {
		return err
	}
	if opts.SkipDB && opts.SkipFiles {
		return nil
	}

	remote := preflightRemoteNeeds{
		Label:                         "push target",
		Target:                        cfg.pushTarget(),
		NeedWPCLI:                     !opts.SkipDB,
		NeedMariaDBCompatibilityCheck: !opts.SkipDB,
		NeedTmpWritable:               !opts.SkipDB,
	}
	if !opts.SkipDB {
		remote.NeedPathReadable = true
	}
	if !opts.SkipFiles {
		remote.NeedPathWritable = true
		remote.AllowCreatePath = opts.SkipDB
	}

	plan := preflightPlan{
		Operation:      "push",
		ProjectRoot:    root,
		Mode:           mode,
		NeedSSH:        true,
		NeedRsync:      false, // transport detection in pipeline handles rsync vs scp/tar
		NeedLocalWPCLI: !opts.SkipDB,
		Remotes:        []preflightRemoteNeeds{remote},
	}
	if !opts.SkipFiles {
		plan.NeedLocalPathReadable = localWPRoot(root, cfg)
	}
	return a.runPreflight(ctx, plan, cfg)
}

func (a *App) preflightProviderAuth(ctx context.Context, projectRoot string, cfg Config) error {
	remotes := []preflightRemoteNeeds{}
	if source := cfg.pullTarget(); source.configured() {
		if err := cfg.validatePullRequired(); err != nil {
			return err
		}
		remotes = append(remotes, preflightRemoteNeeds{
			Label:            "pull source",
			Target:           source,
			NeedWPCLI:        true,
			NeedPathReadable: true,
			NeedTmpWritable:  true,
		})
	}
	if target := cfg.pushTarget(); target.configured() {
		if err := cfg.validatePushRequired(); err != nil {
			return err
		}
		remotes = append(remotes, preflightRemoteNeeds{
			Label:            "push target",
			Target:           target,
			NeedWPCLI:        true,
			NeedPathReadable: true,
			NeedTmpWritable:  true,
		})
	}
	if len(remotes) == 0 {
		return cfg.validatePullRequired()
	}
	return a.runPreflight(ctx, preflightPlan{
		Operation:      "provider auth",
		ProjectRoot:    projectRoot,
		Mode:           modeDDEV,
		NeedSSH:        true,
		NeedRsync:      false, // provider callbacks select rsync or scp/tar after authentication
		NeedLocalWPCLI: true,
		Remotes:        remotes,
	}, cfg)
}

func (a *App) runPreflight(ctx context.Context, plan preflightPlan, cfg Config) error {
	plan.Config = cfg
	if err := a.checkLocalEnvironment(plan); err != nil {
		return err
	}
	for _, remote := range plan.Remotes {
		if err := a.checkRemoteEnvironment(ctx, plan.ProjectRoot, remote); err != nil {
			return err
		}
		if remote.NeedWPCLI {
			if err := a.ensureRemoteWPCLI(ctx, plan.ProjectRoot, remote.Target, remote.Label); err != nil {
				return err
			}
		}
		if remote.NeedMariaDBCompatibilityCheck {
			needsCompatibility, err := a.detectRemoteMariaDBCompatibility(ctx, plan.ProjectRoot, remote.Target)
			if err != nil {
				return fmt.Errorf("fatal: %s database client compatibility check failed: %w", remote.Label, err)
			}
			a.setRemoteMariaDBCompatibility(remote.Target, needsCompatibility)
			if needsCompatibility {
				a.UI.Success("%s MariaDB client compatibility enabled (temporary mysql/mysqldump aliases)", sentenceCase(remote.Label))
			}
		}
	}
	if plan.NeedLocalWPCLI {
		if err := a.ensureLocalWPCLI(ctx, plan.ProjectRoot, cfg); err != nil {
			return err
		}
	}
	return nil
}

// detectRemoteMariaDBCompatibility identifies hosts that expose MariaDB through the
// legacy mysql and mysqldump names only. Current WP-CLI selects the MariaDB names on
// these hosts, so database commands need temporary compatibility symlinks.
func (a *App) detectRemoteMariaDBCompatibility(ctx context.Context, projectRoot string, target RemoteTarget) (bool, error) {
	output, err := a.outputSSHSilent(ctx, projectRoot, target, remoteMariaDBCompatibilityCheckCommand())
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "required", nil
}

// remoteMariaDBCompatibilityCheckCommand prints a marker only for the client
// layout where WP-CLI needs MariaDB compatibility aliases.
func remoteMariaDBCompatibilityCheckCommand() string {
	return strings.Join([]string{
		"set -eu;",
		"if command -v mysql >/dev/null 2>&1 && mysql --version 2>/dev/null | grep -qi 'MariaDB' && { ! command -v mariadb >/dev/null 2>&1 || { ! command -v mariadb-dump >/dev/null 2>&1 && command -v mysqldump >/dev/null 2>&1; }; }; then",
		"printf 'required\\n';",
		"fi",
	}, " ")
}

// setRemoteMariaDBCompatibility preserves the preflight result for the database
// operation that follows in the same CLI process.
func (a *App) setRemoteMariaDBCompatibility(target RemoteTarget, required bool) {
	if a.remoteMariaDBCompatibility == nil {
		a.remoteMariaDBCompatibility = map[string]bool{}
	}
	a.remoteMariaDBCompatibility[remoteTargetKey(target)] = required
}

// needsRemoteMariaDBCompatibility returns the preflight result for this SSH target.
func (a *App) needsRemoteMariaDBCompatibility(target RemoteTarget) bool {
	return a.remoteMariaDBCompatibility[remoteTargetKey(target)]
}

// remoteTargetKey distinguishes cached capability checks for separate SSH targets.
func remoteTargetKey(target RemoteTarget) string {
	return strings.Join([]string{target.Destination, target.User, target.Host, target.Port, target.RemotePath, target.RemoteTmpDir}, "\x00")
}

func (a *App) checkLocalEnvironment(plan preflightPlan) error {
	if plan.NeedSSH {
		if err := a.checkRequiredLocalCommand("ssh", "SSH is required to connect to the configured WordPress source or target"); err != nil {
			return err
		}
	}
	if plan.NeedRsync {
		if err := a.checkRequiredLocalCommand("rsync", "rsync is required to transfer WordPress database exports and files"); err != nil {
			return err
		}
	}
	if plan.Mode == modeDDEV {
		if err := a.checkDDEVEnvironment(plan.ProjectRoot); err != nil {
			return err
		}
	}
	if plan.Mode == modeWPEnv {
		if err := a.checkWPEnvEnvironment(plan.ProjectRoot, plan.Config); err != nil {
			return err
		}
	}
	if plan.NeedLocalPathReadable != "" {
		if err := a.checkLocalReadableDirectory(plan.NeedLocalPathReadable, plan.Operation); err != nil {
			return err
		}
	}
	if plan.NeedLocalPathWritable != "" {
		if err := a.checkLocalWritableDirectory(plan.NeedLocalPathWritable); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) checkRequiredLocalCommand(name string, reason string) error {
	return a.runStep("Checking local "+name, "Local "+name+" available", func() error {
		if commandExists(name) {
			return nil
		}
		return fmt.Errorf("%s is not on PATH. %s", name, reason)
	})
}

func (a *App) checkDDEVEnvironment(projectRoot string) error {
	return a.runStep("Checking DDEV project", "DDEV project available", func() error {
		if !commandExists("ddev") {
			return errors.New("ddev is not on PATH")
		}
		if _, ok := ddevDescribe(projectRoot); ok {
			return nil
		}
		return errors.New("ddev describe failed; run this command from a DDEV project root or start the project with ddev start")
	})
}

// checkWPEnvEnvironment verifies the wp-env environment can run WP-CLI before any
// database or file changes start.
func (a *App) checkWPEnvEnvironment(projectRoot string, cfg Config) error {
	return a.runStep("Checking wp-env project", "wp-env project available", func() error {
		status, ok := wpEnvStatus(projectRoot)
		if !ok {
			return errors.New("wp-env status failed; run this command from a wp-env project root and start it with wp-env start")
		}
		if !status.supportsRun() {
			return fmt.Errorf("wp-env is using the %q runtime, which does not support `wp-env run`; restart with the Docker runtime using wp-env start --runtime=docker", status.Runtime)
		}
		if !status.running() {
			return fmt.Errorf("wp-env environment is %q; start it with wp-env start", defaultString(status.Status, "not running"))
		}
		root := status.wordPressRoot()
		if root == "" {
			return errors.New("wp-env did not report an install path; start the environment with wp-env start")
		}
		// wp-env mounts a `core` source in place of <installPath>/WordPress, so confirm the
		// resolved mount actually exists before anything writes into it.
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return fmt.Errorf("wp-env reported WordPress at %s, but that directory is not readable; run wp-env start to create it", root)
		}
		// Files sync to localWPRoot while WP-CLI runs against the mapped container path. If
		// the configured root is outside the mounted tree those are two different WordPress
		// installs, and every later step would silently operate on the wrong one.
		local := localWPRoot(projectRoot, cfg)
		if _, ok := wpEnvContainerPath(status, local); !ok {
			return fmt.Errorf("local WordPress path %s is outside the wp-env tree at %s, so files and WP-CLI would target different installs; clear local_wp_path to use the wp-env root", local, root)
		}
		return nil
	})
}

func (a *App) checkLocalReadableDirectory(path string, operation string) error {
	return a.runStep("Checking local WordPress files", "Local WordPress files readable", func() error {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s needs local WordPress files at %s: %w", operation, path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s needs local WordPress path %s to be a directory", operation, path)
		}
		if !localDirReadable(path) {
			return fmt.Errorf("local WordPress files are not readable at %s", path)
		}
		return nil
	})
}

func (a *App) checkLocalWritableDirectory(path string) error {
	return a.runStep("Checking local WordPress write access", "Local WordPress files writable", func() error {
		if err := ensureLocalPullDestinationWritable(path); err != nil {
			return err
		}
		return nil
	})
}

func (a *App) checkRemoteEnvironment(ctx context.Context, projectRoot string, needs preflightRemoteNeeds) error {
	return a.runStep("Checking "+needs.Label+" environment", sentenceCase(needs.Label)+" environment verified", func() error {
		if err := a.runSSHQuietSuccess(ctx, projectRoot, needs.Target, remotePreflightCommand(needs)); err != nil {
			return fmt.Errorf("fatal: %s environment check failed: %w", needs.Label, err)
		}
		return nil
	})
}

func remotePreflightCommand(needs preflightRemoteNeeds) string {
	remotePath := trimTrailingSlash(needs.Target.RemotePath)
	tmpDir := trimTrailingSlash(defaultString(needs.Target.RemoteTmpDir, "/tmp"))
	commands := []string{
		"set -eu;",
		"WP_SSH_REMOTE_PATH=" + shellQuote(remotePath) + ";",
		"WP_SSH_REMOTE_TMP=" + shellQuote(tmpDir) + ";",
	}
	if needs.AllowCreatePath {
		commands = append(commands, "mkdir -p \"$WP_SSH_REMOTE_PATH\";")
	}
	commands = append(commands,
		"if [ ! -d \"$WP_SSH_REMOTE_PATH\" ]; then echo \"Fatal: remote WordPress path does not exist: $WP_SSH_REMOTE_PATH\" >&2; exit 1; fi;",
	)
	if needs.NeedPathReadable {
		commands = append(commands,
			"if [ ! -r \"$WP_SSH_REMOTE_PATH\" ] || [ ! -x \"$WP_SSH_REMOTE_PATH\" ]; then echo \"Fatal: remote WordPress path is not readable: $WP_SSH_REMOTE_PATH\" >&2; exit 1; fi;",
		)
	}
	if needs.NeedPathWritable {
		commands = append(commands,
			"if [ ! -w \"$WP_SSH_REMOTE_PATH\" ]; then echo \"Fatal: remote WordPress path is not writable: $WP_SSH_REMOTE_PATH\" >&2; exit 1; fi;",
		)
	}
	if needs.NeedTmpWritable {
		commands = append(commands,
			"mkdir -p \"$WP_SSH_REMOTE_TMP\";",
			"if [ ! -w \"$WP_SSH_REMOTE_TMP\" ]; then echo \"Fatal: remote temporary directory is not writable: $WP_SSH_REMOTE_TMP\" >&2; exit 1; fi;",
		)
	}
	return strings.Join(commands, " ")
}

// ensureLocalPullDestinationWritable catches stale root/container-owned files before rsync emits many partial errors.
func ensureLocalPullDestinationWritable(destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return localPullPermissionError(path, destination, walkErr)
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return localPullPermissionError(path, destination, err)
		}
		if localDirWritable(path) {
			return nil
		}
		if err := os.Chmod(path, info.Mode().Perm()|0o700); err != nil {
			return localPullPermissionError(path, destination, err)
		}
		if !localDirWritable(path) {
			return localPullPermissionError(path, destination, os.ErrPermission)
		}
		return nil
	})
}

func localDirReadable(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	_ = entries
	return true
}

func localDirWritable(path string) bool {
	probe, err := os.MkdirTemp(path, ".wp-ssh-bridge-write-test-")
	if err != nil {
		return false
	}
	return os.Remove(probe) == nil
}

func localPullPermissionError(path string, destination string, err error) error {
	return fmt.Errorf("local WordPress files are not writable at %s: %w. Fix ownership or permissions for %s, then run the pull again; for WSL/Linux use: sudo chown -R $(id -u):$(id -g) %s && chmod -R u+rwX %s", path, err, destination, shellQuote(destination), shellQuote(destination))
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
