package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// wpCLIDatabaseExportFunctions lists the PHP functions that WP-CLI's db export calls
// directly: exec() probes the dump binary for --column-statistics, proc_open() and
// proc_close() run the dump, and escapeshellarg() builds its command line. Hosts that
// disable exec() make db export die with a silent fatal error and exit status 255.
var wpCLIDatabaseExportFunctions = []string{"exec", "proc_open", "proc_close", "escapeshellarg"}

// remotePHPRuntime is how the remote WP-CLI starts: its PHP binary, the php.ini that
// binary loads, and the WP-CLI entry script.
type remotePHPRuntime struct {
	PHPBinary  string
	PHPIni     string
	WPCLIEntry string
}

// phpCommand returns the shell words that start WP-CLI's PHP with WP-CLI's php.ini.
func (r remotePHPRuntime) phpCommand() string {
	parts := []string{shellQuote(r.PHPBinary)}
	if r.PHPIni != "" {
		parts = append(parts, "-c", shellQuote(r.PHPIni))
	}
	return strings.Join(parts, " ")
}

// remotePHPFunctionOverride starts WP-CLI's PHP with only the functions that db export
// needs removed from the host's disable_functions list. It applies to that single
// command; the host's PHP configuration and every other PHP process are unchanged.
type remotePHPFunctionOverride struct {
	remotePHPRuntime
	Reenable         []string
	DisableFunctions string
}

// required reports whether the host disables any function db export needs.
func (o remotePHPFunctionOverride) required() bool {
	return len(o.Reenable) > 0
}

// command returns the WP-CLI invocation that replaces wp_ssh_wp for db export. It
// starts the PHP binary, php.ini, and entry script that WP-CLI normally runs with, so
// bypassing a host's wp wrapper script cannot change the PHP version or configuration.
func (o remotePHPFunctionOverride) command() string {
	return o.phpCommand() + " -d " + shellQuote("disable_functions="+o.DisableFunctions) + " " + shellQuote(o.WPCLIEntry)
}

// detectRemotePHPFunctionCompatibility checks whether the PHP that runs WP-CLI disables
// functions db export needs and, if so, confirms that a per-command override restores
// them. It never runs code inside WP-CLI, so a wp-cli.yml that disables eval cannot
// hide the answer. When the answer cannot be determined, preflight fails: an export on
// a host that disables exec() would otherwise die without any message.
func (a *App) detectRemotePHPFunctionCompatibility(ctx context.Context, projectRoot string, target RemoteTarget, label string) (remotePHPFunctionOverride, error) {
	failed := func(err error) (remotePHPFunctionOverride, error) {
		return remotePHPFunctionOverride{}, fmt.Errorf("fatal: %s PHP function check failed, so the CLI cannot confirm that PHP allows %s, which WP-CLI needs for wp db export: %w", label, strings.Join(wpCLIDatabaseExportFunctions, ", "), err)
	}
	output, err := a.outputSSH(ctx, projectRoot, target, remotePHPRuntimeCommand(target))
	if err != nil {
		return failed(err)
	}
	runtime, err := parseRemotePHPRuntime(output)
	if err != nil {
		return failed(err)
	}
	output, err = a.outputSSH(ctx, projectRoot, target, remotePHPFunctionProbeCommand(target, runtime))
	if err != nil {
		return failed(err)
	}
	override, err := parseRemotePHPFunctionProbe(output, runtime)
	if err != nil {
		return failed(err)
	}
	if !override.required() {
		return override, nil
	}

	// A function that is still blocked ends PHP with a fatal error the host may hide,
	// so only a printed marker proves that the override works.
	output, err = a.outputSSH(ctx, projectRoot, target, remotePHPFunctionVerifyCommand(target, override))
	if err != nil || strings.TrimSpace(output) != "available" {
		blocked := fmt.Errorf("fatal: %s PHP disables %s, which WP-CLI needs for wp db export, and this host does not allow re-enabling them for a single command with php -d; ask the host to allow them for PHP on the command line", label, strings.Join(override.Reenable, ", "))
		if err != nil {
			return remotePHPFunctionOverride{}, fmt.Errorf("%w: %w", blocked, err)
		}
		return remotePHPFunctionOverride{}, blocked
	}
	return override, nil
}

// remotePHPRuntimeCommand reports how WP-CLI starts PHP. WP-CLI's own cli info report
// is preferred; the #! line of the WP-CLI entry script covers hosts where cli info
// fails, for example because PHP disables proc_open(), which cli info also uses.
func remotePHPRuntimeCommand(target RemoteTarget) string {
	return strings.Join([]string{
		"set -eu;",
		"cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + ";",
		remoteWPCLIPrelude(target),
		`if [ "$WP_SSH_WP_CLI_MODE" = 'wp' ]; then WP_SSH_WP_CLI_ENTRY=$(command -v wp); else WP_SSH_WP_CLI_ENTRY=$WP_SSH_WP_CLI_PHAR; fi;`,
		`printf 'php_path=%s\nwp_cli_entry=%s\nwp_cli_first_line=%s\n' "$(command -v php 2>/dev/null || true)" "$WP_SSH_WP_CLI_ENTRY" "$(head -n 1 "$WP_SSH_WP_CLI_ENTRY" 2>/dev/null || true)";`,
		`if WP_SSH_WP_CLI_INFO=$(wp_ssh_wp --allow-root cli info --format=json 2>/dev/null); then printf '%s\n' "$WP_SSH_WP_CLI_INFO"; else printf 'wp_cli_info_status=%s\n' "$?"; fi`,
	}, " ")
}

// parseRemotePHPRuntime resolves WP-CLI's PHP from the runtime report, preferring
// WP-CLI's cli info JSON over the entry script's #! line.
func parseRemotePHPRuntime(output string) (remotePHPRuntime, error) {
	lines := strings.Split(output, "\n")
	var info struct {
		PHPBinaryPath string `json:"php_binary_path"`
		PHPIniUsed    any    `json:"php_ini_used"`
		WPCLIPharPath string `json:"wp_cli_phar_path"`
		WPCLIDirPath  string `json:"wp_cli_dir_path"`
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "{") {
			_ = json.Unmarshal([]byte(line), &info)
		}
	}
	runtime := remotePHPRuntime{PHPBinary: info.PHPBinaryPath, WPCLIEntry: strings.TrimPrefix(info.WPCLIPharPath, "phar://")}
	if ini, ok := info.PHPIniUsed.(string); ok {
		runtime.PHPIni = ini
	}
	if runtime.WPCLIEntry == "" && info.WPCLIDirPath != "" && !strings.HasPrefix(info.WPCLIDirPath, "phar://") {
		// Composer and git installs start WP-CLI from this file instead of a phar.
		runtime.WPCLIEntry = info.WPCLIDirPath + "/php/boot-fs.php"
	}
	if runtime.PHPBinary != "" && runtime.WPCLIEntry != "" {
		return runtime, nil
	}

	values := envMap(lines)
	entry := values["wp_cli_entry"]
	interpreter, ok := phpInterpreter(values["wp_cli_first_line"], values["php_path"])
	if !ok || entry == "" {
		status := "no report"
		if code := values["wp_cli_info_status"]; code != "" {
			status = "exit status " + code
		}
		return remotePHPRuntime{}, fmt.Errorf("could not determine the PHP that runs WP-CLI: wp cli info failed (%s) and %s does not start with a #! line naming php", status, defaultString(entry, "the WP-CLI entry script"))
	}
	return remotePHPRuntime{PHPBinary: interpreter, WPCLIEntry: entry}, nil
}

// phpInterpreter returns the PHP binary named by a script's #! line. "env php" resolves
// through PATH exactly like `command -v php`; an absolute path to php is used as is.
func phpInterpreter(firstLine string, phpPath string) (string, bool) {
	line, ok := strings.CutPrefix(firstLine, "#!")
	fields := strings.Fields(line)
	if !ok || len(fields) == 0 {
		return "", false
	}
	if path.Base(fields[0]) == "env" {
		return phpPath, len(fields) == 2 && fields[1] == "php" && phpPath != ""
	}
	return fields[0], strings.HasPrefix(path.Base(fields[0]), "php")
}

// remotePHPFunctionProbeCommand asks WP-CLI's PHP with plain php -r, outside WP-CLI,
// which db export functions it disables and what its disable_functions list is.
func remotePHPFunctionProbeCommand(target RemoteTarget, runtime remotePHPRuntime) string {
	code := `$missing = array(); foreach (array(` + phpStringList(wpCLIDatabaseExportFunctions) + `) as $name) { if (!function_exists($name)) { $missing[] = $name; } } echo "reenable=", implode(",", $missing), "\n", "disable_functions=", ini_get("disable_functions"), "\n";`
	return "cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + " && " + runtime.phpCommand() + " -r " + shellQuote(code)
}

// remotePHPFunctionVerifyCommand prints "available" only when the override leaves every
// db export function callable.
func remotePHPFunctionVerifyCommand(target RemoteTarget, override remotePHPFunctionOverride) string {
	code := `foreach (array(` + phpStringList(wpCLIDatabaseExportFunctions) + `) as $name) { if (!function_exists($name)) { exit(1); } } echo "available";`
	return "cd " + shellQuote(trimTrailingSlash(target.RemotePath)) + " && " + override.phpCommand() + " -d " + shellQuote("disable_functions="+override.DisableFunctions) + " -r " + shellQuote(code)
}

// parseRemotePHPFunctionProbe builds the override from the probe's key=value lines,
// ignoring other output such as PHP notices. The host's other disabled functions stay
// disabled.
func parseRemotePHPFunctionProbe(output string, runtime remotePHPRuntime) (remotePHPFunctionOverride, error) {
	values := envMap(strings.Split(output, "\n"))
	reenable, ok := values["reenable"]
	if !ok {
		return remotePHPFunctionOverride{}, errors.New("PHP did not report which functions are available")
	}
	if reenable == "" {
		return remotePHPFunctionOverride{}, nil
	}
	keep := []string{}
	for _, name := range strings.Split(values["disable_functions"], ",") {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(wpCLIDatabaseExportFunctions, strings.ToLower(name)) {
			keep = append(keep, name)
		}
	}
	return remotePHPFunctionOverride{
		remotePHPRuntime: runtime,
		Reenable:         strings.Split(reenable, ","),
		DisableFunctions: strings.Join(keep, ","),
	}, nil
}

// phpStringList renders Go strings as a comma-separated list of PHP string literals.
func phpStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+value+`"`)
	}
	return strings.Join(quoted, ", ")
}

// setRemotePHPFunctionOverride preserves the preflight result for the database export
// that follows in the same CLI process.
func (a *App) setRemotePHPFunctionOverride(target RemoteTarget, override remotePHPFunctionOverride) {
	if a.remotePHPFunctions == nil {
		a.remotePHPFunctions = map[string]remotePHPFunctionOverride{}
	}
	a.remotePHPFunctions[remoteTargetKey(target)] = override
}

// remotePHPFunctionOverrideFor returns the preflight result for this SSH target.
func (a *App) remotePHPFunctionOverrideFor(target RemoteTarget) remotePHPFunctionOverride {
	return a.remotePHPFunctions[remoteTargetKey(target)]
}
