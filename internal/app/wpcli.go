package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const wpCLIPharName = "ddev-wp-ssh-wp-cli.phar"

var wpCLIPharURL = "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar"

// ensureLocalWPCLI verifies the host-side WP-CLI command used by local operations.
func (a *App) ensureLocalWPCLI(ctx context.Context, projectRoot string, cfg Config) error {
	err := a.runStep("Checking host WP-CLI compatibility", "Host WP-CLI compatibility verified", func() error {
		return a.ensureLocalWPCLIAvailable(ctx, projectRoot, cfg)
	})
	if err != nil {
		return fmt.Errorf("fatal: host WP-CLI is not available or cannot run: %w", err)
	}
	return nil
}

func (a *App) ensureLocalWPCLIAvailable(ctx context.Context, projectRoot string, cfg Config) error {
	if _, ok := ddevDescribe(projectRoot); ok {
		stderr := a.UI.PrefixedWriter("ddev", true)
		defer flushPrefixed(stderr)
		return a.runExternalWithWriters(ctx, projectRoot, "ddev", io.Discard, stderr, "wp", "--allow-root", "cli", "version")
	}

	phar := localWPCLIPharPath(projectRoot)
	if fileExists(phar) {
		if err := a.testLocalWPCLIPhar(ctx, projectRoot, phar); err == nil {
			return nil
		}
		_ = os.Remove(phar)
	}

	if commandExists("wp") {
		if err := a.runExternalWithWriters(ctx, projectRoot, "wp", io.Discard, io.Discard, "--allow-root", "cli", "version"); err == nil {
			return nil
		}
	}

	if err := downloadLocalWPCLI(ctx, phar); err != nil {
		return fmt.Errorf("WP-CLI is missing and %s could not be downloaded: %w", wpCLIPharURL, err)
	}
	if err := a.testLocalWPCLIPhar(ctx, projectRoot, phar); err != nil {
		return fmt.Errorf("downloaded %s but it is not executable on this host: %w", phar, err)
	}
	return nil
}

func (a *App) testLocalWPCLIPhar(ctx context.Context, projectRoot string, phar string) error {
	name, args := localWPCLIPharCommand(phar)
	args = append(args, "--allow-root", "cli", "version")
	return a.runExternalWithWriters(ctx, projectRoot, name, io.Discard, io.Discard, args...)
}

func downloadLocalWPCLI(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, wpCLIPharURL, nil)
	if err != nil {
		return err
	}

	client := http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %s", response.Status)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".wp-cli-*.phar")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, err := io.Copy(temp, response.Body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func localWPCLIPharPath(projectRoot string) string {
	return filepath.Join(downloadsDir(projectRoot), wpCLIPharName)
}

func localWPCLIPharCommand(phar string) (string, []string) {
	if commandExists("php") {
		return "php", []string{phar}
	}
	return phar, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ensureRemoteWPCLI verifies the remote WP-CLI command and installs a managed fallback phar when needed.
func (a *App) ensureRemoteWPCLI(ctx context.Context, projectRoot string, target RemoteTarget, label string) error {
	err := a.runStep("Checking "+label+" WP-CLI compatibility", sentenceCase(label)+" WP-CLI compatibility verified", func() error {
		return a.runSSH(ctx, projectRoot, target, remoteWPCLIEnsureCommand(target))
	})
	if err != nil {
		return fmt.Errorf("fatal: %s WP-CLI is not available or cannot run: %w", label, err)
	}
	return nil
}

func remoteWPCLIEnsureCommand(target RemoteTarget) string {
	return strings.Join([]string{
		"set -eu;",
		"cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + ";",
		remoteWPCLIPrelude(target),
		"wp_ssh_wp --allow-root cli version >/dev/null;",
	}, " ")
}

func remoteWPCLIPrelude(target RemoteTarget) string {
	phar := remoteWPCLIPharPath(target)
	return strings.Join([]string{
		"WP_SSH_WP_CLI_PHAR=" + shellQuote(phar) + ";",
		"WP_SSH_WP_CLI_MODE='';",
		"wp_ssh_download_wp_cli() {",
		"mkdir -p \"$(dirname \"$WP_SSH_WP_CLI_PHAR\")\";",
		"printf 'WP-CLI not found or unusable; downloading wp-cli.phar to %s\\n' \"$WP_SSH_WP_CLI_PHAR\";",
		"if command -v curl >/dev/null 2>&1; then",
		"curl -fsSL -o \"$WP_SSH_WP_CLI_PHAR\" " + shellQuote(wpCLIPharURL) + ";",
		"elif command -v wget >/dev/null 2>&1; then",
		"wget -q -O \"$WP_SSH_WP_CLI_PHAR\" " + shellQuote(wpCLIPharURL) + ";",
		"else",
		"echo 'Fatal: WP-CLI is missing and neither curl nor wget is available to download wp-cli.phar.' >&2;",
		"return 1;",
		"fi;",
		"chmod 755 \"$WP_SSH_WP_CLI_PHAR\";",
		"};",
		"wp_ssh_try_wp_cli_phar() {",
		"[ -s \"$WP_SSH_WP_CLI_PHAR\" ] || return 1;",
		"if \"$WP_SSH_WP_CLI_PHAR\" --allow-root cli version >/dev/null 2>&1; then WP_SSH_WP_CLI_MODE='phar'; return 0; fi;",
		"if command -v php >/dev/null 2>&1 && php \"$WP_SSH_WP_CLI_PHAR\" --allow-root cli version >/dev/null 2>&1; then WP_SSH_WP_CLI_MODE='php'; return 0; fi;",
		"return 1;",
		"};",
		"if command -v wp >/dev/null 2>&1 && wp --allow-root cli version >/dev/null 2>&1; then",
		"WP_SSH_WP_CLI_MODE='wp';",
		"elif ! wp_ssh_try_wp_cli_phar; then",
		"rm -f \"$WP_SSH_WP_CLI_PHAR\";",
		"wp_ssh_download_wp_cli;",
		"if ! wp_ssh_try_wp_cli_phar; then",
		"echo 'Fatal: WP-CLI is missing and the downloaded wp-cli.phar cannot be executed on this server.' >&2;",
		"exit 1;",
		"fi;",
		"fi;",
		"wp_ssh_wp() {",
		"if [ \"$WP_SSH_WP_CLI_MODE\" = 'wp' ]; then wp \"$@\";",
		"elif [ \"$WP_SSH_WP_CLI_MODE\" = 'phar' ]; then \"$WP_SSH_WP_CLI_PHAR\" \"$@\";",
		"else php \"$WP_SSH_WP_CLI_PHAR\" \"$@\";",
		"fi;",
		"};",
	}, " ")
}

func remoteWPCLIPharPath(target RemoteTarget) string {
	return trimTrailingSlash(defaultString(target.RemoteTmpDir, "/tmp")) + "/" + wpCLIPharName
}

func sentenceCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func localWPCLICommand(projectRoot string) (string, []string) {
	phar := localWPCLIPharPath(projectRoot)
	if fileExists(phar) {
		return localWPCLIPharCommand(phar)
	}
	return "wp", nil
}
