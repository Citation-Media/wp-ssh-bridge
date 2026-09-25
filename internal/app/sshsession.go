package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// sshControlPersist bounds how long a shared SSH connection outlives its last command.
// The CLI closes shared connections itself when it exits; the timeout only reaps a
// connection left behind when the process is killed before it can clean up.
const sshControlPersist = "10m"

// sshSessionCloseTimeout bounds how long exit waits for one shared connection to close.
const sshSessionCloseTimeout = 5 * time.Second

// sshSessionPool shares one authenticated OpenSSH connection (ControlMaster) per SSH
// login for the lifetime of one CLI process, so preflight checks and transfers reuse
// it instead of opening and authenticating a new connection for every command. A nil
// pool disables sharing.
type sshSessionPool struct {
	mu      sync.Mutex
	dir     string
	masters map[string]sshMaster
	closed  bool
}

// sshMaster is an open shared connection and the control socket that reaches it.
type sshMaster struct {
	target      RemoteTarget
	controlPath string
}

// openSSHSession authenticates once and keeps the connection open in the background
// for later commands to the same login. It does nothing without a pool or when the
// login already has a shared connection.
func (a *App) openSSHSession(ctx context.Context, projectRoot string, target RemoteTarget) error {
	pool := a.sshSessions
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()

	key := sshSessionKey(target)
	if _, open := pool.masters[key]; open || pool.closed {
		return nil
	}
	if pool.dir == "" {
		// Unix socket paths are limited to about 100 bytes and ssh appends a random
		// suffix while it creates the socket, so a long per-user TMPDIR is too deep.
		dir, err := os.MkdirTemp("/tmp", "wpssh-")
		if err != nil {
			// Without a socket directory every command keeps its own connection.
			return nil
		}
		pool.dir = dir
	}
	stderrFile, err := os.CreateTemp(pool.dir, "master-*.log")
	if err != nil {
		return nil
	}
	defer os.Remove(stderrFile.Name())
	defer stderrFile.Close()

	controlPath := filepath.Join(pool.dir, fmt.Sprintf("%d.sock", len(pool.masters)))
	args := plainSSHArgv(target,
		"-o", "ControlMaster=yes",
		"-o", "ControlPath="+controlPath,
		"-o", "ControlPersist="+sshControlPersist,
		"-N", "-f",
		sshTarget(target),
	)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	// The backgrounded master inherits these descriptors and outlives this command. A
	// file instead of a pipe keeps Wait from blocking until the master exits.
	cmd.Stderr = stderrFile
	if err := cmd.Run(); err != nil {
		if details, readErr := os.ReadFile(stderrFile.Name()); readErr == nil {
			a.writeCapturedOutput("remote", string(details), true)
		}
		return err
	}
	if pool.masters == nil {
		pool.masters = map[string]sshMaster{}
	}
	pool.masters[key] = sshMaster{target: target, controlPath: controlPath}
	return nil
}

// sshSessionArgs routes a command through the login's shared connection when one is
// open. If that connection has gone away, ssh falls back to a connection of its own.
func (a *App) sshSessionArgs(target RemoteTarget) []string {
	pool := a.sshSessions
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	master, open := pool.masters[sshSessionKey(target)]
	if !open || pool.closed {
		return nil
	}
	return []string{"-o", "ControlMaster=no", "-o", "ControlPath=" + master.controlPath}
}

// closeSSHSessions closes every shared connection and removes their sockets. It runs
// when the CLI exits, whether the command succeeded, failed, or was interrupted.
func (a *App) closeSSHSessions() {
	pool := a.sshSessions
	if pool == nil {
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return
	}
	pool.closed = true
	for _, master := range pool.masters {
		ctx, cancel := context.WithTimeout(context.Background(), sshSessionCloseTimeout)
		args := plainSSHArgv(master.target, "-o", "ControlPath="+master.controlPath, "-O", "exit", sshTarget(master.target))
		_ = exec.CommandContext(ctx, args[0], args[1:]...).Run()
		cancel()
	}
	if pool.dir != "" {
		_ = os.RemoveAll(pool.dir)
	}
}

// closeSSHSessionsOnSignal closes shared connections when the CLI is interrupted or
// terminated, then lets the signal end the process as it would have without the
// handler, so a calling shell script still sees the interruption. Shared connections
// run detached and would otherwise outlive the process until ControlPersist expires.
func (a *App) closeSSHSessionsOnSignal() (stop func()) {
	signals := make(chan os.Signal, 1)
	handled := []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
	signal.Notify(signals, handled...)
	done := make(chan struct{})
	go func() {
		select {
		case received := <-signals:
			a.closeSSHSessions()
			signal.Reset(handled...)
			if self, err := os.FindProcess(os.Getpid()); err == nil && self.Signal(received) == nil {
				// The signal arrives asynchronously; give it time to end the process.
				time.Sleep(time.Second)
			}
			if number, ok := received.(syscall.Signal); ok {
				os.Exit(128 + int(number))
			}
			os.Exit(1)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// sshSessionKey identifies one SSH login; targets on the same login share a connection.
func sshSessionKey(target RemoteTarget) string {
	return target.address() + ":" + target.port()
}
