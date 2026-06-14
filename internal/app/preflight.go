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
	Label            string
	Target           RemoteTarget
	NeedWPCLI        bool
	NeedPathReadable bool
	NeedPathWritable bool
	AllowCreatePath  bool
	NeedTmpWritable  bool
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
				Label:            "pull source",
				Target:           cfg.pullTarget(),
				NeedWPCLI:        !opts.SkipDB,
				NeedPathReadable: true,
				NeedTmpWritable:  !opts.SkipDB,
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
		Label:           "push target",
		Target:          cfg.pushTarget(),
		NeedWPCLI:       !opts.SkipDB,
		NeedTmpWritable: !opts.SkipDB,
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
		NeedRsync:      true,
		NeedLocalWPCLI: true,
		Remotes:        remotes,
	}, cfg)
}

func (a *App) runPreflight(ctx context.Context, plan preflightPlan, cfg Config) error {
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
	}
	if plan.NeedLocalWPCLI {
		if err := a.ensureLocalWPCLI(ctx, plan.ProjectRoot, cfg); err != nil {
			return err
		}
	}
	return nil
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
