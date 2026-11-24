package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// only is a service whose every store is one backend.
type only struct{ backend store.Backend }

var errNoStores = errors.New("this test service has no stores to change")

func (o only) Backend(context.Context, string, string) (store.Backend, error) {
	return o.backend, nil
}
func (only) Config(context.Context) (stores.Config, error) { return stores.Config{}, errNoStores }
func (only) Add(context.Context, stores.Addition) (stores.Connection, error) {
	return stores.Connection{}, errNoStores
}
func (only) SetToken(context.Context, string, []byte) error { return errNoStores }
func (only) Remove(context.Context, string) error           { return errNoStores }
func (only) SetDefault(context.Context, string) error       { return errNoStores }
func (only) Sync(context.Context, string) error             { return errNoStores }

// twoStores is a service with iCloud Keychain and one remote store named
// work, which lists the changes asked of its stores.
type twoStores struct {
	icloud, work store.Backend
	changes      []string
}

// workStore is the connection twoStores has under that name.
var workStore = stores.Connection{ID: "work", Type: stores.Bitwarden, Region: "us", Organization: "org", Project: "project", ProjectName: "Work"}

func (s *twoStores) Config(context.Context) (stores.Config, error) {
	return stores.Config{Default: "work", Stores: []stores.Connection{workStore}}, nil
}

func (s *twoStores) Backend(_ context.Context, id, target string) (store.Backend, error) {
	switch {
	case id == "" || id == stores.ICloud:
		return s.icloud, nil
	case id == "work" && target == workStore.Target():
		return s.work, nil
	case id == "work":
		return nil, &store.Error{Kind: store.KindConflict, Message: "the store was connected to another project"}
	}
	return nil, &store.Error{Kind: store.KindNotFound, Message: "this Mac has no store with that name"}
}

func (s *twoStores) Add(_ context.Context, addition stores.Addition) (stores.Connection, error) {
	s.changes = append(s.changes, "add "+addition.ID+" "+string(addition.Token))
	return stores.Connection{ID: addition.ID, Type: addition.Type}, nil
}

func (s *twoStores) SetToken(_ context.Context, id string, token []byte) error {
	s.changes = append(s.changes, "token "+id+" "+string(token))
	return nil
}

func (s *twoStores) Remove(_ context.Context, id string) error {
	s.changes = append(s.changes, "remove "+id)
	return nil
}

func (s *twoStores) SetDefault(_ context.Context, id string) error {
	s.changes = append(s.changes, "default "+id)
	return nil
}

func (s *twoStores) Sync(_ context.Context, id string) error {
	s.changes = append(s.changes, "sync "+id)
	return nil
}

// witness keeps whether each listing that reaches it had a deadline.
type witness struct {
	*storetest.Backend
	deadlines []bool
}

func (w *witness) Items(ctx context.Context) ([]store.Item, error) {
	_, limited := ctx.Deadline()
	w.deadlines = append(w.deadlines, limited)
	return w.Backend.Items(ctx)
}

// A request reaches the one store it names, with that store's name on its
// records and a limit on how long a remote store may take.
func TestDispatchRoutesByStore(t *testing.T) {
	logs := t.TempDir()
	log := activity.New(logs, func(err error) { t.Error(err) })
	t.Cleanup(func() { log.Remove() })
	icloud := &witness{Backend: storetest.New()}
	work := &witness{Backend: storetest.New()}
	registry := &twoStores{icloud: icloud, work: work}
	ctx := context.Background()
	call := func(req request) response {
		t.Helper()
		if req.Store == "work" {
			req.Target = workStore.Target()
		}
		return dispatch(ctx, registry, log, func(context.Context) error { return nil }, nil, req)
	}

	details := store.Details{Kind: store.ValueToken}
	for _, id := range []string{"", "work"} {
		if r := call(request{Op: "add", Store: id, Account: "app/TOKEN", Value: []byte("synthetic-" + id), Details: &details}); !r.OK {
			t.Fatalf("add to %q: %+v", id, r.Error)
		}
		if r := call(request{Op: "items", Store: id}); !r.OK || len(r.Items) != 1 {
			t.Fatalf("items of %q: %+v", id, r)
		}
	}
	if value, _ := work.Read(ctx, []string{"app/TOKEN"}); string(value["app/TOKEN"].Value) != "synthetic-work" {
		t.Errorf("the store work holds %q", value["app/TOKEN"].Value)
	}
	if value, _ := icloud.Read(ctx, []string{"app/TOKEN"}); string(value["app/TOKEN"].Value) != "synthetic-" {
		t.Errorf("iCloud Keychain holds %q", value["app/TOKEN"].Value)
	}
	if !slices.Equal(icloud.deadlines, []bool{false}) || !slices.Equal(work.deadlines, []bool{true}) {
		t.Errorf("deadlines: iCloud Keychain %v, work %v", icloud.deadlines, work.deadlines)
	}
	var records []string
	files, _ := filepath.Glob(filepath.Join(logs, "*"))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(string(data)) {
			var record struct{ Store, Entry string }
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			records = append(records, record.Store+":"+record.Entry)
		}
	}
	if !slices.Equal(records, []string{":app/TOKEN", "work:app/TOKEN"}) {
		t.Errorf("records %v", records)
	}

	// Only the window reveals, copies, or edits, and its reads come through
	// the service only for a remote store.
	for _, via := range []string{"reveal", "copy", "edit"} {
		read := request{Op: "read", Accounts: []string{"app/TOKEN"}, Via: via, Window: true}
		if r := call(read); r.Error == nil || r.Error.Kind != store.KindInvalid {
			t.Errorf("a read of iCloud Keychain for %s: %+v", via, r.Error)
		}
		read.Store = "work"
		if r := call(read); !r.OK || len(r.Values) != 1 {
			t.Errorf("a read of the store work for %s: %+v", via, r.Error)
		}
		read.Window = false
		if r := call(read); r.Error == nil || r.Error.Kind != store.KindInvalid {
			t.Errorf("mk's read of the store work for %s: %+v", via, r.Error)
		}
	}
	if r := call(request{Op: "read", Store: "work", Accounts: []string{"app/TOKEN"}, Via: "export"}); r.Error == nil {
		t.Error("a read for an unknown purpose was performed")
	}
}

func TestDispatchChangesStores(t *testing.T) {
	registry := &twoStores{}
	call := func(req request) response {
		return dispatch(context.Background(), registry, new(storetest.Recorder), nil, nil, req)
	}
	if r := call(request{Op: "store_add"}); r.Error == nil || r.Error.Kind != store.KindInvalid {
		t.Errorf("store_add without a store: %+v", r)
	}
	added := call(request{Op: "store_add", Connection: &stores.Addition{ID: "work", Type: stores.Bitwarden, Token: []byte("synthetic-token")}})
	if !added.OK || added.Connection == nil || added.Connection.ID != "work" {
		t.Errorf("store_add: %+v", added)
	}
	if r := call(request{Op: "store_token", Store: "work", Value: []byte("synthetic-renewed")}); !r.OK {
		t.Errorf("store_token: %+v", r.Error)
	}
	for _, op := range []string{OpDefaultStore, OpSyncStore, OpRemoveStore} {
		if r := call(request{Op: op, Store: "work"}); !r.OK {
			t.Errorf("%s: %+v", op, r.Error)
		}
	}
	if want := []string{"add work synthetic-token", "token work synthetic-renewed", "default work", "sync work", "remove work"}; !slices.Equal(registry.changes, want) {
		t.Errorf("changes %v, want %v", registry.changes, want)
	}
}

// A request from the window asks for the window's approval and is recorded
// as the window's, where mk's names the program that started mk.
func TestWindowRequestsAreTheWindows(t *testing.T) {
	starter := activity.Caller{Exe: "/bin/starter", Chain: []string{}}
	window := activity.Caller{Exe: "/Applications/Maskey.app/Contents/MacOS/maskey", Chain: []string{}}
	for _, fromWindow := range []bool{false, true} {
		backend := storetest.New()
		backend.Put("test/NAME", []byte("synthetic-value"))
		var asked []bool
		var callers []string
		approve := func(ctx context.Context, reason string) (string, error) {
			// The owner is told which store's entry it is.
			if want := "grant program access to test/NAME in the store work"; reason != want {
				t.Errorf("reason %q, want %q", reason, want)
			}
			asked = append(asked, FromWindow(ctx))
			return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
		}
		recorder := recorderFunc(func(ctx context.Context, _ ...activity.Record) {
			callers = append(callers, activity.CallerOf(ctx).Exe)
		})
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			serveRequest(activity.WithCaller(context.Background(), starter), server, func() activity.Caller { return window }, only{backend}, recorder, nil, approve)
		}()
		decoder := json.NewDecoder(client)
		if err := decoder.Decode(&hello{}); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(client).Encode(request{Op: "grant", Store: "work", Target: workStore.Target(), Accounts: []string{"test/NAME"}, Window: fromWindow}); err != nil {
			t.Fatal(err)
		}
		var r response
		if err := decoder.Decode(&r); err != nil || r.Error == nil || r.Error.Kind != store.KindCancelled {
			t.Fatalf("window=%v: %+v, %v", fromWindow, r, err)
		}
		client.Close()
		<-done
		want := starter.Exe
		if fromWindow {
			want = window.Exe
		}
		if !slices.Equal(asked, []bool{fromWindow}) || !slices.Equal(callers, []string{want}) {
			t.Errorf("window=%v: approvals asked as the window's %v, refusal recorded for %v", fromWindow, asked, callers)
		}
	}
}

type recorderFunc func(ctx context.Context, records ...activity.Record)

func (f recorderFunc) Record(ctx context.Context, records ...activity.Record) { f(ctx, records...) }
