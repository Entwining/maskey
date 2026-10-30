package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

func TestMain(m *testing.M) {
	if n, err := strconv.Atoi(os.Getenv("MASKEY_TEST_EXIT_BY_SIGNAL")); err == nil {
		exitBySignal(syscall.Signal(n))
	}
	os.Exit(m.Run())
}

func TestExitBySignal(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// One signal the Go runtime dies by, and ones it throws on or ignores.
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGPIPE} {
		cmd := exec.Command(self)
		cmd.Env = append(os.Environ(), "MASKEY_TEST_EXIT_BY_SIGNAL="+strconv.Itoa(int(sig)))
		var exitErr *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exitErr) {
			t.Fatalf("%v: error %v", sig, err)
		}
		if status := exitErr.Sys().(syscall.WaitStatus); !status.Signaled() || status.Signal() != sig {
			t.Errorf("%v: status %v", sig, status)
		}
	}
}

// The service attribute of every entry is the app's identifier, so
// a change to either alone would orphan every entry.
func TestServiceIsTheAppIdentifier(t *testing.T) {
	var c struct{ Identifier string }
	if err := json.Unmarshal(config, &c); err != nil || c.Identifier != store.Service {
		t.Fatalf("mygo.json identifier %q, store.Service %q", c.Identifier, store.Service)
	}
}
