package stores

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/bitwarden/bitwardentest"
	"github.com/Entwining/maskey/internal/store"
)

// A process that may not read the configuration is told so apart from one
// that reads a broken file: only the first can have the service read it.
func TestLoadTellsADeniedReadFromABrokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(configPath(dir), []byte(`{"default":"work","stores":[{"id":"work","type":"bitwarden"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(configPath(dir), 0); err != nil {
		t.Fatal(err)
	}
	if config, err := Load(dir); !errors.Is(err, ErrDenied) || !store.IsKind(err, store.KindPrerequisite) || config.Default != ICloud {
		t.Errorf("a file this process may not read: %+v, %v", config, err)
	}
	if err := os.Chmod(configPath(dir), 0o600); err != nil {
		t.Fatal(err)
	}
	if config, err := Load(dir); err != nil || config.Default != "work" {
		t.Errorf("a file it may read: %+v, %v", config, err)
	}
	if err := os.WriteFile(configPath(dir), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); errors.Is(err, ErrDenied) || !store.IsKind(err, store.KindPrerequisite) {
		t.Errorf("a broken file: %v", err)
	}
}

// held is a transport that holds each request to a path containing hold
// until release closes, and says on entered that it holds one.
type held struct {
	http.RoundTripper
	hold             string
	entered, release chan struct{}
}

func (h held) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, h.hold) {
		select {
		case h.entered <- struct{}{}:
			<-h.release
		case <-h.release:
		}
	}
	return h.RoundTripper.RoundTrip(r)
}

// While one store waits for Bitwarden, to sync or to be added, another
// store answers from its saved copy, the stores can be read and changed,
// and a second change to the waiting connection waits its turn.
func TestASlowStoreHoldsBackNoOther(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	setup := f.registry()
	for _, id := range []string{"slow", "work"} {
		addition := bitwardenToken()
		addition.ID = id
		if _, err := setup.Add(ctx, addition); err != nil {
			t.Fatal(err)
		}
	}
	work, err := setup.Backend(ctx, "work", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.Items(ctx); err != nil {
		t.Fatal(err)
	}

	for name, slow := range map[string]func(r *Registry) error{
		"a sync": func(r *Registry) error { return r.Sync(ctx, "slow") },
		"an addition": func(r *Registry) error {
			addition := bitwardenToken()
			addition.ID = "third"
			_, err := r.Add(ctx, addition)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := f.registry()
			transport := held{f.fake.Client().Transport, "/projects", make(chan struct{}), make(chan struct{})}
			r.http = &http.Client{Transport: transport}
			release := sync.OnceFunc(func() { close(transport.release) })
			t.Cleanup(release)
			finished := make(chan error, 2)
			go func() { finished <- slow(r) }()
			<-transport.entered

			others := make(chan error, 1)
			go func() {
				backend, err := r.Backend(ctx, "work", target)
				if err == nil {
					_, err = backend.Items(store.WithSaved(ctx))
				}
				if err == nil {
					_, err = r.Config(ctx)
				}
				if err == nil {
					err = r.SetDefault(ctx, "work")
				}
				others <- err
			}()
			select {
			case err := <-others:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("another store waited for the slow one")
			}

			// The same connection's next change waits for this one.
			go func() { finished <- slow(r) }()
			select {
			case err := <-finished:
				t.Fatalf("a change ended while the connection's request was held: %v", err)
			case <-transport.entered:
				if name == "an addition" {
					t.Fatal("a second addition of the name reached Bitwarden beside the first")
				}
			case <-time.After(200 * time.Millisecond):
			}
			release()
			first, second := <-finished, <-finished
			if name == "an addition" {
				// One connects the name; the other finds it taken.
				if (first == nil) == (second == nil) || !store.IsKind(errors.Join(first, second), store.KindExists) {
					t.Errorf("two additions of one name: %v, %v", first, second)
				}
			} else if first != nil || second != nil {
				t.Errorf("syncs: %v, %v", first, second)
			}
			config, err := Load(f.dir)
			if err != nil || config.Default != "work" {
				t.Errorf("the default set meanwhile: %+v, %v", config, err)
			}
			if _, ok := config.Find("third"); ok != (name == "an addition") {
				t.Errorf("the configuration after %s: %+v", name, config)
			}
			if name == "an addition" {
				if err := r.Remove(ctx, "third", ""); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// A request made for the project a store's name meant reaches no project
// connected under that name since.
func TestBackendRefusesAStoreConnectedAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const second = "44444444-4444-4444-8444-444444444444"
	f.fake.Projects = append(f.fake.Projects, bitwardentest.ProjectInfo{ID: second, Name: "Other"})
	r := f.registry()
	first := bitwardenToken()
	first.Project = bitwardentest.Project
	was, err := r.Add(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Backend(ctx, "bitwarden", was.Target()); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, "bitwarden", ""); err != nil {
		t.Fatal(err)
	}
	again := bitwardenToken()
	again.Project = second
	now, err := r.Add(ctx, again)
	if err != nil || now.ID != was.ID || now.Target() == was.Target() {
		t.Fatalf("connected again: %+v, %v", now, err)
	}
	for _, registry := range []*Registry{r, f.registry()} {
		if _, err := registry.Backend(ctx, "bitwarden", was.Target()); !store.IsKind(err, store.KindConflict) {
			t.Errorf("a request for the earlier project: %v, want conflict", err)
		}
		if _, err := registry.Backend(ctx, "bitwarden", ""); !store.IsKind(err, store.KindConflict) {
			t.Errorf("a request that says no project: %v, want conflict", err)
		}
		if _, err := registry.Backend(ctx, "bitwarden", now.Target()); err != nil {
			t.Errorf("a request for the project connected now: %v", err)
		}
	}
}

func TestRemoveRejectsReboundConnection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	connection, err := r.Add(ctx, bitwardenToken())
	if err != nil {
		t.Fatal(err)
	}
	old := connection
	old.Project = "old-project"
	if err := r.Remove(ctx, old.ID, old.Target()); !store.IsKind(err, store.KindConflict) {
		t.Fatalf("remove with a stale target: %v, want conflict", err)
	}
	config, err := Load(f.dir)
	if err != nil || !slices.Equal(config.Stores, []Connection{connection}) || len(f.local[tokenAccount(connection.ID)]) == 0 {
		t.Fatalf("the connection or its token disappeared: %+v, %v", config, err)
	}
}

// failingDelete fails the removal of one Keychain item.
type failingDelete struct {
	memoryLocal
	account string
}

func (l failingDelete) LocalDelete(ctx context.Context, account string) error {
	if account == l.account {
		return &store.Error{Kind: store.KindKeychain, Message: "synthetic Keychain failure"}
	}
	return l.memoryLocal.LocalDelete(ctx, account)
}

// A removal that stops after the token went says so, and leaves the store
// named, so that removing it again finishes.
func TestRemoveReportsAStepThatFailed(t *testing.T) {
	for step, obstruct := range map[string]func(t *testing.T, f *fixture, r *Registry) (clear func()){
		"its entries' program access could not be removed": func(_ *testing.T, f *fixture, r *Registry) func() {
			r.local = failingDelete{f.local, "grants/bitwarden"}
			return func() { r.local = f.local }
		},
		"the copy of its entries kept on this Mac could not be deleted": func(t *testing.T, f *fixture, _ *Registry) func() {
			parent := filepath.Join(f.dir, "stores")
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			restore := func() { os.Chmod(parent, 0o700) }
			t.Cleanup(restore)
			return restore
		},
	} {
		t.Run(step, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			r := f.registry()
			if _, err := r.Add(ctx, bitwardenToken()); err != nil {
				t.Fatal(err)
			}
			backend, _ := r.Backend(ctx, "bitwarden", target)
			if err := backend.Add(ctx, "a/B", []byte("value"), store.Details{}); err != nil {
				t.Fatal(err)
			}
			clear := obstruct(t, f, r)
			err := r.Remove(ctx, "bitwarden", "")
			want := "the store's access token was removed, but " + step + "; remove the store again"
			if err == nil || err.Error() != want {
				t.Fatalf("a removal that did not finish: %v, want %q", err, want)
			}
			if _, kept := f.local["token/bitwarden"]; kept {
				t.Error("the token stayed")
			}
			if config, _ := Load(f.dir); len(config.Stores) != 1 {
				t.Error("the store whose removal did not finish is no longer named")
			}
			clear()
			if err := r.Remove(ctx, "bitwarden", ""); err != nil {
				t.Fatalf("removing it again: %v", err)
			}
			if config, _ := Load(f.dir); len(config.Stores) != 0 {
				t.Error("the store stayed")
			}
			if _, err := os.Stat(filepath.Join(f.dir, "stores", "bitwarden")); err == nil {
				t.Error("the saved copy stayed")
			}
		})
	}
}
