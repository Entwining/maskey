package bitwarden

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

const indexPath = "/projects/" + project + "/secrets"

// holding returns a backend as another process would start it, whose
// requests to a path containing path wait until release is called. The
// test's end releases them and waits for what is still running, so that
// nothing writes into the test's directory after it.
func (s *stack) holding(path string) (backend *Backend, entered <-chan struct{}, release func()) {
	backend = s.open(vectorTokenParsed(s.t))
	backend.client.http, entered, release = s.fake.Holding(s.t, path)
	s.t.Cleanup(func() {
		release()
		s.running.Wait()
	})
	return backend, entered, release
}

// start runs f while the test goes on.
func (s *stack) start(f func() error) <-chan error {
	finished := make(chan error, 1)
	s.running.Go(func() { finished <- f() })
	return finished
}

// await waits until Bitwarden holds the request that entered reports.
func await(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not reach Bitwarden")
	}
}

// answered is what f returns, which it must do while Bitwarden holds a
// request: what f waits for is not that request.
func answered[T any](s *stack, what string, f func() T) T {
	s.t.Helper()
	answer := make(chan T, 1)
	s.running.Go(func() { answer <- f() })
	select {
	case got := <-answer:
		return got
	case <-time.After(5 * time.Second):
		s.t.Fatalf("%s waited for an exchange with Bitwarden", what)
		var zero T
		return zero
	}
}

// A read the snapshot answers does not wait for a write or a sync of the
// same store that waits for Bitwarden.
func TestBackendAnswersFromTheSnapshotWhileBitwardenIsSlow(t *testing.T) {
	ctx := context.Background()
	for name, exchange := range map[string]struct {
		hold func(id string) string
		run  func(*Backend) error
	}{
		"a write's read":    {func(string) string { return indexPath }, func(b *Backend) error { return b.Update(ctx, "a/B", []byte("next")) }},
		"a write's request": {func(id string) string { return "/secrets/" + id }, func(b *Backend) error { return b.Update(ctx, "a/B", []byte("next")) }},
		"a sync":            {func(string) string { return indexPath }, func(b *Backend) error { return b.Refresh(ctx) }},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.add("a/B", "value", store.Details{})
			backend, entered, release := s.holding(exchange.hold(s.fake.Order[0]))
			finished := s.start(func() error { return exchange.run(backend) })
			await(t, entered)
			// Nothing Bitwarden has not confirmed shows.
			read := answered(s, "a read of a fresh snapshot", func() error {
				value, err := s.read(backend, "a/B")
				if err == nil && value != "value" {
					return fmt.Errorf("read %q", value)
				}
				return err
			})
			if read != nil {
				t.Errorf("read: %v", read)
			}
			release()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// byPath sends the requests to a path containing path through matching,
// and the others through other.
type byPath struct {
	path            string
	matching, other http.RoundTripper
}

func (r byPath) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, r.path) {
		return r.matching.RoundTrip(req)
	}
	return r.other.RoundTrip(req)
}

// watched is a context that reports when a request first waits for it to
// end, which a read does only once it waits for the turn.
type watched struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (w *watched) Done() <-chan struct{} {
	w.once.Do(func() { close(w.waiting) })
	return w.Context.Done()
}

// A read waiting behind a write answers from the snapshot the write's read
// installed, without waiting for the write itself.
func TestBackendReadQueuedBehindAWriteAnswersFromItsRead(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	id := s.fake.Order[0]
	s.now = s.now.Add(snapshotFresh)
	backend, indexEntered, releaseIndex := s.holding(indexPath)
	put, putEntered, releasePut := s.fake.Holding(t, "/secrets/"+id)
	backend.client.http = &http.Client{Transport: byPath{indexPath, backend.client.http.Transport, put.Transport}}
	written := s.start(func() error { return backend.Update(ctx, "a/B", []byte("next")) })
	await(t, indexEntered)
	queued := &watched{Context: ctx, waiting: make(chan struct{})}
	read := make(chan error, 1)
	s.running.Go(func() {
		values, err := backend.Read(queued, []string{"a/B"})
		if err == nil && string(values["a/B"].Value) != "value" {
			err = fmt.Errorf("read %q", values["a/B"].Value)
		}
		read <- err
	})
	await(t, queued.waiting)
	releaseIndex()
	await(t, putEntered)
	select {
	case err := <-read:
		if err != nil {
			t.Errorf("the queued read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued read waited for the write")
	}
	releasePut()
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

// Close ends a read waiting behind a refresh with conflict at once, without
// waiting for the refresh.
func TestBackendCloseEndsAQueuedRead(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	s.now = s.now.Add(snapshotFresh)
	backend, entered, release := s.holding(indexPath)
	refreshed := s.start(func() error { return backend.Refresh(ctx) })
	await(t, entered)
	queued := &watched{Context: ctx, waiting: make(chan struct{})}
	read := make(chan error, 1)
	s.running.Go(func() {
		_, err := backend.Read(queued, []string{"a/B"})
		read <- err
	})
	await(t, queued.waiting)
	backend.Close()
	select {
	case err := <-read:
		if !store.IsKind(err, store.KindConflict) {
			t.Errorf("the queued read: %v, want conflict", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queued read waited for the refresh after Close")
	}
	release()
	if err := <-refreshed; !store.IsKind(err, store.KindConflict) {
		t.Errorf("the refresh: %v, want conflict", err)
	}
}

// timelyGrants are grants that, like the Keychain item that holds them, are
// read once a request has time and from memory after.
type timelyGrants struct {
	*memoryGrants
	loaded bool
}

func (g *timelyGrants) Granted(ctx context.Context) ([]string, error) {
	if !g.loaded && ctx.Err() != nil {
		return nil, &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	g.loaded = true
	return g.memoryGrants.Granted(ctx)
}

// A request that gives up waiting for an earlier one to finish with
// Bitwarden has done nothing: a read answers from its snapshot, with the
// grants, as when Bitwarden cannot be reached, and a write fails without
// reaching Bitwarden.
func TestBackendRequestStopsWaitingAtItsDeadline(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	id := s.fake.Order[0]
	if err := storetest.SetAccess(ctx, s.backend, "a/B", true); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(snapshotFresh)
	backend, entered, release := s.holding(indexPath)
	backend.grants = &timelyGrants{memoryGrants: s.grants}
	finished := s.start(func() error { return backend.Refresh(ctx) })
	await(t, entered)

	deadline := func() context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		t.Cleanup(cancel)
		return ctx
	}
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		return ctx
	}
	for name, read := range map[string]func() (string, error){
		"a read": func() (string, error) {
			values, err := backend.Read(deadline(), []string{"a/B"})
			return string(values["a/B"].Value), err
		},
		"a get": func() (string, error) {
			value, err := backend.Get(deadline(), "a/B")
			return string(value), err
		},
		"a listing": func() (string, error) {
			items, err := backend.Items(deadline())
			if err != nil || len(items) != 1 || !items[0].ProgramAccess {
				return "", err
			}
			return "value", nil
		},
	} {
		got := answered(s, name+" past its deadline", func() []any {
			value, err := read()
			return []any{value, err}
		})
		if got[0] != "value" || got[1] != nil {
			t.Errorf("%s past its deadline: %q, %v", name, got[0], got[1])
		}
	}
	for ctx, want := range map[context.Context]store.Kind{deadline(): store.KindNetwork, cancelled(): store.KindCancelled} {
		err := answered(s, "a write that gave up", func() error { return backend.Update(ctx, "a/B", []byte("next")) })
		if !store.IsKind(err, want) || !strings.Contains(err.Error(), "nothing was done") {
			t.Errorf("a write that gave up: %v, want %s", err, want)
		}
	}
	release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if n := s.fake.Count("PUT /secrets/" + id); n != 0 {
		t.Errorf("%d writes that gave up reached Bitwarden", n)
	}
}

// A read that gave up waiting does not keep the reads after it from asking
// Bitwarden: only a refresh that failed starts refreshPause.
func TestBackendReadThatGaveUpStartsNoPause(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	s.now = s.now.Add(snapshotFresh)
	backend, entered, release := s.holding(indexPath)
	// The write's own read fails once Bitwarden answers it.
	s.fake.Fail[indexRoute] = 503
	written := s.start(func() error { return backend.Update(ctx, "a/B", []byte("next")) })
	await(t, entered)
	waited, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := answered(s, "a read past its deadline", func() error {
		values, err := backend.Read(waited, []string{"a/B"})
		if err == nil && string(values["a/B"].Value) != "value" {
			return fmt.Errorf("read %q", values["a/B"].Value)
		}
		return err
	}); err != nil {
		t.Fatalf("a read past its deadline: %v", err)
	}
	release()
	if err := <-written; !store.IsKind(err, store.KindRemote) {
		t.Fatalf("the write: %v, want remote_error", err)
	}
	listings := s.fake.Count(indexRoute)
	if value, err := s.read(backend, "a/B"); value != "value" || err != nil || s.fake.Count(indexRoute) != listings+1 {
		t.Errorf("the next read: %q, %v after %d listings, want one", value, err, s.fake.Count(indexRoute)-listings)
	}
}

// A read decides with the program access as it is once it has the entries:
// a revoke that completed while it waited for Bitwarden ends a get with
// not_granted, and a listing shows the access revoked.
func TestBackendReadsTheGrantsAfterItsWait(t *testing.T) {
	ctx := context.Background()
	for name, read := range map[string]func(*Backend) error{
		"a get": func(b *Backend) error {
			if _, err := b.Get(ctx, "a/B"); !store.IsKind(err, store.KindNotGranted) {
				return fmt.Errorf("got %v, want not_granted", err)
			}
			return nil
		},
		"a listing": func(b *Backend) error {
			items, err := b.Items(ctx)
			if err == nil && (len(items) != 1 || items[0].ProgramAccess) {
				return fmt.Errorf("listed %+v, want the access revoked", items)
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.add("a/B", "value", store.Details{})
			id := s.fake.Order[0]
			if err := storetest.SetAccess(ctx, s.backend, "a/B", true); err != nil {
				t.Fatal(err)
			}
			s.now = s.now.Add(snapshotFresh)
			backend, entered, release := s.holding(indexPath)
			finished := s.start(func() error { return read(backend) })
			await(t, entered)
			// A revoke the saved copy answers waits for no turn.
			revoked := answered(s, "a revoke", func() error {
				return backend.SetProgramAccess(store.WithSaved(ctx), "a/B", id, false)
			})
			if revoked != nil {
				t.Fatal(revoked)
			}
			release()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		})
	}
}

// Close does not wait for an exchange with Bitwarden, which, once it ends,
// leaves neither a snapshot nor a change to the grants behind: the store
// may be gone, or the name another connection's. A write still reports
// what Bitwarden did.
func TestBackendClosedDuringAnExchangeKeepsNothing(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		hold func(id string) string
		run  func(*Backend) error
		want store.Kind
	}{
		"a delete": {func(string) string { return "/secrets/delete" }, func(b *Backend) error { return b.Delete(ctx, "a/B") }, ""},
		"an update": {func(id string) string { return "/secrets/" + id }, func(b *Backend) error {
			return b.Update(ctx, "a/B", []byte("next"))
		}, ""},
		"a read": {func(string) string { return indexPath }, func(b *Backend) error {
			_, err := b.Items(ctx)
			return err
		}, store.KindConflict},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack(t)
			s.add("a/B", "value", store.Details{})
			if err := storetest.SetAccess(ctx, s.backend, "a/B", true); err != nil {
				t.Fatal(err)
			}
			granted := slices.Clone(s.grants.ids)
			s.now = s.now.Add(snapshotFresh)
			backend, entered, release := s.holding(test.hold(s.fake.Order[0]))
			finished := s.start(func() error { return test.run(backend) })
			await(t, entered)
			answered(s, "Close", func() struct{} {
				backend.Close()
				return struct{}{}
			})
			// The store's removal deletes the snapshot after Close.
			if err := os.Remove(s.path); err != nil {
				t.Fatal(err)
			}
			release()
			if err := <-finished; store.KindOf(err) != test.want {
				t.Errorf("the exchange ended with %v, want %q", err, test.want)
			}
			if _, err := os.Stat(s.path); err == nil {
				t.Error("a closed backend wrote its snapshot")
			}
			if !slices.Equal(s.grants.ids, granted) {
				t.Errorf("a closed backend changed the grants to %v", s.grants.ids)
			}
		})
	}
}

// The snapshot is sealed with the session of the exchange it comes from,
// also once that session's bearer expired, without signing in again.
func TestBackendWritesTheSnapshotWithoutSigningIn(t *testing.T) {
	s := newStack(t)
	s.add("a/B", "value", store.Details{})
	s.now = s.now.Add(snapshotFresh)
	backend, entered, release := s.holding(indexPath)
	var later atomic.Bool
	backend.client.now = func() time.Time {
		if later.Load() {
			return time.Now().Add(2 * time.Hour)
		}
		return time.Now()
	}
	finished := s.start(func() error { return backend.Refresh(context.Background()) })
	await(t, entered)
	sessions := s.fake.Sessions
	later.Store(true)
	release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if s.fake.Sessions != sessions {
		t.Errorf("writing the snapshot signed in %d times", s.fake.Sessions-sessions)
	}
	if saved := s.client.readSnapshot(s.path, project); saved == nil || !saved.Refreshed.Equal(s.now) {
		t.Errorf("the snapshot file holds %+v, want the one refreshed at %v", saved, s.now)
	}
}
