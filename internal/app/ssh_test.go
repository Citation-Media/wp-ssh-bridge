package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireSSHAgentSuggestsDDEVAuthSSHInDDEVProjects(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".ddev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ddev", "config.yaml"), []byte("type: wordpress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := newApp(strings.NewReader(""), os.Stdout, os.Stderr)

	err := app.requireSSHAgent(context.Background(), dir)
	if err == nil {
		t.Skip("local test environment already has an SSH key loaded")
	}
	if !strings.Contains(err.Error(), "ddev auth ssh") {
		t.Fatalf("error missing DDEV SSH guidance: %v", err)
	}
}
