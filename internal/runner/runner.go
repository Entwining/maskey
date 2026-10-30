// Package runner runs a command with credentials in its environment and
// masks their values in its output.
//
// The runner stays between the command and its output for the command's
// whole life: it passes the output on through redactors, forwards
// interruptions, and reports the command's exit. Running the command in
// place of the runner, or masking afterwards, would hand the raw streams to
// whoever reads them. In the foreground of a terminal the command gets a
// terminal of its own, whose output the runner masks on its way to the real
// one, and a monitor on that terminal is the command's parent. Otherwise the
// runner is, and the command's stdout and stderr are pipes.
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
	cmd := exec.Command(path, c.Args...)
	cmd.Args[0] = c.Path
	// exec keeps the last value of a duplicate key, so entries replace
	// inherited variables of the same names.
	cmd.Env = os.Environ()
	for _, entry := range c.Entries {
		cmd.Env = append(cmd.Env, entry.Name+"="+string(entry.Value))
	}

	j := &job{started: make(chan struct{})}
	interrupts := make(chan os.Signal, 4)
	// SIGTSTP stays out: once it is notified, Go's runtime on Darwin never
	// lets it stop the process again, and in terminal mode the runner stops
	// itself with the signal that stopped the command, SIGTSTP for Ctrl-Z.
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

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-j.started:
		case <-done:
			return
		}
		for {
			select {
			case sig := <-interrupts:
				// The terminal already delivered Ctrl-C and Ctrl-\ to a command
				// in its foreground group.
				if j.group || (sig != syscall.SIGINT && sig != syscall.SIGQUIT) {
					j.signal(sig.(syscall.Signal))
				}
			case <-done:
				return
			}
		}
	}()

	out := &stoppingWriter{w: stdout, stop: j.stop}
	errOut := &stoppingWriter{w: stderr, stop: j.stop}
	var status syscall.WaitStatus
	if interactive(c, values) {
		status, err = runTerminal(cmd, c, j, out, errOut)
	} else {
		status, err = runPipes(cmd, c.Stdin, j, out, errOut)
	}

	failure := j.finish()
	if failure == nil {
		for _, w := range []*redact.Writer{stdout, stderr} {
			if failure = w.Close(); failure != nil {
				break
			}
		}
	}
	if failure != nil {
		if j.group {
			// Processes the command started may still run in its group.
			j.signal(syscall.SIGKILL)
		}
		return Exit{}, &Error{Code: ExitFailure, Message: "passing on the command's output failed, so the command was stopped: " + failure.Error()}
	}
	if err != nil {
		return Exit{}, err
	}
	if status.Signaled() {
		return Exit{Signal: status.Signal()}, nil
	}
	return Exit{Code: status.ExitStatus()}, nil
}

// runPipes runs cmd with the runner's stdin and its output through pipes.
func runPipes(cmd *exec.Cmd, stdin *os.File, j *job, stdout, stderr io.Writer) (syscall.WaitStatus, error) {
	// Sharing the terminal's foreground process group lets the command read
	// the terminal and receive Ctrl-C and Ctrl-Z directly. Otherwise the
	// command gets a group of its own, so interruptions and cleanup reach
	// the processes it starts without touching the runner's caller.
	j.group = !foreground(stdin)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: j.group}
	cmd.WaitDelay = outputGrace
	if err := cmd.Start(); err != nil {
		return 0, execError(err, ExitFailure)
	}
	j.start(cmd.Process.Pid)
	waitErr := cmd.Wait()
	if cmd.ProcessState == nil {
		// Waiting itself failed, so the command's status is unknown.
		return 0, &Error{Code: ExitFailure, Message: fmt.Sprintf("waiting for the command: %v", waitErr)}
	}
	// errors.Is(waitErr, exec.ErrWaitDelay): processes the command started
	// still hold its output pipes. Their later output is dropped, which
	// exposes nothing, and the command's own status stands.
	return cmd.ProcessState.Sys().(syscall.WaitStatus), nil
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

// foreground reports whether f is the runner's controlling terminal, by
// whatever name, and the runner is in its foreground process group.
func foreground(f *os.File) bool {
	if f == nil {
		return false
	}
	group, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
	return err == nil && group == unix.Getpgrp()
}

// job is the started command as signals reach it: its process group when
// it has one of its own, otherwise the process alone.
type job struct {
	group   bool
	started chan struct{} // closed once the command runs

	mu      sync.Mutex
	pid     int
	failure error
	kill    *time.Timer
}

func (j *job) start(pid int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pid = pid
	close(j.started)
	if j.failure != nil {
		j.terminate()
	}
}

func (j *job) signal(sig syscall.Signal) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.send(sig)
}

func (j *job) send(sig syscall.Signal) {
	// Before the command runs there is nobody to signal, and kill(0)
	// would reach the runner's own group.
	if j.pid == 0 {
		return
	}
	pid := j.pid
	if j.group {
		pid = -pid
	}
	_ = syscall.Kill(pid, sig)
}

// stop records the first output failure and stops the command, politely
// and then by force.
func (j *job) stop(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failure != nil {
		return
	}
	j.failure = err
	if j.pid != 0 {
		j.terminate()
	}
}

func (j *job) terminate() {
	j.send(syscall.SIGTERM)
	j.kill = time.AfterFunc(killGrace, func() { j.signal(syscall.SIGKILL) })
}

// failed reports whether an output failure is stopping the command.
func (j *job) failed() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.failure != nil
}

// finish ends stopping the command and returns the output failure, if any.
func (j *job) finish() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.kill != nil {
		j.kill.Stop()
	}
	return j.failure
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
