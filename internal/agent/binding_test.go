package agent

import (
	"context"
	"net"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// A process that may not read this Mac's stores gets them from the service.
func TestClientReadsTheStoresThroughTheService(t *testing.T) {
	registry := &twoStores{icloud: storetest.New(), work: storetest.New()}
	path, _, _ := startTestRegistry(t, registry, new(storetest.Recorder), nil, nil, notReplaced)
	client := New(unreachable(), new(storetest.Recorder), nil, false)
	client.path = path
	config, err := client.Stores(context.Background())
	if err != nil || config.Default != "work" || !slices.Equal(config.Stores, []stores.Connection{workStore}) {
		t.Fatalf("stores %+v, %v", config, err)
	}

	path, _, _ = startTestService(t, storetest.New(), new(storetest.Recorder), nil, nil, notReplaced)
	client.path = path
	if config, err := client.Stores(context.Background()); err == nil || config.Default != stores.ICloud {
		t.Errorf("stores the service cannot read: %+v, %v", config, err)
	}
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	if _, err := client.Stores(context.Background()); !store.IsKind(err, store.KindUnavailable) {
		t.Errorf("without a service: %v, want unavailable", err)
	}
}

// A client's requests say which connection its store's name meant, and the
// service performs none for a store that name has come to mean since.
func TestRequestsReachOnlyTheConnectionTheyWereMadeFor(t *testing.T) {
	ctx := context.Background()
	work := storetest.New()
	work.Put("test/NAME", []byte("synthetic-value"))
	registry := &twoStores{icloud: storetest.New(), work: work}
	inService := new(storetest.Recorder)
	approve := func(context.Context, string) (string, error) { return "dialog", nil }
	path, _, _ := startTestRegistry(t, registry, inService, nil, approve, notReplaced)

	current := NewRemote(workStore, new(storetest.Recorder), nil, true)
	current.path = path
	if items, err := current.Items(ctx); err != nil || len(items) != 1 {
		t.Fatalf("the connection as it is: %v, %v", items, err)
	}

	earlier := workStore
	earlier.Project = "another-project"
	stale := NewRemote(earlier, new(storetest.Recorder), nil, true)
	stale.path = path
	ref := store.Ref{Namespace: "test", Name: "NAME"}
	if err := stale.Delete(ctx, "test/NAME"); !store.IsKind(err, store.KindConflict) {
		t.Errorf("a delete for the earlier connection: %v, want conflict", err)
	}
	if err := stale.Update(ctx, "test/NAME", []byte("synthetic-next")); !store.IsKind(err, store.KindConflict) {
		t.Errorf("an update for the earlier connection: %v, want conflict", err)
	}
	if _, err := stale.Read(ctx, []string{"test/NAME"}); !store.IsKind(err, store.KindConflict) {
		t.Errorf("a read for the earlier connection: %v, want conflict", err)
	}
	if _, _, err := stale.ChangeProgramAccess(ctx, []store.Ref{ref}, true); !store.IsKind(err, store.KindConflict) {
		t.Errorf("a grant for the earlier connection: %v, want conflict", err)
	}
	values, _ := work.Read(ctx, []string{"test/NAME"})
	items, _ := work.Items(ctx)
	if string(values["test/NAME"].Value) != "synthetic-value" || len(items) != 1 || items[0].ProgramAccess {
		t.Errorf("the store now holds %q, %+v", values["test/NAME"].Value, items)
	}
	want := []string{"delete test/NAME conflict", "update test/NAME conflict", "read test/NAME conflict", "grant test/NAME conflict"}
	if got := outcomes(inService); !slices.Equal(got, want) {
		t.Errorf("the service recorded %v, want %v", got, want)
	}
}

// refusing is the socket of a service that ended without removing it, as
// one that restarts does: a client waits there for the next one.
func refusing(t *testing.T) string {
	t.Helper()
	path := filepath.Join(serviceTempDir(t), "service.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A request cancelled before it was sent, as while the client waits for
// the service to restart, reached no one who would record it, so the client
// does.
func TestClientRecordsARequestCancelledBeforeItWasSent(t *testing.T) {
	path := refusing(t)
	recorder := new(storetest.Recorder)
	remote := NewRemote(workStore, recorder, nil, true)
	icloud := New(unreachable(), recorder, nil, true)
	sandboxed := New(unreachable(), recorder, nil, false)
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		return ctx
	}
	for _, client := range []*Client{remote, icloud, sandboxed} {
		client.path = path
	}
	refs := []store.Ref{{Namespace: "test", Name: "A"}, {Namespace: "test", Name: "B"}}
	if _, _, err := remote.ChangeProgramAccess(cancelled(), refs, true); !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("a grant: %v, want cancelled", err)
	}
	if err := icloud.Delete(cancelled(), "test/NAME"); !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("a delete: %v, want cancelled", err)
	}
	// A listing is not recorded, and a process that cannot write the record
	// records nothing.
	if _, err := remote.Items(cancelled()); !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("a listing: %v, want cancelled", err)
	}
	if err := sandboxed.Delete(cancelled(), "test/NAME"); !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("a sandboxed delete: %v, want cancelled", err)
	}
	want := []string{"grant test/A cancelled", "grant test/B cancelled", "delete test/NAME cancelled"}
	if got := outcomes(recorder); !slices.Equal(got, want) {
		t.Errorf("the client recorded %v, want %v", got, want)
	}
}

// Once the service received a request, the service alone records it, also
// when the client is cancelled before the answer.
func TestClientLeavesASentRequestToTheService(t *testing.T) {
	inService, inClient := new(storetest.Recorder), new(storetest.Recorder)
	backend := storetest.New()
	backend.Put("test/NAME", []byte("synthetic-value"))
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	backend.Before = func(op, _ string) error {
		if op == "read" {
			cancel()
			<-release
		}
		return nil
	}
	path, _, _ := startTestService(t, backend, inService, nil, nil, notReplaced)
	client := NewRemote(workStore, inClient, nil, true)
	client.path = path
	_, err := client.Read(ctx, []string{"test/NAME"})
	close(release)
	if !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("a read cancelled after it was sent: %v", err)
	}
	if got := outcomes(inClient); len(got) != 0 {
		t.Errorf("the client recorded %v", got)
	}
}
