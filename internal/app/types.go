package app

import (
	"io"
	"os"
)

const (
	defaultProviderName = "wp-ssh"
	configFileName      = "wp-ssh.yaml"
	hookConfigName      = "config.wp-ssh.yaml"
	binaryName          = "wp-ssh-bridge"
)

// App carries process dependencies so command handlers can share IO and cwd state.
type App struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	WorkDir string
	UI      *CLIUI
}

// newApp builds the runtime wrapper used by main and tests.
func newApp(stdin io.Reader, stdout io.Writer, stderr io.Writer) *App {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}

	return &App{
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
		WorkDir: wd,
		UI:      NewCLIUI(stdout, stderr),
	}
}
