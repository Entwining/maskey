package runner

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// session is mk run as a job by a shell on a terminal the test owns, as
// the user runs it.
type session struct {
	master *os.File
	tty    *os.File
	modes  unix.Termios
	shell  *exec.Cmd
	job    int // mk's process ID and process group
	events *bufio.Reader
	output lockedBuffer
	// Holding paused stops the test from reading the terminal, as a slow
	// terminal does.
	paused sync.Mutex
	exited chan int
}

// startSession starts the shell with job ("fg" or "bg") and mk's
// arguments; stderr, if not nil, replaces the terminal as their stderr.
func startSession(t *testing.T, stderr *os.File, job string, args ...string) *session {
	t.Helper()
	master, tty, err := openTerminal()
	if err != nil {
		t.Fatal(err)
	}
	// A size and an erase character of the user's for the command to see.
	modes, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	modes.Cc[unix.VERASE] = 8
	unix.IoctlSetTermios(int(tty.Fd()), unix.TIOCSETA, modes)
	unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 33, Col: 111})
	events, eventsW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &session{master: master, tty: tty, modes: *modes, events: bufio.NewReader(events), exited: make(chan int, 1)}
	s.shell = exec.Command(self(t), append([]string{"runner-child", "shell", job}, args...)...)
	s.shell.Stdin, s.shell.Stdout, s.shell.Stderr = tty, tty, tty
	if stderr != nil {
		s.shell.Stderr = stderr
	}
	s.shell.ExtraFiles = []*os.File{eventsW}
	s.shell.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := s.shell.Start(); err != nil {
		t.Fatal(err)
	}
	eventsW.Close()
	if _, err := fmt.Fscanf(s.events, "job %d\n", &s.job); err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			s.paused.Lock()
			s.output.Write(buf[:n])
			s.paused.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		s.shell.Wait()
		s.exited <- s.shell.ProcessState.ExitCode()
	}()
	t.Cleanup(func() {
		s.shell.Process.Kill()
		master.Close()
		tty.Close()
		events.Close()
	})
	return s
}

// command returns the arguments that run the synthetic command in mode.
func command(t *testing.T, mode ...string) []string {
	return append([]string{self(t), "runner-child"}, mode...)
}

// wait returns the shell's exit status, which is mk's.
func (s *session) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-s.exited:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("mk did not finish; output %q", s.output.String())
		return 0
	}
}

func (s *session) waitOutput(t *testing.T, text string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(s.output.String(), text) {
			return
		}
	}
	t.Fatalf("output %q, want %q in it", s.output.String(), text)
}

// currentModes reads the terminal's modes through the master: the shell's
// exit revokes tty.
func (s *session) currentModes(t *testing.T) unix.Termios {
	t.Helper()
	conn, err := s.master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var modes *unix.Termios
	conn.Control(func(fd uintptr) { modes, err = unix.IoctlGetTermios(int(fd), unix.TIOCGETA) })
	if err != nil {
		t.Fatal(err)
	}
	return *modes
}

// stopOutput makes the terminal stop taking output with its queue full,
// so mk blocks in its next write to it. TIOCSTOP holds the queue where
// pausing the test's reader would not, as a read under way empties it.
// Input to the terminal restarts its output.
func (s *session) stopOutput(t *testing.T) {
	t.Helper()
	unix.IoctlSetInt(int(s.tty.Fd()), unix.TIOCSTOP, 0)
	fill, err := unix.Open(s.tty.Name(), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for block := make([]byte, 1024); err == nil; {
		_, err = unix.Write(fill, block)
	}
	unix.Close(fill)
	if err != unix.EAGAIN {
		t.Fatal(err)
	}
}

// restored checks that mk left the terminal's modes as it found them.
func (s *session) restored(t *testing.T) {
	t.Helper()
	if s.currentModes(t) != s.modes {
		t.Fatal("the terminal's modes were not restored")
	}
}

func TestTerminal(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "tty")...)
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	// The command has the terminal on stdin, stdout and stderr, in its
	// foreground, with the user's size and modes, which turn \n into \r\n.
	// The value shows masked.
	want := "terminals=true,true,true foreground=true size=33x111 erase=8\r\nstderr=***\r\ntoken=***\r\n"
	s.waitOutput(t, want)
	if s.output.String() != want {
		t.Fatalf("output %q, want %q", s.output.String(), want)
	}
	s.restored(t)
}

// A value written in two parts shows nothing of the first part while the
// command waits before writing the second.
func TestTerminalValueSplitAcrossWrites(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "split")...)
	// The first half arrives in the same write as "token=".
	s.waitOutput(t, "token=")
	if s.output.String() != "token=" {
		t.Fatalf("output %q while the value is incomplete", s.output.String())
	}
	s.master.Write([]byte("\r"))
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	s.waitOutput(t, "token=***\r\n")
	if s.output.String() != "token=***\r\n" {
		t.Fatalf("output %q", s.output.String())
	}
}

// A stderr redirected away from the terminal stays apart, masked.
func TestRedirectedStderr(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(t *testing.T) (r io.ReadCloser, w *os.File)
		want string
	}{
		{"pipe", func(t *testing.T) (io.ReadCloser, *os.File) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			return r, w
		}, "stderr=***\n"},
		{"another terminal", func(t *testing.T) (io.ReadCloser, *os.File) {
			master, tty, err := openTerminal()
			if err != nil {
				t.Fatal(err)
			}
			return master, tty
		}, "stderr=***\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, w := test.open(t)
			// A terminal discards what is unread once its last user closes it.
			stderr := make(chan []byte, 1)
			go func() {
				data, _ := io.ReadAll(r)
				stderr <- data
			}()
			s := startSession(t, w, "fg", command(t, "tty")...)
			w.Close()
			if code := s.wait(t); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if got := <-stderr; string(got) != test.want {
				t.Fatalf("stderr %q, want %q", got, test.want)
			}
			s.waitOutput(t, "terminals=true,true,false ")
			if strings.Contains(s.output.String(), "stderr") {
				t.Fatalf("stderr reached the terminal: %q", s.output.String())
			}
			s.restored(t)
		})
	}
}

// A stderr on the same terminal by another name stays on the command's
// terminal.
func TestStderrOnDevTTY(t *testing.T) {
	s := startSession(t, nil, "fg", append([]string{"devtty"}, command(t, "tty")...)...)
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	s.waitOutput(t, "terminals=true,true,true ")
	s.waitOutput(t, "stderr=***\r\n")
}

// Without the foreground, or with a value that a terminal would change,
// the command runs as without a terminal: stdout and stderr are pipes.
func TestTerminalNotGiven(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{"background", append([]string{"bg"}, command(t, "tty")...), []string{"terminals=true,false,false foreground=false"}},
		{"control character", append([]string{"fg", "control"}, command(t, "tty")...), []string{
			"terminals=true,false,false foreground=true",
			"mk: a selected value contains a control character, so the command runs without a terminal of its own\r\n",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := startSession(t, nil, test.args[0], test.args[1:]...)
			if code := s.wait(t); code != 0 {
				t.Fatalf("exit %d", code)
			}
			for _, want := range test.want {
				s.waitOutput(t, want)
			}
			s.restored(t)
		})
	}
}

// A stdout on another terminal is not the terminal mk runs in, so the
// command's output is a pipe.
func TestStdoutOnAnotherTerminal(t *testing.T) {
	master, tty, err := openTerminal()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	stdout := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(master)
		stdout <- data
	}()
	s := startSession(t, nil, "fg", append([]string{"stdout", tty.Name()}, command(t, "tty")...)...)
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	tty.Close()
	if got := string(<-stdout); !strings.HasPrefix(got, "terminals=true,false,false ") {
		t.Fatalf("stdout %q", got)
	}
	s.restored(t)
}

func TestResizeReachesCommand(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "winch")...)
	s.waitOutput(t, "size=33x111\r\n")
	unix.IoctlSetWinsize(int(s.tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120})
	s.waitOutput(t, "size=40x120\r\n")
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// A command that stops, however it is stopped, stops mk's job with the
// same signal, with all it wrote shown and the terminal restored for the
// shell; fg continues both once, and input reaches the command again.
func TestSuspendAndResume(t *testing.T) {
	for _, test := range []struct {
		name   string
		stop   func(s *session, command int)
		signal syscall.Signal
	}{
		{"Ctrl-Z", func(s *session, _ int) { s.master.Write([]byte{0x1a}) }, syscall.SIGTSTP},
		{"SIGSTOP", func(_ *session, command int) { syscall.Kill(command, syscall.SIGSTOP) }, syscall.SIGSTOP},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "written")
			s := startSession(t, nil, "fg", command(t, "suspend", record)...)
			// The command stops with its output waiting for a slow terminal.
			s.paused.Lock()
			var written, pid int
			waitFor(t, func() bool {
				data, _ := os.ReadFile(record)
				_, err := fmt.Sscanf(string(data), "%d %d", &written, &pid)
				return err == nil
			})
			test.stop(s, pid)
			// Time for mk to act on the stop while the terminal still lags.
			time.Sleep(100 * time.Millisecond)
			s.paused.Unlock()
			line := make(chan string, 1)
			go func() {
				event, _ := s.events.ReadString('\n')
				line <- event
			}()
			select {
			case event := <-line:
				if want := fmt.Sprintf("stopped %d\n", test.signal); event != want {
					t.Fatalf("event %q, want %q", event, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("mk's job did not stop")
			}
			waitFor(t, func() bool { return strings.Count(s.output.String(), ".") == written })
			s.restored(t)

			s.shell.Process.Signal(syscall.SIGUSR1)
			waitFor(t, func() bool { return s.currentModes(t).Lflag&unix.ICANON == 0 })
			s.master.Write([]byte("hello\r"))
			s.waitOutput(t, "got hello continued=1\r\n")
			if code := s.wait(t); code != 0 {
				t.Fatalf("exit %d", code)
			}
			s.restored(t)
		})
	}
}

// A stop mk does not take itself, such as a SIGTSTP sent to mk alone,
// leaves the terminal to the shell, which may set its own modes; fg makes
// it raw again for the command.
func TestResumeAfterOutsideStop(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "wait")...)
	s.waitOutput(t, "ready")
	syscall.Kill(s.job, syscall.SIGTSTP)
	if event, _ := s.events.ReadString('\n'); event != fmt.Sprintf("stopped %d\n", syscall.SIGTSTP) {
		t.Fatalf("event %q", event)
	}
	unix.IoctlSetTermios(int(s.tty.Fd()), unix.TIOCSETA, &s.modes)
	s.shell.Process.Signal(syscall.SIGUSR1)
	waitFor(t, func() bool { return s.currentModes(t).Lflag&unix.ICANON == 0 })
}

func TestTerminalExitStatus(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage")
	os.WriteFile(garbage, []byte{0xff, 0xfe, 0, 0}, 0o700)
	for _, test := range []struct {
		name   string
		args   []string
		action func(*session)
		want   int
	}{
		{"exit code", command(t, "exit", "3"), nil, 3},
		{"killed", command(t, "signal"), nil, 128 + int(syscall.SIGKILL)},
		{"Ctrl-C", command(t, "wait"), func(s *session) { s.master.Write([]byte{0x03}) }, 128 + int(syscall.SIGINT)},
		{"mk terminated", command(t, "wait"), func(s *session) { syscall.Kill(-s.job, syscall.SIGTERM) }, 128 + int(syscall.SIGTERM)},
		{"not executable", []string{garbage}, nil, ExitNotExecutable},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := startSession(t, nil, "fg", test.args...)
			if test.action != nil {
				s.waitOutput(t, "ready")
				test.action(s)
			}
			if code := s.wait(t); code != test.want {
				t.Fatalf("exit %d, want %d; output %q", code, test.want, s.output.String())
			}
			s.restored(t)
		})
	}
}

func TestTerminalDescendantHoldingOutputDoesNotBlock(t *testing.T) {
	start := time.Now()
	s := startSession(t, nil, "fg", command(t, "orphan")...)
	if code := s.wait(t); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %v for a descendant holding the terminal", elapsed)
	}
	s.waitOutput(t, "parent done")
}

// When the real terminal closes, mk fails and ends the command, even one
// that ignores SIGTERM or stops when asked to end.
func TestTerminalOutputFailureStopsCommand(t *testing.T) {
	for _, mode := range [][]string{{"flood"}, {"flood", "stop"}} {
		t.Run(strings.Join(mode, " "), func(t *testing.T) {
			s := startSession(t, nil, "fg", command(t, mode...)...)
			s.waitOutput(t, "***\r\n")
			start := time.Now()
			s.master.Close()
			if code := s.wait(t); code != ExitFailure {
				t.Fatalf("exit %d, want %d", code, ExitFailure)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("mk took %v to end after its terminal closed", elapsed)
			}
		})
	}
}

// The real terminal can close while mk passes on what a stopped command
// wrote; mk then ends the command instead of stopping its own job.
func TestTerminalOutputFailureWhileStopping(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "release")...)
	s.waitOutput(t, "\r\n")
	var pid int
	if _, err := fmt.Sscanf(s.output.String(), "ready %d", &pid); err != nil {
		t.Fatal(err)
	}
	// mk blocks passing on the command's last line.
	s.stopOutput(t)
	syscall.Kill(pid, syscall.SIGUSR1)
	waitFor(t, func() bool {
		proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		return err == nil && proc.Proc.P_stat == 4 // SSTOP in <sys/proc.h>
	})
	// Time for mk to take the stop report, which nothing outside shows.
	time.Sleep(100 * time.Millisecond)
	s.master.Close()
	if code := s.wait(t); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
}

// A hangup of the real terminal reaches the command as one even when mk
// gets no SIGHUP itself, as the kernel signals only the session leader.
func TestTerminalHangupReachesCommand(t *testing.T) {
	s := startSession(t, nil, "fg", command(t, "wait")...)
	s.waitOutput(t, "ready")
	s.master.Close()
	if code := s.wait(t); code != 128+int(syscall.SIGHUP) {
		t.Fatalf("exit %d, want %d", code, 128+int(syscall.SIGHUP))
	}
}

// A monitor that ends without the command's status is a runner failure,
// and mk first passes on the output it has read, however long the real
// terminal takes to take it.
func TestTerminalMonitorEnds(t *testing.T) {
	read := filepath.Join(t.TempDir(), "read")
	s := startSession(t, nil, "fg", command(t, "release", read)...)
	s.waitOutput(t, "\r\n")
	var pid, monitor int
	if _, err := fmt.Sscanf(s.output.String(), "ready %d %d", &pid, &monitor); err != nil {
		t.Fatal(err)
	}
	s.stopOutput(t)
	syscall.Kill(pid, syscall.SIGUSR1)
	waitFor(t, func() bool {
		_, err := os.Stat(read)
		return err == nil
	})
	syscall.Kill(monitor, syscall.SIGKILL)
	// Not waiting for its output, mk would end at once and drop it. The
	// shell cannot finish exiting while the terminal's output is stopped,
	// so mk itself is looked at.
	time.Sleep(500 * time.Millisecond)
	if proc, err := unix.SysctlKinfoProc("kern.proc.pid", s.job); err != nil || proc.Proc.P_stat == 5 { // SZOMB
		t.Fatal("mk ended before passing on the output")
	}
	unix.IoctlSetInt(int(s.tty.Fd()), unix.TIOCSTART, 0)
	if code := s.wait(t); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	s.waitOutput(t, "released\r\n")
}

func TestMonitorNeedsTheRunner(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	err := exec.Command(self(t), append([]string{MonitorArg}, command(t, "touch", marker)...)...).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitFailure {
		t.Fatalf("error %v, want exit %d", err, ExitFailure)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the command started")
	}
}
