package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableRemoteMaintenanceModeCallsActivate(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ssh-args.txt")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsFile)+`
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	if _, err := app.enableRemoteMaintenanceMode(context.Background(), dir, target); err != nil {
		t.Fatalf("enableRemoteMaintenanceMode() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsBytes)
	for _, want := range []string{"maintenance-mode", "activate"} {
		if !strings.Contains(args, want) {
			t.Errorf("SSH args missing %q:\n%s", want, args)
		}
	}
	if !strings.Contains(stdout.String(), "✓ Remote maintenance mode enabled") {
		t.Errorf("missing success message:\n%s", stdout.String())
	}
}

func TestDisableRemoteMaintenanceModeCallsDeactivate(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "ssh-args.txt")
	installFakeSSH(t, dir, `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsFile)+`
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	app.disableRemoteMaintenanceMode(context.Background(), dir, target)

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsBytes)
	for _, want := range []string{"maintenance-mode", "deactivate"} {
		if !strings.Contains(args, want) {
			t.Errorf("SSH args missing %q:\n%s", want, args)
		}
	}
	if !strings.Contains(stdout.String(), "✓ Remote maintenance mode disabled") {
		t.Errorf("missing success message:\n%s", stdout.String())
	}
}

func TestDisableRemoteMaintenanceModeWarnsOnFailure(t *testing.T) {
	dir := t.TempDir()
	installFakeSSH(t, dir, `#!/bin/sh
exit 1
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	app.disableRemoteMaintenanceMode(context.Background(), dir, target)

	if !strings.Contains(stderr.String(), "Could not disable remote maintenance mode") {
		t.Errorf("expected warning in stderr:\n%s", stderr.String())
	}
}

func TestEnableLocalMaintenanceModeCallsActivate(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "wp-args.txt")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsFile)+`
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{LocalWPPath: "."}
	if _, err := app.enableLocalMaintenanceMode(context.Background(), dir, cfg); err != nil {
		t.Fatalf("enableLocalMaintenanceMode() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsBytes)
	for _, want := range []string{"maintenance-mode", "activate"} {
		if !strings.Contains(args, want) {
			t.Errorf("wp args missing %q:\n%s", want, args)
		}
	}
	if !strings.Contains(stdout.String(), "✓ Local maintenance mode enabled") {
		t.Errorf("missing success message:\n%s", stdout.String())
	}
}

func TestDisableLocalMaintenanceModeCallsDeactivate(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "wp-args.txt")
	installFakeCommand(t, dir, "wp", `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsFile)+`
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{LocalWPPath: "."}
	app.disableLocalMaintenanceMode(context.Background(), dir, cfg)

	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(argsBytes)
	for _, want := range []string{"maintenance-mode", "deactivate"} {
		if !strings.Contains(args, want) {
			t.Errorf("wp args missing %q:\n%s", want, args)
		}
	}
	if !strings.Contains(stdout.String(), "✓ Local maintenance mode disabled") {
		t.Errorf("missing success message:\n%s", stdout.String())
	}
}

func TestDisableLocalMaintenanceModeWarnsOnFailure(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "wp", `#!/bin/sh
exit 1
`)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	cfg := Config{LocalWPPath: "."}
	app.disableLocalMaintenanceMode(context.Background(), dir, cfg)

	if !strings.Contains(stderr.String(), "Could not disable local maintenance mode") {
		t.Errorf("expected warning in stderr:\n%s", stderr.String())
	}
}

func TestSkipMaintenanceModeFlag(t *testing.T) {
	opts, err := parseConfigCommand("push", []string{"--skip-maintenance-mode"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfigCommand() error = %v", err)
	}
	if !opts.SkipMaintenanceMode {
		t.Error("expected SkipMaintenanceMode = true")
	}
}

func TestEnableLocalMaintenanceModeSkipsSiteThatIsNotInstalled(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "wp.log")
	installFakeCommand(t, dir, "wp", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\ncase \"$*\" in *'core is-installed'*) exit 1 ;; esac\n")
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

	enabled, err := app.enableLocalMaintenanceMode(context.Background(), dir, Config{LocalWPPath: "."})
	if err != nil || enabled {
		t.Fatalf("enableLocalMaintenanceMode() = (%v, %v), want skipped without error", enabled, err)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "maintenance-mode") {
		t.Fatalf("maintenance mode must not be activated without an installed site:\n%s", log)
	}
	if !strings.Contains(stdout.String(), "Skipping local maintenance mode: no installed WordPress site at the destination yet") {
		t.Fatalf("missing skip notice:\n%s", stdout.String())
	}
}

func TestEnableRemoteMaintenanceModeSkipsSiteThatIsNotInstalled(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ssh.log")
	installFakeSSH(t, dir, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(logPath)+"\ncase \"$*\" in *is-installed*) exit 1 ;; esac\n")
	stdout := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &bytes.Buffer{})

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	enabled, err := app.enableRemoteMaintenanceMode(context.Background(), dir, target)
	if err != nil || enabled {
		t.Fatalf("enableRemoteMaintenanceMode() = (%v, %v), want skipped without error", enabled, err)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "maintenance-mode") {
		t.Fatalf("maintenance mode must not be activated on an empty push target:\n%s", log)
	}
	if !strings.Contains(stdout.String(), "Skipping remote maintenance mode: no installed WordPress site on the push target yet") {
		t.Fatalf("missing skip notice:\n%s", stdout.String())
	}
}

// runCloneIntoEmptyDestination drives the clone pipeline against fake ssh and wp: the
// export arrives through ssh cat, and wp reports no installed site at the destination.
func runCloneIntoEmptyDestination(t *testing.T, cleanTarget bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	dump := filepath.Join(dir, "export.sql.gz")
	var gz bytes.Buffer
	writer := gzip.NewWriter(&gz)
	_, _ = writer.Write([]byte("CREATE TABLE wp_options (option_id int);\n"))
	_ = writer.Close()
	if err := os.WriteFile(dump, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	installFakeSSH(t, dir, `#!/bin/sh
case "$*" in
  *"cli info --format=json"*) printf '{"php_binary_path":"/usr/bin/php","wp_cli_phar_path":"phar:///usr/local/bin/wp"}\n' ;;
  *reenable=*) printf 'reenable=\n' ;;
  *" cat "*) cat `+shellQuote(dump)+` ;;
esac
exit 0
`)
	wpLog := filepath.Join(dir, "wp.log")
	// Like WP-CLI on an empty database: nothing is installed, so activation fails.
	installFakeCommand(t, dir, "wp", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(wpLog)+"\ncase \"$*\" in\n  *'core is-installed'*) exit 1 ;;\n  *'maintenance-mode activate'*) echo 'Error: The site you have requested is not installed.' >&2; exit 1 ;;\nesac\n")

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	adapter := standaloneAdapter{runtime: runtimeContext{Mode: modeStandalone, Root: dir}}
	cfg := Config{User: "deploy", Host: "example.com", RemotePath: "/var/www/html", LocalWPPath: "."}
	opts := configOptions{Clone: true, CleanTarget: cleanTarget, SkipFiles: true, ForceScpTransport: true, Silent: true, Yes: true}
	if err := app.runPullPipeline(context.Background(), adapter, cfg, opts); err != nil {
		t.Fatalf("clone into an empty destination failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	log, err := os.ReadFile(wpLog)
	if err != nil {
		t.Fatal(err)
	}
	return string(log), stdout.String()
}

func TestCloneIntoDestinationWithoutInstalledSiteImportsWithoutMaintenanceMode(t *testing.T) {
	wpLog, stdout := runCloneIntoEmptyDestination(t, false)
	if !strings.Contains(wpLog, "core is-installed") || strings.Contains(wpLog, "maintenance-mode") {
		t.Fatalf("clone should check the destination and skip maintenance mode:\n%s", wpLog)
	}
	if !strings.Contains(wpLog, "db import") {
		t.Fatalf("clone should still import the database:\n%s", wpLog)
	}
	if !strings.Contains(stdout, "Skipping local maintenance mode: no installed WordPress site at the destination yet") {
		t.Fatalf("missing skip notice:\n%s", stdout)
	}
}

func TestCloneWithCleanTargetSkipsMaintenanceModeWithoutChecking(t *testing.T) {
	wpLog, stdout := runCloneIntoEmptyDestination(t, true)
	if strings.Contains(wpLog, "core is-installed") || strings.Contains(wpLog, "maintenance-mode") {
		t.Fatalf("--clean-target should skip maintenance mode outright:\n%s", wpLog)
	}
	if !strings.Contains(wpLog, "db import") {
		t.Fatalf("clone should still import the database:\n%s", wpLog)
	}
	if !strings.Contains(stdout, "Skipping local maintenance mode: --clean-target replaces the destination site") {
		t.Fatalf("missing skip notice:\n%s", stdout)
	}
}
