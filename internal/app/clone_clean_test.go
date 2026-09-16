package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyDirExceptRemovesContentButKeepsProtected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "host default")
	writeFile(t, filepath.Join(dir, "wp-content", "plugins", "x.php"), "<?php")
	writeFile(t, filepath.Join(dir, ".git", "config"), "[core]")
	writeFile(t, filepath.Join(dir, ".wp-ssh.yaml"), "cfg")

	keep := map[string]bool{".git": true, ".wp-ssh.yaml": true}
	if err := emptyDirExcept(dir, keep); err != nil {
		t.Fatalf("emptyDirExcept() error = %v", err)
	}

	for _, name := range []string{"index.html", "wp-content"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %q removed, stat err = %v", name, err)
		}
	}
	for _, name := range []string{".git", ".wp-ssh.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %q preserved: %v", name, err)
		}
	}
}

func TestEmptyDirExceptRefusesFilesystemRoot(t *testing.T) {
	if err := emptyDirExcept(string(filepath.Separator), nil); err == nil {
		t.Fatal("expected emptyDirExcept to refuse the filesystem root")
	}
}

func TestEmptyDirExceptRefusesHomeDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Sentinel that must survive because the guard rejects the home directory.
	writeFile(t, filepath.Join(dir, "keepme"), "x")
	if err := emptyDirExcept(dir, nil); err == nil {
		t.Fatal("expected emptyDirExcept to refuse the home directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "keepme")); err != nil {
		t.Errorf("home-directory guard should run before any deletion: %v", err)
	}
}

func TestCloneCleanKeepIncludesToolFilesAndBinary(t *testing.T) {
	keep := cloneCleanKeep("/opt/bin/wp-ssh-bridge")
	for _, name := range []string{".git", ".ddev", ".wp-ssh", ".wp-ssh.yaml", "wp-config-ddev.php", "wp-ssh-bridge"} {
		if !keep[name] {
			t.Errorf("expected %q in the preserved set", name)
		}
	}
	if keep["index.html"] {
		t.Error("did not expect arbitrary content in the preserved set")
	}
}

func TestCleanTargetFlagIsCloneOnly(t *testing.T) {
	opts, err := parseConfigCommand("clone", []string{"--clean-target"}, io.Discard)
	if err != nil {
		t.Fatalf("clone should accept --clean-target: %v", err)
	}
	if !opts.CleanTarget {
		t.Error("expected CleanTarget to be set for clone")
	}

	for _, name := range []string{"pull", "push", "init"} {
		if _, err := parseConfigCommand(name, []string{"--clean-target"}, io.Discard); err == nil {
			t.Errorf("expected %q to reject --clean-target (clone-only)", name)
		}
	}
}

// TestFilesPullCloneRsyncGatesDeleteOnCleanTarget verifies the rsync side of the flag:
// a clone is additive (no --delete) unless --clean-target is set, while --safe-links is
// always present. Normal (non-clone) pulls keep --delete unconditionally, covered by
// TestFilesPullRsyncUsesDeleteAndHideRuleForRecursiveLogs.
func TestFilesPullCloneRsyncGatesDeleteOnCleanTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cleanTarget bool
		wantDelete  bool
	}{
		{"additive without flag", false, false},
		{"clean with flag", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argsPath := filepath.Join(dir, "rsync-args.txt")
			installFakeCommand(t, dir, "rsync", `#!/bin/sh
printf '%s\n' "$@" > `+shellQuote(argsPath)+`
`)
			var stdout, stderr bytes.Buffer
			app := newApp(strings.NewReader(""), &stdout, &stderr)

			err := app.filesPull(context.Background(), dir, Config{
				User:       "deploy",
				Host:       "example.com",
				RemotePath: "/var/www/html",
			}, false, true, tc.cleanTarget, false)
			if err != nil {
				t.Fatalf("filesPull() error = %v\nstderr:\n%s", err, stderr.String())
			}

			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(args), "--delete"); got != tc.wantDelete {
				t.Fatalf("clone rsync --delete present = %v, want %v\nargs:\n%s", got, tc.wantDelete, args)
			}
			if !strings.Contains(string(args), "--safe-links") {
				t.Errorf("expected --safe-links in rsync args regardless of --clean-target:\n%s", args)
			}
		})
	}
}

func TestCleanCloneTargetEmptiesResolvedDestination(t *testing.T) {
	projectRoot := t.TempDir()
	// Destination is a WordPress subdirectory of the project.
	writeFile(t, filepath.Join(projectRoot, "wp", "index.html"), "host default")
	writeFile(t, filepath.Join(projectRoot, "wp", ".git", "config"), "[core]")

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	cfg := Config{LocalWPPath: "wp"}

	if err := app.cleanCloneTarget(projectRoot, cfg); err != nil {
		t.Fatalf("cleanCloneTarget() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(projectRoot, "wp", "index.html")); !os.IsNotExist(err) {
		t.Errorf("expected pre-existing target content removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "wp", ".git")); err != nil {
		t.Errorf("expected .git preserved in the target: %v", err)
	}
}
