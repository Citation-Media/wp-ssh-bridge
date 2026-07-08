package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// incompressibleBytes returns n deterministic pseudo-random bytes that gzip cannot
// meaningfully compress, so a streamed archive exceeds the OS pipe buffer and a
// stalled consumer/producer would block on write (surfacing a pipe deadlock).
func incompressibleBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.New(rand.NewSource(1)).Read(b)
	return b
}

// --- buildTarExcludeArgs ---

func TestBuildTarExcludeArgsStripsTrailingSlashes(t *testing.T) {
	patterns := []string{".git/", ".ddev/", "wp-config.php"}
	args := buildTarExcludeArgs(patterns)
	want := []string{"--exclude=.git", "--exclude=.ddev", "--exclude=wp-config.php"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i, a := range args {
		if a != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, want[i])
		}
	}
}

// --- tarPipeFromRemote ---

func TestTarPipeFromRemoteExtractsFiles(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "test.tar.gz")
	if err := writeTarGz(archivePath, map[string]string{
		"wp-config.php":        "<?php // config\n",
		"wp-content/index.php": "<?php // silence\n",
	}); err != nil {
		t.Fatal(err)
	}

	// Fake SSH cats the archive to stdout (simulates remote tar).
	installFakeSSH(t, dir, `#!/bin/sh
cat `+shellQuote(archivePath)+`
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/remote/wp"}
	if err := app.tarPipeFromRemote(context.Background(), dir, target, "tar -czf - -C /remote/wp .", dst); err != nil {
		t.Fatalf("tarPipeFromRemote() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	for _, name := range []string{"wp-config.php", filepath.Join("wp-content", "index.php")} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("expected extracted file %s: %v", name, err)
		}
	}
}

// TestTarPipeFromRemoteToleratesRemoteTarWarning verifies that a remote tar warning
// (exit status 1, "file changed as we read it" on a live site) does not fail a pull
// whose local extract completed — mirroring the rsync transport's exit-24 tolerance.
func TestTarPipeFromRemoteToleratesRemoteTarWarning(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(dir, "test.tar.gz")
	if err := writeTarGz(archivePath, map[string]string{"wp-config.php": "<?php // config\n"}); err != nil {
		t.Fatal(err)
	}

	// Fake SSH streams a complete, valid archive but exits 1, as remote tar does when a
	// file changes while being read.
	installFakeSSH(t, dir, `#!/bin/sh
cat `+shellQuote(archivePath)+`
exit 1
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/remote/wp"}
	if err := app.tarPipeFromRemote(context.Background(), dir, target, "tar -czf - -C /remote/wp .", dst); err != nil {
		t.Fatalf("tarPipeFromRemote() should tolerate a remote tar warning, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wp-config.php")); err != nil {
		t.Errorf("expected extracted file despite the warning: %v", err)
	}
}

// --- tarPipeToRemote ---

func TestTarPipeToRemoteStreamsArchiveToSSH(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "wp-config.php"), []byte("<?php // config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake SSH saves stdin to a file so we can verify the archive arrived.
	receivedPath := filepath.Join(dir, "received.tar.gz")
	installFakeSSH(t, dir, `#!/bin/sh
cat - > `+shellQuote(receivedPath)+`
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/remote/wp"}
	localTarArgs := []string{"-czf", "-", "-C", src, "."}
	if err := app.tarPipeToRemote(context.Background(), dir, target, localTarArgs, "cat - > /dev/null"); err != nil {
		t.Fatalf("tarPipeToRemote() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	f, err := os.Open(receivedPath)
	if err != nil {
		t.Fatalf("expected received archive: %v", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("received file is not gzip: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if strings.Contains(hdr.Name, "wp-config.php") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected wp-config.php in received archive")
	}
}

// TestTarPipeToRemoteDoesNotHangWhenRemoteExitsEarly guards against a pipe deadlock:
// if the remote extract exits before draining stdin, the local tar must not block
// forever writing to a pipe with no reader.
func TestTarPipeToRemoteDoesNotHangWhenRemoteExitsEarly(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "big.bin"), incompressibleBytes(256*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fake SSH exits with an error without reading stdin, closing the read end early.
	installFakeSSH(t, dir, `#!/bin/sh
echo "remote extract failed" >&2
exit 1
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/remote/wp"}
	localTarArgs := []string{"-czf", "-", "-C", src, "."}

	err := runWithinNoHang(t, 30*time.Second, "tarPipeToRemote() (remote exited early)", func() error {
		return app.tarPipeToRemote(t.Context(), dir, target, localTarArgs, "tar -xzf - -C /remote/wp")
	})
	if err == nil {
		t.Fatal("tarPipeToRemote() returned nil despite an early remote failure")
	}
	// The remote extract is the consumer, so its failure (with captured stderr) must win
	// over the local tar's resulting broken pipe.
	if !strings.Contains(err.Error(), "remote extract failed") {
		t.Errorf("expected the remote stderr to be surfaced, got: %v", err)
	}
}

// TestTarPipeFromRemoteDoesNotHangWhenExtractFails guards the mirror case: if the
// local extract exits early, the remote (ssh) producer must not block forever
// writing to a full pipe with no reader.
func TestTarPipeFromRemoteDoesNotHangWhenExtractFails(t *testing.T) {
	dir := t.TempDir()

	archivePath := filepath.Join(dir, "big.tar.gz")
	if err := writeTarGz(archivePath, map[string]string{"big.bin": string(incompressibleBytes(256 * 1024))}); err != nil {
		t.Fatal(err)
	}
	installFakeSSH(t, dir, `#!/bin/sh
cat `+shellQuote(archivePath)+`
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)
	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: "/remote/wp"}
	// Missing destination makes the local `tar -xzf - -C <missing>` fail immediately.
	missingDest := filepath.Join(dir, "does-not-exist")

	err := runWithinNoHang(t, 30*time.Second, "tarPipeFromRemote() (local extract failed)", func() error {
		return app.tarPipeFromRemote(t.Context(), dir, target, "tar -czf - -C /remote/wp .", missingDest)
	})
	if err == nil {
		t.Fatal("tarPipeFromRemote() returned nil despite a failing local extract")
	}
	// The failing consumer here is the local extract; it must be reported as the cause
	// rather than the ssh side's resulting broken pipe.
	if !strings.Contains(err.Error(), "local extract") {
		t.Errorf("expected the local extract failure to be reported, got: %v", err)
	}
}

// runWithinNoHang runs fn in a goroutine and returns its error, failing the test if fn
// does not return within timeout — a pipe-deadlock regression. fn should be driven by a
// cancelable context (e.g. t.Context()) so a hung run is torn down at test cleanup rather
// than leaking its child processes.
func runWithinNoHang(t *testing.T, timeout time.Duration, what string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("%s hung (deadlock regression)", what)
		return nil
	}
}

// writeTarGz creates a gzipped tar archive at path containing the given name→content entries.
func writeTarGz(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			return err
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}
