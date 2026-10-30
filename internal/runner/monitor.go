package runner

import (
	"encoding/binary"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// MonitorArg, as the first argument, makes mk's executable the monitor of
// a command that runs on a terminal of its own; main passes the remaining
// arguments to Monitor.
const MonitorArg = "--run-monitor"

// report is what the monitor tells the runner about the command.
type report struct {
	Kind  uint32
	Value uint32
}

const (
	started    uint32 = iota // Value is the command's process ID.
	notStarted               // Value is the errno that starting it failed with.
	changed                  // Value is its wait status: stopped, exited, or killed.
)

// Monitor leads the session of the terminal that runTerminal opens, with
// the command's environment and that terminal as its stdin, its stdout,
// and, unless redirected, its stderr. It starts the command, args[1:] from
// the executable args[0], in the terminal's foreground and reports to the
// runner on descriptor 3 until the command ends. Its own exit then hangs
// the terminal up and revokes it from what the command left running.
func Monitor(args []string) error {
	var stat unix.Stat_t
	if len(args) < 2 || unix.Fstat(3, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.New("this is part of mk run, not a command")
	}
	reports := os.NewFile(3, "reports")
	syscall.CloseOnExec(3)
	send := func(kind, value uint32) error {
		return binary.Write(reports, binary.NativeEndian, report{kind, value})
	}
	pid, err := syscall.ForkExec(args[0], args[1:], &syscall.ProcAttr{
		Env:   os.Environ(),
		Files: []uintptr{0, 1, 2},
		Sys:   &syscall.SysProcAttr{Foreground: true},
	})
	if err != nil {
		var errno syscall.Errno
		errors.As(err, &errno)
		return send(notStarted, uint32(errno))
	}
	// The command does not inherit this: it was started with the defaults.
	// Waiting for the terminal's output below, the monitor is in the
	// background, where the kernel would stop it with SIGTTOU.
	signal.Ignore(syscall.SIGTTOU)
	if err := send(started, uint32(pid)); err != nil {
		return err
	}
	for {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(pid, &status, syscall.WUNTRACED, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		_, stopped := stopSignal(status)
		if stopped {
			// The runner passes on what the command wrote before it
			// stopped, which this waits for it to have read.
			_ = unix.IoctlSetInt(0, unix.TIOCDRAIN, 0)
		}
		if err := send(changed, uint32(status)); err != nil || !stopped {
			return err
		}
	}
}

// stopSignal returns the signal that stopped the command, if a stop is what
// status reports. syscall.WaitStatus on Darwin takes a stop by SIGSTOP for
// a continue.
func stopSignal(status syscall.WaitStatus) (syscall.Signal, bool) {
	if status&0x7f != 0x7f {
		return 0, false
	}
	return syscall.Signal(status >> 8 & 0xff), true
}
