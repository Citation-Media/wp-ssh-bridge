package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCheckRequiredLocalCommandReportsMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	err := app.checkRequiredLocalCommand("rsync", "rsync is required for file transfer")
	if err == nil {
		t.Fatal("checkRequiredLocalCommand() accepted missing rsync")
	}
	if !strings.Contains(err.Error(), "rsync is not on PATH") {
		t.Fatalf("error should name missing command, got: %v", err)
	}
	if !strings.Contains(stderr.String(), "Checking local rsync failed") {
		t.Fatalf("stderr should include failed preflight step, got:\n%s", stderr.String())
	}
}

func TestRemotePreflightCommandReflectsPullSourceRequirements(t *testing.T) {
	command := remotePreflightCommand(preflightRemoteNeeds{
		Target: RemoteTarget{
			RemotePath:   "/var/www/html",
			RemoteTmpDir: "/tmp/wp-ssh",
		},
		NeedPathReadable: true,
		NeedTmpWritable:  true,
	})

	for _, want := range []string{
		"WP_SSH_REMOTE_PATH='/var/www/html'",
		"WP_SSH_REMOTE_TMP='/tmp/wp-ssh'",
		"remote WordPress path is not readable",
		"remote temporary directory is not writable",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote preflight command missing %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "mkdir -p \"$WP_SSH_REMOTE_PATH\"") {
		t.Fatalf("pull source preflight should not create remote WordPress path:\n%s", command)
	}
}

func TestRemotePreflightCommandAllowsCreateForFilesOnlyPushTarget(t *testing.T) {
	command := remotePreflightCommand(preflightRemoteNeeds{
		Target: RemoteTarget{
			RemotePath: "/var/www/html",
		},
		NeedPathWritable: true,
		AllowCreatePath:  true,
	})

	for _, want := range []string{
		"mkdir -p \"$WP_SSH_REMOTE_PATH\"",
		"remote WordPress path is not writable",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote preflight command missing %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "remote temporary directory is not writable") {
		t.Fatalf("files-only push should not check remote tmp directory:\n%s", command)
	}
}

func TestPreflightPullValidatesSourceConfigBeforeLocalTools(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	adapter := standaloneAdapter{runtime: runtimeContext{Mode: modeStandalone, Root: t.TempDir()}}

	err := app.preflightPull(context.Background(), adapter, Config{}, configOptions{})
	if err == nil {
		t.Fatal("preflightPull() accepted missing source config")
	}
	if !strings.Contains(err.Error(), "missing SSH user for pull source") {
		t.Fatalf("preflightPull() error = %v", err)
	}
}
