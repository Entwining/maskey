//go:build darwin && arm64

package activity

import (
	"os"
	"testing"
)

// The kernel describes this test's own process: the program that started it,
// and the terminal it runs on, if any.
func TestInvoker(t *testing.T) {
	parent, ok := lookup(os.Getppid())
	if !ok || parent.path == "" {
		t.Fatalf("the kernel did not describe the test's parent: %+v", parent)
	}
	caller := Invoker(os.Getpid())
	if caller.Exe != parent.path {
		t.Errorf("exe %q, want the parent's %q", caller.Exe, parent.path)
	}
	// Opening /dev/tty succeeds only with a controlling terminal.
	if tty, err := os.Open("/dev/tty"); err == nil {
		tty.Close()
		if caller.TTY == "" {
			t.Error("no terminal for a process that has one")
		}
	} else if caller.TTY != "" {
		t.Errorf("terminal %q for a process without one", caller.TTY)
	}
}
