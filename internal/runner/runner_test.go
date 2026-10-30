package runner

import (
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

	"github.com/Entwining/maskey/internal/store"
)

// The test binary is also the synthetic command: `runner-child <mode>`.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "runner-child" {
		child(os.Args[2], os.Args[3:])
		return
	}
	outputGrace = 300 * time.Millisecond
	killGrace = 300 * time.Millisecond
	os.Exit(m.Run())
}

func child(mode string, args []string) {
	value := os.Getenv("SYNTH_TOKEN")
	switch mode {
	case "env":
		fmt.Printf("token=%s other=%s\n", value, os.Getenv("SYNTH_OTHER"))
	case "signal":
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
	case "wait":
		// Ready, then wait to be interrupted.
		fmt.Println("ready")
		time.Sleep(10 * time.Second)
		os.Exit(3)
	case "flood":
		// Ignore polite stops and broken pipes, and keep printing.
		signal.Ignore(syscall.SIGTERM, syscall.SIGPIPE)
		for {
			fmt.Println(value)
		}
	case "orphan":
		// Leave a descendant holding stdout open, then exit.
		cmd := exec.Command("/bin/sleep", "10")
		cmd.Stdout = os.Stdout
		cmd.Start()
		fmt.Print("parent done")
	case "touch":
		os.WriteFile(args[0], nil, 0o600)
	case "parent":
		// Run a flooding command with this process's own stdout.
		_, err := Run(context.Background(), Command{Path: os.Args[0], Args: []string{"runner-child", "flood"}, Entries: entries, Stdout: os.Stdout, Stderr: os.Stderr})
		var runErr *Error
		if errors.As(err, &runErr) {
			os.Exit(runErr.Code)
		}
		os.Exit(0)
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
	cmd := exec.Command(self(t), "runner-child", "parent")
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
