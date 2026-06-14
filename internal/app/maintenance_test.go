package app

import (
	"bytes"
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
	if err := app.enableRemoteMaintenanceMode(context.Background(), dir, target); err != nil {
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
	if err := app.enableLocalMaintenanceMode(context.Background(), dir, cfg); err != nil {
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
