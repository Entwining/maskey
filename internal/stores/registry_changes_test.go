package stores

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/bitwarden/bitwardentest"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// hold makes r's requests to a path containing path wait until release is
// called. The test's end releases them and waits for the goroutines it
// started with running, so that none writes into its directory after it.
func hold(t *testing.T, f *fixture, r *Registry, path string) (entered <-chan struct{}, release func(), running *sync.WaitGroup) {
	r.http, entered, release = f.fake.Holding(t, path)
	running = new(sync.WaitGroup)
	t.Cleanup(func() {
		release()
		running.Wait()
	})
	return entered, release, running
}

// await waits until the remote holds the request that entered reports.
func await(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach Bitwarden")
	}
}

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
			entered, release, running := hold(t, f, r, "/projects")
			finished := make(chan error, 2)
			running.Go(func() { finished <- slow(r) })
			await(t, entered)

			others := make(chan error, 1)
			running.Go(func() {
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
			})
			select {
			case err := <-others:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("another store waited for the slow one")
			}

			// The same connection's next change waits for this one.
			running.Go(func() { finished <- slow(r) })
			select {
			case err := <-finished:
				t.Fatalf("a change ended while the connection's request was held: %v", err)
			case <-entered:
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

// Removing a store or replacing its token does not wait for an exchange the
// store's backend is in, and that exchange, once it ends, leaves nothing of
// the old connection on this Mac.
func TestStoreChangesDoNotWaitForBitwarden(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(r *Registry) error{
		"a removal":           func(r *Registry) error { return r.Remove(ctx, "bitwarden", target) },
		"a token replacement": func(r *Registry) error { return r.SetToken(ctx, "bitwarden", target, []byte(bitwardentest.Token)) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			setup := f.registry()
			if _, err := setup.Add(ctx, bitwardenToken()); err != nil {
				t.Fatal(err)
			}
			backend, _ := setup.Backend(ctx, "bitwarden", target)
			if err := backend.Add(ctx, "a/B", []byte("value"), store.Details{}); err != nil {
				t.Fatal(err)
			}
			if err := storetest.SetAccess(ctx, backend, "a/B", true); err != nil {
				t.Fatal(err)
			}
			grants := slices.Clone(f.local["grants/bitwarden"])

			r := f.registry()
			entered, release, running := hold(t, f, r, "/secrets/delete")
			backend, err := r.Backend(ctx, "bitwarden", target)
			if err != nil {
				t.Fatal(err)
			}
			deleted := make(chan error, 1)
			running.Go(func() { deleted <- backend.Delete(ctx, "a/B") })
			await(t, entered)
			changed := make(chan error, 1)
			running.Go(func() { changed <- change(r) })
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s waited for Bitwarden", name)
			}
			release()
			if err := <-deleted; err != nil {
				t.Errorf("the delete Bitwarden performed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.dir, "stores", "bitwarden")); err == nil {
				t.Error("the old connection's snapshot was written after the change")
			}
			kept, ok := f.local["grants/bitwarden"]
			switch {
			case name == "a removal" && ok:
				t.Error("the removed store's grants were written again")
			case name == "a token replacement" && !slices.Equal(kept, grants):
				t.Errorf("the grants the store keeps changed to %s", kept)
			}
		})
	}
}

// A request that waits for another to open or change the same store gives
// up at its deadline or cancellation, having done nothing.
func TestStoreChangeStopsWaitingAtItsDeadline(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.registry().Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	r := f.registry()
	entered, release, running := hold(t, f, r, "/projects")
	replaced := make(chan error, 1)
	running.Go(func() { replaced <- r.SetToken(ctx, "bitwarden", target, []byte(bitwardentest.Token)) })
	await(t, entered)

	deadline, cancelDeadline := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelDeadline()
	cancelled, cancel := context.WithCancel(ctx)
	time.AfterFunc(50*time.Millisecond, cancel)
	for waiting, want := range map[context.Context]store.Kind{deadline: store.KindNetwork, cancelled: store.KindCancelled} {
		removed := make(chan error, 1)
		running.Go(func() { removed <- r.Remove(waiting, "bitwarden", "") })
		select {
		case err := <-removed:
			if !store.IsKind(err, want) || !strings.Contains(err.Error(), "nothing was done") {
				t.Errorf("a removal that gave up: %v, want %s", err, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a removal waited past its deadline")
		}
	}
	release()
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	if config, err := Load(f.dir); err != nil || len(config.Stores) != 1 || len(f.local["token/bitwarden"]) == 0 {
		t.Errorf("after removals that gave up: %+v, %v, token kept %v", config, err, len(f.local["token/bitwarden"]) != 0)
	}
}

// A backend made for a store after a new token closed its earlier one waits
// for the write that one is still in, so that it does not read the project
// before that write lands and make the same entry beside it.
func TestNewBackendWaitsForTheWriteOfTheOneItReplaced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.registry().Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	r := f.registry()
	entered, release, running := hold(t, f, r, "/organizations/"+bitwardentest.Organization+"/secrets")
	earlier, err := r.Backend(ctx, "bitwarden", target)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	running.Go(func() { first <- earlier.Add(ctx, "a/B", []byte("value"), store.Details{}) })
	await(t, entered)
	replaced := make(chan error, 1)
	running.Go(func() { replaced <- r.SetToken(ctx, "bitwarden", target, []byte(bitwardentest.Token)) })
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replacing the token waited for the earlier backend's write")
	}
	backend, err := r.Backend(ctx, "bitwarden", target)
	if err != nil || backend == earlier {
		t.Fatalf("the backend after the new token: %v, the earlier one %v", err, backend == earlier)
	}
	second := make(chan error, 1)
	running.Go(func() { second <- backend.Add(ctx, "a/B", []byte("other"), store.Details{}) })
	select {
	case <-entered:
		t.Fatal("the new backend made the entry beside the earlier one's write")
	case err := <-second:
		t.Fatalf("the new backend's add ended while the earlier one's was in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if err := <-first; err != nil {
		t.Errorf("the earlier backend's add: %v", err)
	}
	if err := <-second; !store.IsKind(err, store.KindExists) {
		t.Errorf("the new backend's add: %v, want exists", err)
	}
	if n := len(f.fake.Order); n != 1 {
		t.Errorf("the project holds %d secrets, want 1", n)
	}
}
