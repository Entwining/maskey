// Package runner runs a command with credentials in its environment and
// masks their values in its output.
//
// The runner stays the command's parent for its whole life: it drains the
// command's stdout and stderr through separate redactors, forwards
// interruptions, and reports the command's exit. Running the command in
// place of the runner, or masking afterwards, would hand the raw streams to
// whoever reads them.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Entwining/maskey/internal/redact"
	"github.com/Entwining/maskey/internal/store"
)

// Exit statuses of runner failures, following env(1).
const (
	ExitFailure       = 125 // the runner failed, before or after starting the command
	ExitNotExecutable = 126
	ExitNotFound      = 127
)

// Grace periods for cleanup. Processes started by the command can inherit
// its output pipes and keep them open after the command exits; the runner
// stops reading after outputGrace instead of waiting for them. A command
// asked to stop after an output failure is killed after killGrace.
var (
	outputGrace = 2 * time.Second
	killGrace   = 2 * time.Second
)

// Command describes what to run.
type Command struct {
	Path    string
	Args    []string
	Entries []store.Entry
	Stdin   *os.File
	Stdout  io.Writer
	Stderr  io.Writer
}

// Exit is how the command ended: an exit code, or the signal that ended
// it.
type Exit struct {
	Code   int
	Signal syscall.Signal
}

// Error is a runner failure; Code is the status to exit with.
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Run starts the command and waits for it. It returns an *Error when the
// command could not start or its output could not be passed on whole; the
// command is stopped in the latter case even if it would have succeeded.
// It returns ctx's error, without starting the command, if ctx ended first;
// once the command runs, interruptions reach it as signals instead.
func Run(ctx context.Context, c Command) (Exit, error) {
	values := make([][]byte, len(c.Entries))
	for i, entry := range c.Entries {
		values[i] = entry.Value
	}
	stdout, err := redact.NewWriter(c.Stdout, values)
	if err != nil {
		return Exit{}, &Error{Code: ExitFailure, Message: err.Error()}
	}
	stderr, _ := redact.NewWriter(c.Stderr, values)

	path, err := exec.LookPath(c.Path)
	if err != nil {
		return Exit{}, execError(err, ExitNotFound)
	}

	// Sharing the terminal's foreground process group lets the command read
	// the terminal and receive Ctrl-C and Ctrl-Z directly. Otherwise the
	// command gets a group of its own, so interruptions and cleanup reach
	// the processes it starts without touching the runner's caller.
	ownGroup := !foreground(c.Stdin)
	cmd := exec.Command(path, c.Args...)
	cmd.Args[0] = c.Path
	// exec keeps the last value of a duplicate key, so entries replace
	// inherited variables of the same names.
	cmd.Env = os.Environ()
	for _, entry := range c.Entries {
		cmd.Env = append(cmd.Env, entry.Name+"="+string(entry.Value))
	}
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: ownGroup}
	cmd.WaitDelay = outputGrace

	var mu sync.Mutex
	var outputErr error
	var killTimer *time.Timer
	stop := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if outputErr != nil {
			return
		}
		outputErr = err
		signalCommand(cmd, ownGroup, syscall.SIGTERM)
		killTimer = time.AfterFunc(killGrace, func() { signalCommand(cmd, ownGroup, syscall.SIGKILL) })
	}
	cmd.Stdout = &stoppingWriter{w: stdout, stop: stop}
	cmd.Stderr = &stoppingWriter{w: stderr, stop: stop}

	interrupts := make(chan os.Signal, 4)
	signal.Notify(interrupts, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(interrupts)
	// Without this, a write to os.Stdout or os.Stderr whose reader went away
	// ends the process by SIGPIPE before stop can end the command; with it,
	// the write fails like any other output failure.
	brokenPipes := make(chan os.Signal, 1)
	signal.Notify(brokenPipes, syscall.SIGPIPE)
	defer signal.Stop(brokenPipes)
	// An interruption before interrupts was registered ended only ctx.
	if err := ctx.Err(); err != nil {
		return Exit{}, err
	}

	if err := cmd.Start(); err != nil {
		return Exit{}, execError(err, ExitFailure)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-interrupts:
				// The terminal already delivered Ctrl-C and Ctrl-\ to a command
				// in its foreground group.
				if ownGroup || (sig != syscall.SIGINT && sig != syscall.SIGQUIT) {
					signalCommand(cmd, ownGroup, sig.(syscall.Signal))
				}
			case <-done:
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	mu.Lock()
	failure := outputErr
	if killTimer != nil {
		killTimer.Stop()
	}
	mu.Unlock()
	if failure == nil {
		for _, w := range []*redact.Writer{stdout, stderr} {
			if failure = w.Close(); failure != nil {
				break
			}
		}
	}
	if failure != nil {
		if ownGroup {
			// Processes the command started may still run in its group.
			signalCommand(cmd, true, syscall.SIGKILL)
		}
		return Exit{}, &Error{Code: ExitFailure, Message: "passing on the command's output failed, so the command was stopped: " + failure.Error()}
	}
	if cmd.ProcessState == nil {
		// Waiting itself failed, so the command's status is unknown.
		return Exit{}, &Error{Code: ExitFailure, Message: fmt.Sprintf("waiting for the command: %v", waitErr)}
	}
	status := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return Exit{Signal: status.Signal()}, nil
	}
	// errors.Is(waitErr, exec.ErrWaitDelay): processes the command started
	// still hold its output pipes. Their later output is dropped, which
	// exposes nothing, and the command's own status stands.
	return Exit{Code: status.ExitStatus()}, nil
}

// execError classifies a failure to find or start the command: 127 when
// nothing is there, 126 when something is but cannot run.
func execError(err error, otherwise int) *Error {
	code := otherwise
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, os.ErrNotExist):
		code = ExitNotFound
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EISDIR), errors.Is(err, syscall.ENOEXEC):
		code = ExitNotExecutable
	}
	return &Error{Code: code, Message: err.Error()}
}

func foreground(stdin *os.File) bool {
	if stdin == nil {
		return false
	}
	group, err := unix.IoctlGetInt(int(stdin.Fd()), unix.TIOCGPGRP)
	return err == nil && group == unix.Getpgrp()
}

func signalCommand(cmd *exec.Cmd, ownGroup bool, sig syscall.Signal) {
	pid := cmd.Process.Pid
	if ownGroup {
		pid = -pid
	}
	_ = syscall.Kill(pid, sig)
}

// stoppingWriter stops the command when its output cannot be passed on,
// instead of letting it run on with nobody reading.
type stoppingWriter struct {
	w    io.Writer
	stop func(error)
}

func (s *stoppingWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		s.stop(err)
	}
	return n, err
}
