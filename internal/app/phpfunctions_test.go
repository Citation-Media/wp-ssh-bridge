package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// hostingerCLIInfo is WP-CLI 2.12's cli info JSON on Hostinger, with PHP's escaped slashes.
const hostingerCLIInfo = `{"php_binary_path":"\/opt\/alt\/php82\/usr\/bin\/php","php_ini_used":"\/opt\/alt\/php82\/etc\/php.ini","wp_cli_phar_path":"phar:\/\/\/usr\/local\/bin\/wp-cli-2.12.0.phar","wp_cli_dir_path":"phar:\/\/wp-cli.phar\/vendor\/wp-cli\/wp-cli"}`

var hostingerRuntime = remotePHPRuntime{
	PHPBinary:  "/opt/alt/php82/usr/bin/php",
	PHPIni:     "/opt/alt/php82/etc/php.ini",
	WPCLIEntry: "/usr/local/bin/wp-cli-2.12.0.phar",
}

// fakePHPFunctionSSH answers the runtime report and the PHP probe like Hostinger,
// where exec() is disabled, and the override check with the given output. Any command
// that runs WP-CLI's eval fails, as on a host whose wp-cli.yml disables it.
func fakePHPFunctionSSH(verifyOutput string) string {
	return `case "$*" in
  *" eval "*) printf "Error: The 'eval' command has been disabled from the config file.\n" >&2; exit 1 ;;
  *"cli info --format=json"*) printf 'php_path=/usr/bin/php\nwp_cli_entry=/usr/local/bin/wp\nwp_cli_first_line=#!/usr/bin/env php\n%s\n' '` + hostingerCLIInfo + `' ;;
  *"-d 'disable_functions="*) printf '` + verifyOutput + `' ;;
  *reenable=*) printf 'reenable=exec\ndisable_functions=system,exec,shell_exec\n' ;;
esac
`
}

func TestRemotePHPRuntimeCommandDoesNotRunCodeInsideWPCLI(t *testing.T) {
	t.Parallel()
	command := remotePHPRuntimeCommand(RemoteTarget{RemotePath: "/var/www/html/"})
	for _, want := range []string{
		"cd '/var/www/html';",
		"wp_ssh_wp --allow-root cli info --format=json",
		"command -v php",
		`head -n 1 "$WP_SSH_WP_CLI_ENTRY"`,
		"wp_cli_info_status=",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("PHP runtime report missing %q:\n%s", want, command)
		}
	}
	for _, unwanted := range []string{" eval ", "--exec", "--require"} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("PHP runtime report must not run code inside WP-CLI (%q):\n%s", unwanted, command)
		}
	}
}

func TestParseRemotePHPRuntimePrefersWPCLIInfo(t *testing.T) {
	t.Parallel()
	runtime, err := parseRemotePHPRuntime("php_path=/usr/bin/php\nwp_cli_entry=/usr/local/bin/wp\nwp_cli_first_line=#!/usr/bin/env php\n" + hostingerCLIInfo + "\n")
	if err != nil {
		t.Fatalf("parseRemotePHPRuntime() error = %v", err)
	}
	if runtime != hostingerRuntime {
		t.Fatalf("parseRemotePHPRuntime() = %+v, want %+v", runtime, hostingerRuntime)
	}
}

func TestParseRemotePHPRuntimeStartsComposerInstallsFromBootFS(t *testing.T) {
	t.Parallel()
	runtime, err := parseRemotePHPRuntime(`{"php_binary_path":"\/usr\/bin\/php8.2","php_ini_used":false,"wp_cli_phar_path":"","wp_cli_dir_path":"\/home\/deploy\/.composer\/vendor\/wp-cli\/wp-cli"}`)
	if err != nil {
		t.Fatalf("parseRemotePHPRuntime() error = %v", err)
	}
	want := remotePHPRuntime{PHPBinary: "/usr/bin/php8.2", WPCLIEntry: "/home/deploy/.composer/vendor/wp-cli/wp-cli/php/boot-fs.php"}
	if runtime != want {
		t.Fatalf("parseRemotePHPRuntime() = %+v, want %+v", runtime, want)
	}
}

func TestParseRemotePHPRuntimeFallsBackToEntryScriptInterpreter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		firstLine string
		want      string
	}{
		{firstLine: "#!/usr/bin/env php", want: "/usr/bin/php"},
		{firstLine: "#!/opt/php82/bin/php -q", want: "/opt/php82/bin/php"},
	} {
		runtime, err := parseRemotePHPRuntime("php_path=/usr/bin/php\nwp_cli_entry=/usr/local/bin/wp\nwp_cli_first_line=" + tc.firstLine + "\nwp_cli_info_status=255\n")
		if err != nil {
			t.Fatalf("parseRemotePHPRuntime(%q) error = %v", tc.firstLine, err)
		}
		want := remotePHPRuntime{PHPBinary: tc.want, WPCLIEntry: "/usr/local/bin/wp"}
		if runtime != want {
			t.Fatalf("parseRemotePHPRuntime(%q) = %+v, want %+v", tc.firstLine, runtime, want)
		}
	}
}

func TestParseRemotePHPRuntimeFailsWhenWPCLIIsAWrapperAndCLIInfoFails(t *testing.T) {
	t.Parallel()
	for _, firstLine := range []string{"#!/bin/bash", "#!/usr/bin/env php8.2"} {
		_, err := parseRemotePHPRuntime("php_path=/usr/bin/php\nwp_cli_entry=/usr/local/bin/wp\nwp_cli_first_line=" + firstLine + "\nwp_cli_info_status=255\n")
		if err == nil {
			t.Fatalf("parseRemotePHPRuntime() accepted %q without cli info", firstLine)
		}
		for _, want := range []string{"wp cli info failed (exit status 255)", "/usr/local/bin/wp", "#! line naming php"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error missing %q: %v", want, err)
			}
		}
	}
}

func TestRemotePHPFunctionCommandsUsePlainPHP(t *testing.T) {
	t.Parallel()
	target := RemoteTarget{RemotePath: "/var/www/html"}
	override := remotePHPFunctionOverride{remotePHPRuntime: hostingerRuntime, Reenable: []string{"exec"}, DisableFunctions: "system"}
	commands := map[string][]string{
		remotePHPFunctionProbeCommand(target, hostingerRuntime): {
			"cd '/var/www/html' && '/opt/alt/php82/usr/bin/php' -c '/opt/alt/php82/etc/php.ini' -r ",
			`array("exec", "proc_open", "proc_close", "escapeshellarg")`,
			`ini_get("disable_functions")`,
		},
		remotePHPFunctionVerifyCommand(target, override): {
			"cd '/var/www/html' && '/opt/alt/php82/usr/bin/php' -c '/opt/alt/php82/etc/php.ini' -d 'disable_functions=system' -r ",
			`echo "available";`,
		},
	}
	for command, wants := range commands {
		for _, want := range wants {
			if !strings.Contains(command, want) {
				t.Fatalf("PHP command missing %q:\n%s", want, command)
			}
		}
		if strings.Contains(command, "wp_ssh_wp") || strings.Contains(command, " eval ") {
			t.Fatalf("PHP function checks must not run through WP-CLI:\n%s", command)
		}
	}
}

func TestParseRemotePHPFunctionProbeKeepsOtherDisabledFunctions(t *testing.T) {
	t.Parallel()
	override, err := parseRemotePHPFunctionProbe("PHP Notice: ignored=noise\nreenable=\ndisable_functions=system\n", hostingerRuntime)
	if err != nil || override.required() {
		t.Fatalf("available functions = (%+v, %v), want no override", override, err)
	}

	override, err = parseRemotePHPFunctionProbe("reenable=exec,proc_open\ndisable_functions=system, EXEC ,shell_exec,proc_open,popen\n", hostingerRuntime)
	if err != nil {
		t.Fatalf("parseRemotePHPFunctionProbe() error = %v", err)
	}
	if strings.Join(override.Reenable, ",") != "exec,proc_open" || override.DisableFunctions != "system,shell_exec,popen" || override.remotePHPRuntime != hostingerRuntime {
		t.Fatalf("parseRemotePHPFunctionProbe() = %+v, want exec,proc_open re-enabled and system,shell_exec,popen still disabled", override)
	}

	if _, err := parseRemotePHPFunctionProbe("", hostingerRuntime); err == nil {
		t.Fatal("probe output without a result should be an error")
	}
}

func TestRemotePHPFunctionOverrideCommandReenablesOnlyForThisInvocation(t *testing.T) {
	t.Parallel()
	override := remotePHPFunctionOverride{remotePHPRuntime: hostingerRuntime, Reenable: []string{"exec"}, DisableFunctions: "system,shell_exec"}
	want := "'/opt/alt/php82/usr/bin/php' -c '/opt/alt/php82/etc/php.ini' -d 'disable_functions=system,shell_exec' '/usr/local/bin/wp-cli-2.12.0.phar'"
	if got := override.command(); got != want {
		t.Fatalf("command() = %s, want %s", got, want)
	}

	override.PHPIni = ""
	override.DisableFunctions = ""
	want = "'/opt/alt/php82/usr/bin/php' -d 'disable_functions=' '/usr/local/bin/wp-cli-2.12.0.phar'"
	if got := override.command(); got != want {
		t.Fatalf("command() without php.ini = %s, want %s", got, want)
	}
}

func TestDetectRemotePHPFunctionCompatibilityWorksWhenWPCLIEvalIsDisabled(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, fakePHPFunctionSSH("available"))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	override, err := app.detectRemotePHPFunctionCompatibility(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, "pull source")
	if err != nil {
		t.Fatalf("detectRemotePHPFunctionCompatibility() error = %v", err)
	}
	if strings.Join(override.Reenable, ",") != "exec" || override.DisableFunctions != "system,shell_exec" || override.remotePHPRuntime != hostingerRuntime {
		t.Fatalf("detectRemotePHPFunctionCompatibility() = %+v, want exec override with the Hostinger runtime", override)
	}
	commands := readSSHLog(t, logPath)
	if len(commands) != 3 || !strings.Contains(commands[0], "cli info --format=json") || !strings.Contains(commands[1], "reenable=") || !strings.Contains(commands[2], "-d 'disable_functions=system,shell_exec'") {
		t.Fatalf("expected runtime report, PHP probe, and override check:\n%s", strings.Join(commands, "\n"))
	}
}

func TestDetectRemotePHPFunctionCompatibilityFailsWhenOverrideIsBlocked(t *testing.T) {
	dir := t.TempDir()
	installLoggingFakeSSH(t, dir, fakePHPFunctionSSH(""))
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	_, err := app.detectRemotePHPFunctionCompatibility(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, "pull source")
	if err == nil {
		t.Fatal("detectRemotePHPFunctionCompatibility() accepted an override the host blocks")
	}
	for _, want := range []string{"pull source PHP disables exec", "wp db export", "php -d"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestDetectRemotePHPFunctionCompatibilityFailsWhenPHPCannotBeDetermined(t *testing.T) {
	dir := t.TempDir()
	installLoggingFakeSSH(t, dir, "printf 'php_path=/usr/bin/php\\nwp_cli_entry=/usr/local/bin/wp\\nwp_cli_first_line=#!/bin/bash\\nwp_cli_info_status=255\\n'\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	_, err := app.detectRemotePHPFunctionCompatibility(context.Background(), dir, RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, "pull source")
	if err == nil {
		t.Fatal("an undetermined PHP should stop the pull instead of risking a silent export failure")
	}
	for _, want := range []string{
		"pull source PHP function check failed",
		"exec, proc_open, proc_close, escapeshellarg",
		"could not determine the PHP that runs WP-CLI",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestPreflightRecordsRemotePHPFunctionOverride(t *testing.T) {
	dir := t.TempDir()
	installLoggingFakeSSH(t, dir, fakePHPFunctionSSH("available"))
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}

	err := app.runPreflight(context.Background(), preflightPlan{
		ProjectRoot: dir,
		Remotes: []preflightRemoteNeeds{{
			Label:                "pull source",
			Target:               target,
			NeedPHPFunctionCheck: true,
		}},
	}, Config{})
	if err != nil {
		t.Fatalf("runPreflight() error = %v", err)
	}
	if !app.remotePHPFunctionOverrideFor(target).required() {
		t.Fatal("preflight should record the PHP function override for the pull source")
	}
	if !strings.Contains(stdout.String(), "Pull source PHP function compatibility enabled (exec allowed for WP-CLI db export only)") {
		t.Fatalf("preflight should report the override:\n%s", stdout.String())
	}
}

func TestPreflightChecksPHPFunctionsOnlyForPullSource(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, fakePHPFunctionSSH("available"))
	installFakeCommand(t, dir, "wp", "#!/bin/sh\nexit 0\n")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cfg := Config{PushUser: "deploy", PushHost: "example.com", PushRemotePath: "/var/www/html"}

	if err := app.preflightPush(context.Background(), dir, modeStandalone, cfg, configOptions{SkipFiles: true}); err != nil {
		t.Fatalf("preflightPush() error = %v", err)
	}
	for _, command := range readSSHLog(t, logPath) {
		if strings.Contains(command, "cli info --format=json") || strings.Contains(command, "reenable=") {
			t.Fatalf("push only imports and should not probe PHP functions:\n%s", command)
		}
	}
}

func TestDBPullExportsThroughPHPFunctionOverride(t *testing.T) {
	dir := t.TempDir()
	logPath := installLoggingFakeSSH(t, dir, "")
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	target := Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}.pullTarget()
	override := remotePHPFunctionOverride{remotePHPRuntime: hostingerRuntime, Reenable: []string{"exec"}, DisableFunctions: "system"}
	app.setRemotePHPFunctionOverride(target, override)

	if err := app.dbPull(context.Background(), dir, Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}, true); err != nil {
		t.Fatalf("dbPull() error = %v", err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), override.command()+" --allow-root db export '/tmp/ddev-") {
		t.Fatalf("database export should run through the PHP function override:\n%s", log)
	}
	if strings.Contains(string(log), "wp_ssh_wp --allow-root db export") {
		t.Fatalf("database export should not use the restricted WP-CLI invocation:\n%s", log)
	}
}
