//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// Program access changes only in the service, which holds the owner's
// approval, even where the Keychain is reachable directly.
func TestClientChangesProgramAccessOnlyThroughService(t *testing.T) {
	backend := storetest.New()
	backend.Put("test/NAME", []byte("synthetic-value"))
	calls := 0
	backend.Before = func(string, string) error { calls++; return nil }
	client := New(backend, nil, true)
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	_, _, err := client.ChangeProgramAccess(context.Background(), []store.Ref{{Namespace: "test", Name: "NAME"}}, true)
	if !store.IsKind(err, store.KindUnavailable) || calls != 0 {
		t.Fatalf("without the service: %v after %d direct calls", err, calls)
	}
}

func TestClientDirectRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := New(storetest.New(), nil, true)
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	account := "test/NAME"
	for _, test := range []struct {
		write func(context.Context, string, []byte) error
		value []byte
	}{
		{func(ctx context.Context, account string, value []byte) error {
			return client.Add(ctx, account, value, store.Details{})
		}, []byte("synthetic-value")},
		{client.Update, []byte("synthetic-update")},
	} {
		if err := test.write(ctx, account, test.value); err != nil {
			t.Fatal(err)
		}
		values, err := client.Read(ctx, []string{account})
		if err != nil || !bytes.Equal(values[account], test.value) {
			t.Fatalf("direct read: %v", err)
		}
	}
	items, err := client.Items(ctx)
	if err != nil || len(items) != 1 || items[0].Account != account {
		t.Fatalf("direct items: %v, %v", items, err)
	}
	if err := client.Delete(ctx, account); err != nil {
		t.Fatal(err)
	}
	items, err = client.Items(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("direct items after delete: %v, %v", items, err)
	}
}

// clientCalls are the Client's Keychain operations other than program access.
var clientCalls = []struct {
	name string
	call func(*Client) error
}{
	{"items", func(c *Client) error { _, err := c.Items(context.Background()); return err }},
	{"read", func(c *Client) error { _, err := c.Read(context.Background(), []string{"test/NAME"}); return err }},
	{"get", func(c *Client) error { _, err := c.Get(context.Background(), "test/NAME"); return err }},
	{"add", func(c *Client) error {
		return c.Add(context.Background(), "test/NAME", []byte("synthetic-value"), store.Details{})
	}},
	{"update", func(c *Client) error { return c.Update(context.Background(), "test/NAME", []byte("synthetic-value")) }},
	{"edit", func(c *Client) error { return c.Edit(context.Background(), "test/NAME", "test/OTHER", nil, nil) }},
	{"delete", func(c *Client) error { return c.Delete(context.Background(), "test/NAME") }},
}

// A process that can write the activity record tries the Keychain directly
// first, and uses the service only once that is unavailable.
func TestClientFallbackOnlyUnavailable(t *testing.T) {
	for _, method := range clientCalls {
		// Every other kind takes uncertain's path; falling back on it would
		// repeat a write.
		for _, kind := range []store.Kind{store.KindUnavailable, store.KindUncertain} {
			t.Run(method.name+"/"+string(kind), func(t *testing.T) {
				want := &store.Error{Kind: kind, Message: "direct backend error"}
				backend := storetest.New()
				calls := 0
				backend.Before = func(op, account string) error { calls++; return want }
				client := New(backend, nil, true)
				client.path = filepath.Join(t.TempDir(), "missing.sock")
				got := method.call(client)
				if kind == store.KindUnavailable {
					if !store.IsKind(got, store.KindUnavailable) || got == want || got.Error() != unavailable().Error() {
						t.Fatalf("unavailable did not fall back to the service: %v", got)
					}
				} else if got != want {
					t.Fatalf("direct error was replaced: %v, want %v", got, want)
				}
				if calls != 1 {
					t.Fatalf("direct backend calls = %d, want 1", calls)
				}
				// Only unavailable sends later calls straight to the service.
				method.call(client)
				wantCalls := 2
				if kind == store.KindUnavailable {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("direct backend calls after a second call = %d, want %d", calls, wantCalls)
				}
			})
		}
	}
}

// countedService starts a service whose backend counts the operations it
// performs.
func countedService(t *testing.T, before func(op string) error) (string, *atomic.Int32) {
	t.Helper()
	var served atomic.Int32
	backend := storetest.New()
	backend.Before = func(op, _ string) error {
		served.Add(1)
		return before(op)
	}
	path, _, _ := startTestService(t, backend, nil, func(context.Context) error { return nil }, nil, notReplaced)
	return path, &served
}

// A process that can write the activity record performs every operation
// directly, without asking the service; one that cannot sends each recorded
// operation to the service, which records it, and still lists directly.
func TestClientRoutesByRecordability(t *testing.T) {
	path, served := countedService(t, func(string) error { return nil })
	for _, recordable := range []bool{true, false} {
		for _, method := range clientCalls {
			t.Run(fmt.Sprintf("%s/recordable=%v", method.name, recordable), func(t *testing.T) {
				served.Store(0)
				direct := storetest.New()
				performed := 0
				direct.Before = func(string, string) error { performed++; return nil }
				client := New(direct, nil, recordable)
				client.path = path
				if err := method.call(client); store.IsKind(err, store.KindUnavailable) {
					t.Fatal(err)
				}
				wantDirect := 0
				if recordable || method.name == "items" {
					wantDirect = 1
				}
				if performed != wantDirect || served.Load() != int32(1-wantDirect) {
					t.Fatalf("performed directly %d times and by the service %d times", performed, served.Load())
				}
			})
		}
	}
}

// Where the service cannot be reached, a process that cannot write the
// activity record performs the operation directly, warns with the service's
// failure as the cause, and no longer asks the service.
func TestClientWithoutServicePerformsDirectly(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	direct := recordingReads{storetest.New(), activity.New(filepath.Join(blocker, "Logs"), nil)}
	direct.Put("test/NAME", []byte("synthetic-value"))
	var warnings []error
	client := New(direct, func(err error) { warnings = append(warnings, err) }, false)
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	values, err := client.Read(context.Background(), []string{"test/NAME"})
	if err != nil || !bytes.Equal(values["test/NAME"], []byte("synthetic-value")) || len(direct.Reads) != 1 {
		t.Fatalf("read %q, %v after %d direct reads", values, err, len(direct.Reads))
	}
	if len(warnings) != 1 || !errors.Is(warnings[0], unix.ENOTDIR) || !store.IsKind(warnings[0], store.KindUnavailable) {
		t.Fatalf("warnings %v, want the failure to record with the service's", warnings)
	}
	path, served := countedService(t, func(string) error { return nil })
	client.path = path
	if _, err := client.Read(context.Background(), []string{"test/NAME"}); err != nil || len(direct.Reads) != 2 || served.Load() != 0 {
		t.Fatalf("second read: %v after %d direct reads, %d by the service", err, len(direct.Reads), served.Load())
	}
	if len(warnings) != 2 || !store.IsKind(warnings[1], store.KindUnavailable) {
		t.Fatalf("warnings %v, want the second with the service's failure too", warnings)
	}
}

// A side that answered unavailable stays skipped for the rest of the
// process, whichever side a later operation tries first.
func TestClientRemembersUnavailableSides(t *testing.T) {
	ctx := context.Background()
	direct := storetest.New()
	directCalls := 0
	direct.Before = func(string, string) error {
		directCalls++
		return &store.Error{Kind: store.KindUnavailable, Message: "direct backend error"}
	}
	remote := storetest.New()
	remote.Put("test/NAME", []byte("synthetic-value"))
	path, stop, finished := startTestService(t, remote, nil, func(context.Context) error { return nil }, nil, notReplaced)
	client := New(direct, nil, false)
	client.path = path
	// mk delete lists the entries, then deletes them.
	if items, err := client.Items(ctx); err != nil || len(items) != 1 {
		t.Fatalf("items through the service: %v, %v", items, err)
	}
	stop()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, "test/NAME"); !store.IsKind(err, store.KindUnavailable) {
		t.Fatalf("delete without either side: %v", err)
	}
	path, served := countedService(t, func(string) error { return nil })
	client.path = path
	if _, err := client.Items(ctx); !store.IsKind(err, store.KindUnavailable) {
		t.Fatalf("items after both sides answered unavailable: %v", err)
	}
	if directCalls != 1 || served.Load() != 0 {
		t.Fatalf("tried directly %d times and in a later service %d times", directCalls, served.Load())
	}
}

// Where neither side can be reached, the error is the service's, which
// names what to fix.
func TestClientWithoutEitherSide(t *testing.T) {
	direct := storetest.New()
	direct.Before = func(string, string) error {
		return &store.Error{Kind: store.KindUnavailable, Message: "direct backend error"}
	}
	client := New(direct, nil, false)
	client.path = filepath.Join(t.TempDir(), "missing.sock")
	if _, err := client.Get(context.Background(), "test/NAME"); err == nil || err.Error() != unavailable().Error() {
		t.Fatalf("got %v, want the service's error", err)
	}
}

// The service's answer other than unavailable stands: an uncertain write is
// not repeated directly, and later operations still go to the service.
func TestClientKeepsServiceAnswer(t *testing.T) {
	path, served := countedService(t, func(string) error {
		return &store.Error{Kind: store.KindUncertain, Message: "synthetic uncertain write"}
	})
	direct := storetest.New()
	performed := 0
	direct.Before = func(string, string) error { performed++; return nil }
	client := New(direct, nil, false)
	client.path = path
	for range 2 {
		if err := client.Update(context.Background(), "test/NAME", []byte("synthetic-value")); !store.IsKind(err, store.KindUncertain) {
			t.Fatalf("update: %v", err)
		}
	}
	if performed != 0 || served.Load() != 2 {
		t.Fatalf("performed directly %d times and by the service %d times", performed, served.Load())
	}
}
