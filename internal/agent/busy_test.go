//go:build darwin && arm64

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/bitwarden"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// named is a service whose stores are backends by name, "" for iCloud
// Keychain.
type named struct {
	only
	backends map[string]store.Backend
}

func (n named) Backend(_ context.Context, id, _ string) (store.Backend, error) {
	return n.backends[id], nil
}

// Many requests stuck in one slow store hold back neither another store nor
// iCloud Keychain.
func TestServeAnswersOtherStoresWhileOneIsSlow(t *testing.T) {
	const stuck = 8
	slow, other, icloud := storetest.New(), storetest.New(), storetest.New()
	for _, backend := range []*storetest.Backend{slow, other, icloud} {
		backend.Put("fixture/NAME", []byte("synthetic"))
	}
	entered, release := make(chan struct{}, stuck), make(chan struct{})
	slow.Before = func(op, _ string) error {
		entered <- struct{}{}
		<-release
		return nil
	}
	registry := named{backends: map[string]store.Backend{"slow": slow, "other": other, "": icloud}}
	path, _, _ := startTestRegistry(t, registry, new(storetest.Recorder), nil, nil, notReplaced)
	defer close(release)
	read := func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := (&Client{path: path}).remote(ctx, request{Op: "read", Store: id, Accounts: []string{"fixture/NAME"}})
		return err
	}
	for range stuck {
		go read("slow")
	}
	timeout := time.After(5 * time.Second)
wait:
	for served := range stuck {
		select {
		case <-entered:
		case <-timeout:
			t.Errorf("only %d of %d requests to the slow store were served", served, stuck)
			break wait
		}
	}
	for _, id := range []string{"other", ""} {
		if err := read(id); err != nil {
			t.Errorf("a read of %q while the store slow was stuck: %v", id, err)
		}
	}
}

// waitingForTurn is a store whose listing waits for turn, as a remote
// store's listing waits for its turn behind a slow request, and says on
// waiting that it began to.
type waitingForTurn struct {
	*storetest.Backend
	turn    *bitwarden.Turn
	waiting chan struct{}
}

func (b waitingForTurn) Items(ctx context.Context) ([]store.Item, error) {
	b.waiting <- struct{}{}
	if err := b.turn.Take(ctx); err != nil {
		return nil, err
	}
	defer b.turn.Release()
	return b.Backend.Items(ctx)
}

// listed waits until the store's listing began to wait for its turn.
func (b waitingForTurn) listed(t *testing.T) {
	t.Helper()
	select {
	case <-b.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("the grant did not list its store")
	}
}

// slowStore is a store with one entry whose listing waits behind an earlier
// request, released at the test's end.
func slowStore(t *testing.T) waitingForTurn {
	t.Helper()
	turn := bitwarden.NewTurn()
	if err := turn.Take(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := waitingForTurn{storetest.New(), turn, make(chan struct{}, 1)}
	backend.Put("fixture/NAME", []byte("synthetic"))
	return backend
}

// A grant withdrawn while its store is still listed ends then, with nothing
// written, rather than when the store answers.
func TestGrantWithdrawnWhileWaitingForTheStore(t *testing.T) {
	backend := slowStore(t)
	recorder := new(storetest.Recorder)
	approve := func(context.Context, string) (string, error) { return "dialog", nil }
	client, decoder, _ := startRequest(t, backend, recorder, approve, request{Op: "grant", Accounts: []string{"fixture/NAME"}})
	t.Cleanup(backend.turn.Release)
	backend.listed(t)
	if err := json.NewEncoder(client).Encode(withdrawal{Cancel: true}); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var result response
	if err := decoder.Decode(&result); err != nil || result.Error == nil || result.Error.Kind != store.KindCancelled {
		t.Fatalf("answer to the withdrawal: %+v, %v", result, err)
	}
	if items, _ := backend.Backend.Items(context.Background()); items[0].ProgramAccess {
		t.Error("a withdrawn change was written")
	}
	if records := recorder.Records(); len(records) != 1 || records[0].Result != "cancelled" {
		t.Errorf("records %+v, want the withdrawn grant", records)
	}
}

// The service's stop ends a grant still waiting for its store, which has
// written nothing, rather than wait for the store.
func TestShutdownEndsAGrantWaitingForTheStore(t *testing.T) {
	backend := slowStore(t)
	approve := func(context.Context, string) (string, error) { return "dialog", nil }
	path, stop, finished := startTestService(t, backend, new(storetest.Recorder), nil, approve, notReplaced)
	t.Cleanup(backend.turn.Release)
	changed := make(chan error, 1)
	go func() {
		_, err := (&Client{path: path}).remote(context.Background(), request{Op: "grant", Accounts: []string{"fixture/NAME"}})
		changed <- err
	}()
	backend.listed(t)
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service waited for the store of a grant not yet approved")
	}
	if err := <-changed; !store.IsKind(err, store.KindCancelled) {
		t.Errorf("the grant: %v, want cancelled", err)
	}
}

// contexts keeps how each listing and change of program access that reaches
// it could end.
type contexts struct {
	*storetest.Backend
	seen []string
}

func ending(ctx context.Context) string {
	_, deadline := ctx.Deadline()
	return fmt.Sprintf("deadline=%v ends=%v", deadline, ctx.Done() != nil)
}

func (c *contexts) Items(ctx context.Context) ([]store.Item, error) {
	c.seen = append(c.seen, "list "+ending(ctx))
	return c.Backend.Items(ctx)
}

func (c *contexts) SetProgramAccess(ctx context.Context, account, id string, granted bool) error {
	c.seen = append(c.seen, "change "+ending(ctx))
	return c.Backend.SetProgramAccess(ctx, account, id, granted)
}

// A grant to a remote store has the limit of other requests to it until the
// owner approves; neither the owner's answer nor the change approved has a
// limit, and nothing ends the change.
func TestRemoteGrantIsLimitedUntilApproved(t *testing.T) {
	backend := &contexts{Backend: storetest.New()}
	backend.Put("fixture/NAME", []byte("synthetic"))
	approve := func(ctx context.Context, _ string) (string, error) {
		backend.seen = append(backend.seen, "approve "+ending(ctx))
		return "dialog", nil
	}
	_, decoder, _ := startRequest(t, backend, new(storetest.Recorder), approve, request{Op: "grant", Store: "work", Accounts: []string{"fixture/NAME"}})
	var result response
	if err := decoder.Decode(&result); err != nil || !result.OK {
		t.Fatalf("grant: %+v, %v", result, err)
	}
	want := []string{"list deadline=true ends=true", "approve deadline=false ends=true", "change deadline=false ends=false"}
	if !slices.Equal(backend.seen, want) {
		t.Errorf("contexts %q, want %q", backend.seen, want)
	}
}
