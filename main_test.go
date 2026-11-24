package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
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

// servedStores stands in for the Maskey service's reading of the stores.
type servedStores struct {
	config stores.Config
	asked  int
}

func (s *servedStores) Stores(context.Context) (stores.Config, error) {
	s.asked++
	return s.config, nil
}

// A process that may not read the stores' configuration, as in a sandbox,
// gets it from the service; a missing or broken file is not the service's
// to answer for.
func TestStoresConfigAsksTheServiceWhereTheFileIsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := stores.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	work := stores.Connection{ID: "work", Type: stores.Bitwarden, Region: "us", Project: "project"}
	service := &servedStores{config: stores.Config{Default: "work", Stores: []stores.Connection{work}}}
	ctx := context.Background()
	path := filepath.Join(dir, "stores.json")

	if config, err := storesConfig(ctx, service); err != nil || config.Default != stores.ICloud || service.asked != 0 {
		t.Errorf("without a file: %+v, %v, the service asked %d times", config, err, service.asked)
	}
	if err := os.WriteFile(path, []byte(`{"default":"icloud","stores":[{"id":"local","type":"bitwarden"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if config, err := storesConfig(ctx, service); err != nil || len(config.Stores) != 1 || config.Stores[0].ID != "local" || service.asked != 0 {
		t.Errorf("a file this process reads: %+v, %v, the service asked %d times", config, err, service.asked)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if config, err := storesConfig(ctx, service); err != nil || config.Default != "work" || service.asked != 1 {
		t.Errorf("a file this process may not read: %+v, %v, the service asked %d times", config, err, service.asked)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := storesConfig(ctx, service); !store.IsKind(err, store.KindPrerequisite) || service.asked != 1 {
		t.Errorf("a broken file: %v, the service asked %d times", err, service.asked)
	}
}
