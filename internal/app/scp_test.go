package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
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
	// PATH with no rsync
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
	// SSH always fails (simulates remote without rsync)
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
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a small tar.gz archive to serve as the "remote" output.
	archivePath := filepath.Join(dir, "test.tar.gz")
	if err := writeTarGz(archivePath, map[string]string{
		"wp-config.php":        "<?php // config\n",
		"wp-content/index.php": "<?php // silence\n",
	}); err != nil {
		t.Fatal(err)
	}

	// Install a fake SSH that cats the archive to stdout (simulates remote tar).
	installFakeSSH(t, dir, `#!/bin/sh
cat `+shellQuote(archivePath)+`
`)

	var stdout, stderr bytes.Buffer
	app := newApp(strings.NewReader(""), &stdout, &stderr)

	target := RemoteTarget{User: "deploy", Host: "example.com", RemotePath: src}
	if err := app.tarPipeFromRemote(context.Background(), dir, target, "tar -czf - -C /remote .", dst); err != nil {
		t.Fatalf("tarPipeFromRemote() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	for _, name := range []string{"wp-config.php", filepath.Join("wp-content", "index.php")} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Errorf("expected extracted file %s: %v", name, err)
		}
	}
}

// --- tarPipeToRemote ---

func TestTarPipeToRemoteStreamsArchiveToSSH(t *testing.T) {
	dir := t.TempDir()

	// Create a source directory with a file.
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "wp-config.php"), []byte("<?php // config\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install a fake SSH that reads stdin into a file so we can verify the archive arrived.
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

	// Verify the received file is a valid tar.gz containing wp-config.php.
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
