package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

// The test binary also plays the processes around the runner: the
// synthetic command (`runner-child <mode>`), the monitor, mk itself
// (`runner-child mk`), and the user's shell (`runner-child shell`).
func TestMain(m *testing.M) {
	outputGrace = 300 * time.Millisecond
	killGrace = 300 * time.Millisecond
	// The race detector otherwise keeps every helper process alive for a
	// second after it exits, the monitor holding the terminal among them.
	os.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	switch {
	case len(os.Args) > 1 && os.Args[1] == MonitorArg:
		if err := Monitor(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(ExitFailure)
		}
	case len(os.Args) > 2 && os.Args[1] == "runner-child":
		child(os.Args[2], os.Args[3:])
	default:
		os.Exit(m.Run())
	}
}

func child(mode string, args []string) {
	value := os.Getenv("SYNTH_TOKEN")
	switch mode {
	case "env":
		fmt.Printf("token=%s other=%s\n", value, os.Getenv("SYNTH_OTHER"))
	case "exit":
		code, _ := strconv.Atoi(args[0])
		os.Exit(code)
	case "signal":
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
	case "wait":
		// Ready, then wait to be interrupted.
		fmt.Println("ready")
		time.Sleep(10 * time.Second)
		os.Exit(3)
	case "suspend":
		// Fill the terminal's buffers, as a full-screen program's last
		// screen does on a slow terminal, and record in args[0] how much it
		// wrote and its process ID, for the test to stop it; then echo a
		// line of input and count the SIGCONTs received.
		continued := make(chan os.Signal, 8)
		signal.Notify(continued, syscall.SIGCONT)
		syscall.SetNonblock(1, true)
		written := 0
		chunk := bytes.Repeat([]byte("."), 4096)
		for full := 0; full < 3; {
			n, err := syscall.Write(1, chunk)
			written += max(n, 0)
			if err == nil {
				full = 0
			} else {
				full++
				time.Sleep(50 * time.Millisecond)
			}
		}
		syscall.SetNonblock(1, false)
		os.WriteFile(args[0], fmt.Appendf(nil, "%d %d", written, os.Getpid()), 0o600)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("got %s continued=%d\n", strings.TrimSpace(line), len(continued))
	case "flood":
		// Keep printing through hangups, broken pipes, and polite stops, or
		// with "stop", stop as on Ctrl-Z when asked to end.
		signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
		if len(args) > 0 && args[0] == "stop" {
			terminate := make(chan os.Signal, 1)
			signal.Notify(terminate, syscall.SIGTERM)
			go func() {
				<-terminate
				syscall.Kill(0, syscall.SIGTSTP)
			}()
		} else {
			signal.Ignore(syscall.SIGTERM)
		}
		for {
			fmt.Println(value)
		}
	case "orphan":
		// Leave a descendant holding stdout open, which a hangup does not
		// end, then exit.
		signal.Ignore(syscall.SIGHUP)
		cmd := exec.Command("/bin/sleep", "10")
		cmd.Stdout = os.Stdout
		cmd.Start()
		fmt.Print("parent done")
	case "touch":
		os.WriteFile(args[0], nil, 0o600)
	case "tty":
		// What the command sees of its terminal, then the value on stderr
		// and stdout.
		size, _ := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
		modes, _ := unix.IoctlGetTermios(0, unix.TIOCGETA)
		group, _ := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		fmt.Printf("terminals=%v,%v,%v foreground=%v size=%dx%d erase=%d\n",
			term.IsTerminal(0), term.IsTerminal(1), term.IsTerminal(2), group == unix.Getpgrp(), size.Row, size.Col, modes.Cc[unix.VERASE])
		fmt.Fprintf(os.Stderr, "stderr=%s\n", value)
		fmt.Printf("token=%s\n", value)
	case "split":
		// Write half the value, and the rest after a line of input, which
		// the terminal does not echo between the halves.
		modes, _ := unix.IoctlGetTermios(0, unix.TIOCGETA)
		modes.Lflag &^= unix.ECHO
		unix.IoctlSetTermios(0, unix.TIOCSETA, modes)
		half := len(value) / 2
		fmt.Print("token=" + value[:half])
		bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Println(value[half:])
	case "winch":
		// Report the size, and again after a resize.
		resized := make(chan os.Signal, 1)
		signal.Notify(resized, syscall.SIGWINCH)
		report := func() {
			size, _ := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
			fmt.Printf("size=%dx%d\n", size.Row, size.Col)
		}
		report()
		select {
		case <-resized:
			report()
		case <-time.After(5 * time.Second):
		}
	case "release":
		// Ready with its own and its monitor's process IDs; on SIGUSR1,
		// write a line, then stop as on Ctrl-Z, or, given args[0], create
		// that file once mk has read the line.
		proceed := make(chan os.Signal, 1)
		signal.Notify(proceed, syscall.SIGUSR1)
		fmt.Printf("ready %d %d\n", os.Getpid(), os.Getppid())
		<-proceed
		fmt.Println("released")
		if len(args) == 0 {
			syscall.Kill(os.Getpid(), syscall.SIGTSTP)
			return
		}
		unix.IoctlSetInt(1, unix.TIOCDRAIN, 0)
		os.WriteFile(args[0], nil, 0o600)
		time.Sleep(10 * time.Second)
	case "mk":
		// mk itself, running args with its own stdio; it exits with its
		// outcome, a signal as 128+n. With "control", the value has a
		// control character; with "devtty", stderr is /dev/tty; with
		// "stdout <path>", stdout is that file.
		selected := entries
		stdout, stderr := os.Stdout, os.Stderr
	options:
		for ; ; args = args[1:] {
			switch args[0] {
			case "control":
				selected = []store.Entry{{Ref: store.Ref{Namespace: "test", Name: "SYNTH_TOKEN"}, Value: []byte("line1\nline2")}}
			case "devtty":
				stderr, _ = os.OpenFile("/dev/tty", os.O_WRONLY, 0)
			case "stdout":
				stdout, _ = os.OpenFile(args[1], os.O_WRONLY, 0)
				args = args[1:]
			default:
				break options
			}
		}
		exit, err := Run(context.Background(), Command{Path: args[0], Args: args[1:], Entries: selected, Stdin: os.Stdin, Stdout: stdout, Stderr: stderr})
		var runErr *Error
		switch {
		case errors.As(err, &runErr):
			os.Exit(runErr.Code)
		case err != nil:
			os.Exit(1)
		case exit.Signal != 0:
			os.Exit(128 + int(exit.Signal))
		}
		os.Exit(exit.Code)
	case "shell":
		// A job-control shell on its stdin: it runs `runner-child mk
		// args[1:]` as a job in the foreground, or the background if
		// args[0] is "bg"; reports on descriptor 3 the job's process ID and
		// the signal of each stop; continues it in the foreground on
		// SIGUSR1, as fg does; and exits as the job did.
		foreground := make(chan os.Signal, 1)
		signal.Notify(foreground, syscall.SIGUSR1)
		// The test hangs up the terminal to make mk's output fail.
		signal.Notify(make(chan os.Signal, 1), syscall.SIGHUP)
		job, err := syscall.ForkExec(os.Args[0], append([]string{os.Args[0], "runner-child", "mk"}, args[1:]...), &syscall.ProcAttr{
			Env:   os.Environ(),
			Files: []uintptr{0, 1, 2},
			Sys:   &syscall.SysProcAttr{Setpgid: true, Foreground: args[0] == "fg"},
		})
		if err != nil {
			os.Exit(1)
		}
		// Taking the terminal back from the background.
		signal.Ignore(syscall.SIGTTOU)
		events := os.NewFile(3, "events")
		fmt.Fprintln(events, "job", job)
		for {
			var status syscall.WaitStatus
			if _, err := syscall.Wait4(job, &status, syscall.WUNTRACED, nil); err != nil {
				continue
			}
			sig, stopped := stopSignal(status)
			switch {
			case status.Signaled():
				os.Exit(128 + int(status.Signal()))
			case !stopped:
				os.Exit(status.ExitStatus())
			}
			unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, unix.Getpgrp())
			fmt.Fprintln(events, "stopped", int(sig))
			<-foreground
			unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, job)
			syscall.Kill(-job, syscall.SIGCONT)
		}
	}
}

const token = "synthetic-7d1e-token"

var entries = []store.Entry{{Ref: store.Ref{Namespace: "test", Name: "SYNTH_TOKEN"}, Value: []byte(token)}}

func self(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// lockedBuffer is safe to read while the runner writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func run(t *testing.T, stdin *os.File, args ...string) (Exit, string, string, error) {
	t.Helper()
	var stdout, stderr lockedBuffer
	exit, err := Run(context.Background(), Command{
		Path:    self(t),
		Args:    append([]string{"runner-child"}, args...),
		Entries: entries,
		Stdin:   stdin,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	return exit, stdout.String(), stderr.String(), err
}

func TestEnvironmentInjection(t *testing.T) {
	t.Setenv("SYNTH_TOKEN", "inherited")
	t.Setenv("SYNTH_OTHER", "kept")
	exit, stdout, _, err := run(t, nil, "env")
	if err != nil || exit != (Exit{}) {
		t.Fatalf("exit %+v, error %v", exit, err)
	}
	// The entry replaces the inherited variable and reaches the command,
	// whose output shows it masked; other variables pass through.
	if stdout != "token=*** other=kept\n" {
		t.Fatalf("stdout %q", stdout)
	}
}

func TestSignalExitPreserved(t *testing.T) {
	exit, _, _, err := run(t, nil, "signal")
	if err != nil || exit.Signal != syscall.SIGKILL {
		t.Fatalf("exit %+v, error %v, want SIGKILL", exit, err)
	}
}

func TestInterruptReachesCommand(t *testing.T) {
	var stdout, stderr lockedBuffer
	result := make(chan Exit, 1)
	go func() {
		exit, err := Run(context.Background(), Command{Path: self(t), Args: []string{"runner-child", "wait"}, Entries: entries, Stdout: &stdout, Stderr: &stderr})
		if err != nil {
			t.Error(err)
		}
		result <- exit
	}()
	waitFor(t, func() bool { return stdout.String() == "ready\n" })
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case exit := <-result:
		if exit.Signal != syscall.SIGINT {
			t.Fatalf("exit %+v, want SIGINT", exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command was not interrupted")
	}
}

func TestOutputFailureStopsCommand(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), Command{Path: self(t), Args: []string{"runner-child", "flood"}, Entries: entries, Stdout: failing{}, Stderr: io.Discard})
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != ExitFailure {
		t.Fatalf("error %v, want a runner failure", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stopping a command that ignores SIGTERM took %v", elapsed)
	}
}

func TestBrokenStdoutIsAnOutputFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	cmd := exec.Command(self(t), "runner-child", "mk", self(t), "runner-child", "flood")
	cmd.Stdout = w
	err = cmd.Run()
	w.Close()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v", err)
	}
	status := exitErr.Sys().(syscall.WaitStatus)
	if status.Signaled() || status.ExitStatus() != ExitFailure {
		t.Fatalf("status %v, want exit %d", status, ExitFailure)
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestDescendantHoldingOutputDoesNotBlock(t *testing.T) {
	start := time.Now()
	exit, stdout, _, err := run(t, nil, "orphan")
	if err != nil || exit != (Exit{}) {
		t.Fatalf("exit %+v, error %v", exit, err)
	}
	if stdout != "parent done" {
		t.Fatalf("stdout %q", stdout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %v for a descendant holding stdout", elapsed)
	}
}

func TestFinalOutputFailureStopsDescendants(t *testing.T) {
	for _, test := range []struct {
		name string
		fd   string
		fail bool
	}{
		{"success", "1", false},
		{"stdout", "1", true},
		{"stderr", "2", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			var output bytes.Buffer
			var stdout, stderr io.Writer = &output, io.Discard
			if test.fail {
				if test.fd == "1" {
					stdout = failing{}
				} else {
					stderr = failing{}
				}
			}
			// A proper prefix of token stays buffered until Close.
			exit, err := Run(context.Background(), Command{
				Path: "/bin/sh", Args: []string{"-c", `/bin/sleep 30 & printf '%s' "$!" > "$1"; printf s >&"$2"`, "sh", pidfile, test.fd},
				Entries: entries, Stdout: stdout, Stderr: stderr,
			})
			data, readErr := os.ReadFile(pidfile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if !test.fail {
				if err != nil || exit != (Exit{}) || output.String() != "s" {
					t.Fatalf("exit %+v, error %v, output %q", exit, err, output.String())
				}
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatalf("successful command's descendant stopped: %v", err)
				}
				return
			}
			var runErr *Error
			if !errors.As(err, &runErr) || runErr.Code != ExitFailure || !strings.Contains(runErr.Message, "broken pipe") {
				t.Fatalf("error %v, want an output failure", err)
			}
			waitFor(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
		})
	}
}

func TestNoSpawnWhenSetupFails(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	_, err := Run(context.Background(), Command{
		Path:    self(t),
		Args:    []string{"runner-child", "touch", marker},
		Entries: []store.Entry{{Ref: store.Ref{Namespace: "test", Name: "EMPTY"}, Value: nil}},
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	})
	if err == nil {
		t.Fatal("Run accepted an unmaskable value")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the command started")
	}
}

func TestCommandNotStartable(t *testing.T) {
	dir := t.TempDir()
	notExecutable := filepath.Join(dir, "script")
	os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600)
	badFormat := filepath.Join(dir, "garbage")
	os.WriteFile(badFormat, []byte{0xff, 0xfe, 0, 0}, 0o700)
	for path, want := range map[string]int{
		"maskey-no-such-command":      ExitNotFound,
		filepath.Join(dir, "missing"): ExitNotFound,
		notExecutable:                 ExitNotExecutable,
		dir:                           ExitNotExecutable,
		badFormat:                     ExitNotExecutable,
	} {
		_, err := Run(context.Background(), Command{Path: path, Entries: entries, Stdout: io.Discard, Stderr: io.Discard})
		var runErr *Error
		if !errors.As(err, &runErr) || runErr.Code != want {
			t.Errorf("%s: error %v, want exit %d", path, err, want)
		}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatal("timed out")
}
