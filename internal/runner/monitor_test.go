package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A monitor that ends without the command's status is a runner failure,
// and mk first passes on the output it has read, however long the real
// terminal takes to take it.
func TestTerminalMonitorEnds(t *testing.T) {
	read := filepath.Join(t.TempDir(), "read")
	exited := filepath.Join(t.TempDir(), "exited")
	s := startSession(t, nil, "fg", append([]string{"exited", exited}, command(t, "release", read)...)...)
	s.waitOutput(t, "\r\n")
	var pid, monitor int
	if _, err := fmt.Sscanf(s.output.String(), "ready %d %d", &pid, &monitor); err != nil {
		t.Fatal(err)
	}
	// The command outlives its monitor.
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
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
	// Exited runs without waiting for the output, as when the command exits.
	if _, err := os.Stat(exited); err != nil {
		t.Errorf("Exited did not run once the monitor ended: %v", err)
	}
	unix.IoctlSetInt(int(s.tty.Fd()), unix.TIOCSTART, 0)
	if code := s.wait(t); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	s.waitOutput(t, "released\r\n")
}

// Exited runs once the command exits, while its output still waits for a
// stopped terminal.
func TestTerminalExitedBeforeOutput(t *testing.T) {
	read := filepath.Join(t.TempDir(), "read")
	exited := filepath.Join(t.TempDir(), "exited")
	s := startSession(t, nil, "fg", append([]string{"exited", exited}, command(t, "release", read)...)...)
	s.waitOutput(t, "\r\n")
	var pid int
	if _, err := fmt.Sscanf(s.output.String(), "ready %d", &pid); err != nil {
		t.Fatal(err)
	}
	s.stopOutput(t)
	syscall.Kill(pid, syscall.SIGUSR1)
	waitFor(t, func() bool {
		_, err := os.Stat(read)
		return err == nil
	})
	syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, func() bool {
		_, err := os.Stat(exited)
		return err == nil
	})
	unix.IoctlSetInt(int(s.tty.Fd()), unix.TIOCSTART, 0)
	if code := s.wait(t); code != 128+int(syscall.SIGKILL) {
		t.Fatalf("exit %d, want %d", code, 128+int(syscall.SIGKILL))
	}
	s.waitOutput(t, "released\r\n")
}

// Exited runs once a stopped command exits, while mk and its monitor still
// wait for the real terminal to take what the command wrote; mk's job then
// stops for each stop it passes on, and ends as the command did.
func TestTerminalExitedAfterStop(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		again bool
	}{
		{"stopped", nil, false},
		// The monitor waits for the line left in the terminal before it
		// reports the stop.
		{"output queued", []string{"queued"}, false},
		// mk's reports wait while it takes the first stop.
		{"stopped twice", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			exited := filepath.Join(t.TempDir(), "exited")
			s := startSession(t, nil, "fg", append([]string{"exited", exited}, command(t, append([]string{"release"}, test.args...)...)...)...)
			s.waitOutput(t, "\r\n")
			var pid int
			if _, err := fmt.Sscanf(s.output.String(), "ready %d", &pid); err != nil {
				t.Fatal(err)
			}
			stat := func() int8 {
				proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
				if err != nil {
					return 0
				}
				return proc.Proc.P_stat
			}
			const stopped = 4 // SSTOP in <sys/proc.h>
			stop := func() {
				waitFor(t, func() bool { return stat() == stopped })
				// Time for mk to take the stop, which nothing outside shows.
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(exited); err == nil {
					t.Fatal("Exited ran when the command stopped")
				}
			}
			s.stopOutput(t)
			syscall.Kill(pid, syscall.SIGUSR1)
			stop()
			if test.again {
				syscall.Kill(pid, syscall.SIGCONT)
				waitFor(t, func() bool { return stat() != stopped })
				syscall.Kill(pid, syscall.SIGSTOP)
				stop()
			}
			syscall.Kill(pid, syscall.SIGKILL)
			waitFor(t, func() bool {
				_, err := os.Stat(exited)
				return err == nil
			})
			go func() {
				for {
					if _, err := s.events.ReadString('\n'); err != nil {
						return
					}
					s.shell.Process.Signal(syscall.SIGUSR1)
				}
			}()
			unix.IoctlSetInt(int(s.tty.Fd()), unix.TIOCSTART, 0)
			if code := s.wait(t); code != 128+int(syscall.SIGKILL) {
				t.Fatalf("exit %d, want %d", code, 128+int(syscall.SIGKILL))
			}
			s.waitOutput(t, "released\r\n")
		})
	}
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
