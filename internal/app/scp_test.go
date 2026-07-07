package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// --- needsScpTransport ---

func TestNeedsScpTransportReturnsTrueWhenForced(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "rsync", "#!/bin/sh\nexit 0\n")
	installFakeSSH(t, dir, "#!/bin/sh\nexit 0\n")

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	if !app.needsScpTransport(context.Background(), dir, target, true) {
		t.Error("expected needsScpTransport=true when force=true")
	}
	if !strings.Contains(stdout.String(), "--force-scp") {
		t.Errorf("expected --force-scp notice:\n%s", stdout.String())
	}
}

func TestNeedsScpTransportReturnsTrueWhenLocalRsyncMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	if !app.needsScpTransport(context.Background(), dir, target, false) {
		t.Error("expected needsScpTransport=true when local rsync is missing")
	}
	if !strings.Contains(stderr.String(), "Local rsync not available") {
		t.Errorf("expected fallback warning:\n%s", stderr.String())
	}
}

func TestNeedsScpTransportReturnsTrueWhenRemoteRsyncMissing(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "rsync", "#!/bin/sh\nexit 0\n")
	installFakeSSH(t, dir, "#!/bin/sh\nexit 1\n")

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	if !app.needsScpTransport(context.Background(), dir, target, false) {
		t.Error("expected needsScpTransport=true when remote rsync probe fails")
	}
	if !strings.Contains(stderr.String(), "Remote rsync not available") {
		t.Errorf("expected fallback warning:\n%s", stderr.String())
	}
}

func TestNeedsScpTransportReturnsFalseWhenBothAvailable(t *testing.T) {
	dir := t.TempDir()
	installFakeCommand(t, dir, "rsync", "#!/bin/sh\nexit 0\n")
	installFakeSSH(t, dir, "#!/bin/sh\nexit 0\n")

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/var/www/html"}
	if app.needsScpTransport(context.Background(), dir, target, false) {
		t.Error("expected needsScpTransport=false when both rsync instances are available")
	}
}

// --- scpArgs ---

func TestScpArgsIncludesPortWhenConfigured(t *testing.T) {
	args := scpArgs(RemoteTarget{User: "deploy", Host: "example.com", Port: "2222"})
	portIdx := -1
	for i, a := range args {
		if a == "-P" {
			portIdx = i
			break
		}
	}
	if portIdx == -1 || portIdx+1 >= len(args) || args[portIdx+1] != "2222" {
		t.Errorf("scpArgs missing -P 2222 for non-standard port:\n%v", args)
	}
}

func TestScpArgsOmitsPortWhenDefault(t *testing.T) {
	args := scpArgs(RemoteTarget{User: "deploy", Host: "example.com"})
	for _, a := range args {
		if a == "-P" {
			t.Errorf("scpArgs should not include -P when port is empty:\n%v", args)
		}
	}
}

// --- --force-scp flag ---

func TestForceScpFlag(t *testing.T) {
	opts, err := parseConfigCommand("pull", []string{"--force-scp"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfigCommand() error = %v", err)
	}
	if !opts.ForceScpTransport {
		t.Error("expected ForceScpTransport=true")
	}
}
