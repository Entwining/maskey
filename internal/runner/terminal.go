package runner

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// interactive reports whether the command gets a terminal of its own: the
// runner is in the foreground of the terminal that is its stdin and stdout,
// and the values pass through a terminal unchanged.
func interactive(c Command, values [][]byte) bool {
	stdout, ok := c.Stdout.(*os.File)
	if !ok || !foreground(stdout) || !foreground(c.Stdin) {
		return false
	}
	// A terminal's output processing rewrites control bytes, such as a
	// newline into CR LF, so a value with one would reach the redactor
	// changed and show unmasked.
	for _, v := range values {
		if slices.ContainsFunc(v, func(b byte) bool { return b < 0x20 || b == 0x7f }) {
			fmt.Fprintln(c.Stderr, "mk: a selected value contains a control character, so the command runs without a terminal of its own")
			return false
		}
	}
	return true
}

// runTerminal runs cmd on a pseudo-terminal the way sudo's use_pty does.
// The runner keeps the real terminal raw, passes keystrokes to the master
// unchanged, and passes the master's output on through stdout. A monitor
// leads the pseudo-terminal's session, starts the command in a foreground
// process group of that session, and reports when the command stops or
// ends. The command cannot lead the session itself: its process group
// would be orphaned, and the kernel discards stop signals to those, so
// Ctrl-Z would not stop it.
func runTerminal(cmd *exec.Cmd, c Command, j *job, stdout, stderr io.Writer) (syscall.WaitStatus, error) {
	failed := func(doing string, err error) error {
		return &Error{Code: ExitFailure, Message: doing + ": " + err.Error()}
	}
	j.group = true
	tty := int(c.Stdin.Fd())
	modes, err := unix.IoctlGetTermios(tty, unix.TIOCGETA)
	if err != nil {
		return 0, failed("reading the terminal's modes", err)
	}
	master, slave, err := openTerminal()
	if err != nil {
		return 0, failed("opening a terminal", err)
	}
	defer master.Close()
	defer slave.Close()
	// The command starts with the user's modes, such as the erase
	// character, and the user's window size.
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TIOCSETA, modes); err != nil {
		return 0, failed("setting up the terminal", err)
	}
	if err := pty.InheritSize(c.Stdin, master); err != nil {
		return 0, failed("setting up the terminal", err)
	}
	reports, reportsW, err := os.Pipe()
	if err != nil {
		return 0, failed("starting the command's monitor", err)
	}
	defer reports.Close()
	defer reportsW.Close()
	self, err := os.Executable()
	if err != nil {
		return 0, failed("starting the command's monitor", err)
	}
	path := cmd.Path
	cmd.Args = append([]string{self, MonitorArg, path}, cmd.Args...)
	cmd.Path = self
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A stderr redirected away from the terminal, by any name it has,
	// stays apart, a pipe as in pipe mode.
	if f, ok := c.Stderr.(*os.File); !ok || !foreground(f) {
		cmd.Stderr = stderr
		cmd.WaitDelay = outputGrace
	}
	cmd.ExtraFiles = []*os.File{reportsW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)
	continued := make(chan os.Signal, 1)
	signal.Notify(continued, syscall.SIGCONT)
	defer signal.Stop(continued)

	// Raw, the real terminal neither echoes nor turns keys into signals:
	// the pseudo-terminal does both with the user's modes.
	if _, err := term.MakeRaw(tty); err != nil {
		return 0, failed("setting up the terminal", err)
	}
	raw := true
	restore := func() {
		if raw {
			_ = unix.IoctlSetTermios(tty, unix.TIOCSETA, modes)
			raw = false
		}
	}
	defer restore()

	if err := cmd.Start(); err != nil {
		return 0, failed("starting the command's monitor", err)
	}
	slave.Close()
	reportsW.Close()
	events := make(chan report)
	go func() {
		defer close(events)
		for {
			var r report
			if binary.Read(reports, binary.NativeEndian, &r) != nil {
				return
			}
			events <- r
		}
	}()
	// A session leader's exit waits until its terminal's output is read,
	// so the master is closed before the monitor is waited for.
	end := func() string {
		master.Close()
		if err := cmd.Wait(); err != nil {
			return err.Error()
		}
		return "it exited"
	}

	r, ok := <-events
	if !ok {
		return 0, &Error{Code: ExitFailure, Message: "starting the command failed: its monitor ended: " + end()}
	}
	if r.Kind == notStarted {
		end()
		return 0, execError(&os.PathError{Op: "fork/exec", Path: path, Err: syscall.Errno(r.Value)}, ExitFailure)
	}
	j.start(int(r.Value))
	// Input is read only now: a key such as Ctrl-C would otherwise reach
	// the monitor, which leads the terminal's foreground until the command
	// takes it over.
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := c.Stdin.Read(buf)
			if n > 0 {
				if _, err := master.Write(buf[:n]); err != nil {
					return
				}
				continue
			}
			// Reading nothing from a raw terminal means it hung up, which the
			// kernel signals only to its session leader; from a cooked one,
			// as around a stop, it is Ctrl-D.
			modes, merr := unix.IoctlGetTermios(tty, unix.TIOCGETA)
			if err == io.EOF && merr == nil && modes.Lflag&unix.ICANON != 0 {
				continue
			}
			j.signal(syscall.SIGHUP)
			return
		}
	}()
	output := relay(master, stdout)

	// resume runs when the runner's stop returns and again on the SIGCONT
	// that ended it, unless the kernel discarded the stop; suspended makes
	// it continue the command once.
	suspended := false
	resume := func() {
		// The terminal's own modes decide, not raw: a stop the runner did
		// not take itself, as by a SIGTSTP sent to it, leaves the terminal
		// to the shell, which may set other modes for the job's return.
		modes, err := unix.IoctlGetTermios(tty, unix.TIOCGETA)
		cooked := err == nil && modes.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0
		if cooked && foreground(c.Stdin) {
			if _, err := term.MakeRaw(tty); err == nil {
				raw = true
			}
			_ = pty.InheritSize(c.Stdin, master)
		}
		if suspended {
			suspended = false
			j.signal(syscall.SIGCONT)
		}
	}
	for {
		select {
		case <-resized:
			_ = pty.InheritSize(c.Stdin, master)
		case <-continued:
			resume()
		case r, ok := <-events:
			if !ok {
				message := "waiting for the command: its monitor ended: " + end()
				<-output
				return 0, &Error{Code: ExitFailure, Message: message}
			}
			status := syscall.WaitStatus(r.Value)
			sig, stopped := stopSignal(status)
			if !stopped {
				// The output ends without a grace period: the monitor's exit
				// revokes the terminal from processes the command left on it.
				<-output
				end()
				return status, nil
			}
			// What the command wrote before it stopped, such as a
			// full-screen program's switch back to the normal screen, has
			// been read; the relay passes it on before ending.
			_ = master.SetReadDeadline(time.Now())
			<-output
			_ = master.SetReadDeadline(time.Time{})
			// An output failure, before the stop or while passing that on,
			// is ending the command: the runner stays running for the
			// SIGKILL, which ends it even while it is stopped.
			if !j.failed() {
				// Stopping the runner's whole group, as sudo does, also stops
				// a script without job control that runs it, so the user's
				// shell sees its job stop. Kill returns once the group
				// continues, or at once if the kernel discards the stop
				// because no shell manages the group.
				restore()
				suspended = true
				_ = syscall.Kill(-unix.Getpgrp(), sig)
				resume()
			}
			output = relay(master, stdout)
		}
	}
}

// openTerminal opens a pseudo-terminal whose master Go's poller waits on,
// so reads of it take deadlines and end when it is closed. pty.Open returns
// a blocking master, which the poller leaves alone; a non-blocking
// duplicate is registered with it.
func openTerminal() (master, slave *os.File, err error) {
	blocking, slave, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	defer blocking.Close()
	fd, err := unix.FcntlInt(blocking.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		if err = unix.SetNonblock(fd, true); err != nil {
			unix.Close(fd)
		}
	}
	if err != nil {
		slave.Close()
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), blocking.Name()), slave, nil
}

// relay passes the master's output on to w until the master ends or its
// read deadline passes, and then closes the channel it returns. It keeps
// reading after w fails, which has stopped the command by then, so the
// command and its monitor never wait for the output to be read.
func relay(master *os.File, w io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return done
}
