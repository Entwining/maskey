package agent

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func outcomes(recorder *storetest.Recorder) []string {
	var lines []string
	for _, r := range recorder.Records() {
		lines = append(lines, r.Op+" "+r.Entry+" "+r.Result)
	}
	return lines
}

func unreachable() *storetest.Backend {
	backend := storetest.New()
	backend.Before = func(string, string) error {
		return &store.Error{Kind: store.KindUnavailable, Message: "direct backend error"}
	}
	return backend
}

// An operation is recorded once, by the process that ends it: the service
// when it performs what this process could not, and this process when
// neither side could.
func TestClientRecordsWhereTheOperationEnds(t *testing.T) {
	ctx := context.Background()
	inService, inClient := new(storetest.Recorder), new(storetest.Recorder)
	remote := storetest.New()
	remote.Put("test/NAME", []byte("synthetic-value"))
	path, _, _ := startTestService(t, remote, inService, func(context.Context) error { return nil }, nil, notReplaced)
	client := New(unreachable(), inClient, nil, true)
	client.path = path
	if _, err := client.Read(ctx, []string{"test/NAME"}); err != nil {
		t.Fatal(err)
	}
	if got := outcomes(inService); !slices.Equal(got, []string{"read test/NAME ok"}) || len(inClient.Records()) != 0 {
		t.Errorf("the service recorded %v and the client %v", got, outcomes(inClient))
	}

	// A service that answers unavailable has recorded the operation.
	inService, inClient = new(storetest.Recorder), new(storetest.Recorder)
	path, _, _ = startTestService(t, unreachable(), inService, func(context.Context) error { return nil }, nil, notReplaced)
	for _, client := range []*Client{New(unreachable(), inClient, nil, true), NewRemote(workStore, inClient, nil, true)} {
		client.path = path
		if err := client.Delete(ctx, "test/NAME"); !store.IsKind(err, store.KindUnavailable) {
			t.Fatal(err)
		}
	}
	if got := outcomes(inService); !slices.Equal(got, []string{"delete test/NAME unavailable", "delete test/NAME unavailable"}) || len(inClient.Records()) != 0 {
		t.Errorf("the service recorded %v and the client %v", got, outcomes(inClient))
	}

	inClient = new(storetest.Recorder)
	client = New(unreachable(), inClient, nil, true)
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	if err := client.Delete(ctx, "test/NAME"); !store.IsKind(err, store.KindUnavailable) {
		t.Fatal(err)
	}
	if _, err := client.Items(ctx); !store.IsKind(err, store.KindUnavailable) {
		t.Fatal(err)
	}
	if got := outcomes(inClient); !slices.Equal(got, []string{"delete test/NAME unavailable"}) {
		t.Errorf("without either side the client recorded %v", got)
	}

	// A remote store has only the service, and the record names the store.
	record := activity.New(t.TempDir(), func(err error) { t.Error(err) })
	t.Cleanup(func() { record.Remove() })
	work := NewRemote(workStore, record, nil, true)
	work.path = client.path
	_, _ = work.Get(ctx, "test/NAME")
	_, _, _ = work.ChangeProgramAccess(ctx, []store.Ref{{Namespace: "test", Name: "A"}, {Namespace: "test", Name: "B"}}, true)
	lines, _, err := record.Read(time.Time{}, func(activity.Record) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, line := range lines {
		named = append(named, line.Store+":"+line.Op+" "+line.Entry+" "+line.Result)
	}
	slices.Sort(named)
	if want := []string{"work:get test/NAME unavailable", "work:grant test/A unavailable", "work:grant test/B unavailable"}; !slices.Equal(named, want) {
		t.Errorf("recorded %v, want %v", named, want)
	}
}

// The service records an operation on a store it could not open.
func TestDispatchRecordsAStoreThatDoesNotOpen(t *testing.T) {
	recorder := new(storetest.Recorder)
	registry := &twoStores{icloud: storetest.New(), work: storetest.New()}
	r := dispatch(context.Background(), registry, recorder, nil, nil, request{Op: "read", Store: "gone", Accounts: []string{"test/A", "test/B"}, Via: "run"})
	if r.Error == nil || r.Error.Kind != store.KindNotFound {
		t.Fatalf("%+v", r)
	}
	if got := outcomes(recorder); !slices.Equal(got, []string{"read test/A not_found", "read test/B not_found"}) {
		t.Errorf("recorded %v", got)
	}
}

// A replacement crosses the socket as one: the add that finds the entry is
// not recorded, while an add on its own is.
func TestReplacingReachesTheService(t *testing.T) {
	ctx := context.Background()
	recorder := new(storetest.Recorder)
	remote := storetest.New()
	remote.Put("test/NAME", []byte("synthetic-value"))
	path, _, _ := startTestService(t, remote, recorder, func(context.Context) error { return nil }, nil, notReplaced)
	client := NewRemote(workStore, new(storetest.Recorder), nil, true)
	client.path = path
	if _, err := store.New(client, recorder).Set(ctx, store.Ref{Namespace: "test", Name: "NAME"}, []byte("next")); err != nil {
		t.Fatal(err)
	}
	if err := client.Add(ctx, "test/NAME", []byte("other"), store.Details{}); !store.IsKind(err, store.KindExists) {
		t.Fatal(err)
	}
	if got := outcomes(recorder); !slices.Equal(got, []string{"update test/NAME ok", "add test/NAME exists"}) {
		t.Errorf("recorded %v", got)
	}
}

// A request the service received is the service's to record, also when its
// answer is lost; a lost answer to a change of this Mac's stores says so,
// not that an entry needs checking.
func TestExchangeTellsWhatTheServiceReceived(t *testing.T) {
	lose := func(req request) (response, error) {
		client, service := net.Pipe()
		go func() {
			defer service.Close()
			_ = json.NewEncoder(service).Encode(hello{})
			_ = json.NewDecoder(service).Decode(new(request))
		}()
		defer client.Close()
		return exchange(context.Background(), client, req)
	}
	if r, err := lose(request{Op: "get", Account: "test/NAME"}); !store.IsKind(err, store.KindUnavailable) || !r.reached {
		t.Errorf("a read: %v, reached %v", err, r.reached)
	}
	if r, err := lose(request{Op: OpSyncStore, Store: "work"}); !store.IsKind(err, store.KindUncertain) || !r.reached || strings.Contains(err.Error(), "entry") {
		t.Errorf("a sync: %v, reached %v", err, r.reached)
	}
}

type savedBackend struct {
	*storetest.Backend
	saved []bool
}

func (b *savedBackend) Items(ctx context.Context) ([]store.Item, error) {
	b.saved = append(b.saved, store.Saved(ctx))
	return b.Backend.Items(ctx)
}

// Only the listing asked from the saved copy reaches the store as one.
func TestSavedListingReachesTheService(t *testing.T) {
	remote := &savedBackend{Backend: storetest.New()}
	path, _, _ := startTestService(t, remote, new(storetest.Recorder), func(context.Context) error { return nil }, nil, notReplaced)
	client := NewRemote(workStore, new(storetest.Recorder), nil, true)
	client.path = path
	for _, ctx := range []context.Context{store.WithSaved(context.Background()), context.Background()} {
		if _, err := client.Items(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(remote.saved, []bool{true, false}) {
		t.Errorf("listings reached the store as saved: %v", remote.saved)
	}
}
