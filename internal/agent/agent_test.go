package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// A service that rejects the client closes before its hello; the write was
// not sent, so it is unavailable rather than uncertain.
func TestExchangeSendsNothingBeforeHello(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer client.Close()
	received := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(server)
		received <- data
	}()
	go func() {
		time.Sleep(50 * time.Millisecond)
		server.Close()
	}()
	_, err := exchange(ctx, client, request{Op: "add", Account: "test/NAME", Value: []byte("synthetic-value")})
	client.Close()
	if !store.IsKind(err, store.KindUnavailable) {
		t.Fatalf("closed before hello: %v", err)
	}
	if data := <-received; len(data) != 0 {
		t.Fatal("a request was sent before the hello")
	}
}

// Once a request is sent, a lost response or a cancellation leaves a write
// uncertain; a read is unavailable or cancelled.
func TestExchangeAfterRequestSent(t *testing.T) {
	add := request{Op: "add", Account: "test/NAME", Value: []byte("synthetic-new-value")}
	read := request{Op: "read", Accounts: []string{"test/NAME"}}
	for _, tc := range []struct {
		name   string
		req    request
		cancel bool
		want   store.Kind
	}{
		{"add lost", add, false, store.KindUncertain},
		{"update lost", request{Op: "update", Account: "test/NAME", Value: []byte("synthetic-new-value")}, false, store.KindUncertain},
		{"delete lost", request{Op: "delete", Account: "test/NAME"}, false, store.KindUncertain},
		{"read lost", read, false, store.KindUnavailable},
		{"add cancelled", add, true, store.KindUncertain},
		{"read cancelled", read, true, store.KindCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := storetest.New()
			if tc.req.Op != "add" {
				backend.Put("test/NAME", []byte("synthetic-old-value"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer client.Close()
			executed := make(chan bool, 1)
			go func() {
				// The connection closes without a response, after any
				// cancellation.
				defer server.Close()
				var req request
				if json.NewEncoder(server).Encode(hello{}) != nil || json.NewDecoder(server).Decode(&req) != nil {
					executed <- false
					return
				}
				executed <- dispatch(ctx, backend, nil, req).OK
				if tc.cancel {
					cancel()
				}
			}()
			if _, err := exchange(ctx, client, tc.req); !store.IsKind(err, tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if !<-executed {
				t.Fatal("the request was not received and carried out")
			}
		})
	}
}

func TestAgentRoundTrip(t *testing.T) {
	backend := storetest.New()
	account := "test/NAME"
	first := []byte(" \t synthetic-line-1\nsynthetic-line-2\r\n \t")
	second := []byte("\r\n\t synthetic-replacement \n ")
	for _, test := range []struct {
		op    string
		value []byte
	}{{"add", first}, {"update", second}} {
		if _, err := agentRoundTrip(t, backend, request{Op: test.op, Account: account, Value: test.value}); err != nil {
			t.Fatal(err)
		}
		result, err := agentRoundTrip(t, backend, request{Op: "read", Accounts: []string{account}})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.Values, map[string][]byte{account: test.value}) {
			t.Fatal("the service changed value bytes")
		}
	}
	// An edit without a value keeps the stored one.
	details := store.Details{Kind: store.ValuePassword, Notes: "synthetic notes\nsecond line"}
	if _, err := agentRoundTrip(t, backend, request{Op: "edit", Account: account, To: "test/OTHER", Details: &details}); err != nil {
		t.Fatal(err)
	}
	account = "test/OTHER"
	result, err := agentRoundTrip(t, backend, request{Op: "read", Accounts: []string{account}})
	if err != nil || !reflect.DeepEqual(result.Values, map[string][]byte{account: second}) {
		t.Fatalf("read after the edit: %v", err)
	}
	modified := time.Date(2026, 10, 30, 13, 7, 0, 0, time.UTC)
	backend.Now = func() time.Time { return modified }
	if _, err := agentRoundTrip(t, backend, request{Op: "update", Account: account, Value: second}); err != nil {
		t.Fatal(err)
	}
	result, err = agentRoundTrip(t, backend, request{Op: "items"})
	if err != nil || len(result.Items) != 1 || result.Items[0].Account != account || result.Items[0].Details != details || !result.Items[0].Modified.Equal(modified) {
		t.Fatalf("items: %+v, %v", result.Items, err)
	}
	if _, err := agentRoundTrip(t, backend, request{Op: "delete", Account: account}); err != nil {
		t.Fatal(err)
	}
	result, err = agentRoundTrip(t, backend, request{Op: "items"})
	if err != nil || len(result.Items) != 0 {
		t.Fatalf("items after delete: %v, %v", result.Items, err)
	}
}

func TestAgentRejectsInvalidRequests(t *testing.T) {
	for _, test := range []struct {
		name string
		req  request
	}{
		{"unsupported operation", request{Op: "export"}},
		{"missing reference separator", request{Op: "add", Account: "test", Value: []byte("synthetic-value")}},
		{"invalid name", request{Op: "delete", Account: "test/NOT-VALID"}},
		{"invalid reference after a valid read", request{Op: "read", Accounts: []string{"test/NAME", "bad/name/extra"}}},
		{"empty value", request{Op: "add", Account: "test/NAME"}},
		{"NUL value", request{Op: "update", Account: "test/NAME", Value: []byte("synthetic\x00value")}},
		{"invalid edit target", request{Op: "edit", Account: "test/NAME", To: "test/NOT-VALID"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := storetest.New()
			var calls atomic.Int32
			backend.Before = func(string, string) error { calls.Add(1); return nil }
			_, err := agentRoundTrip(t, backend, test.req)
			if !store.IsKind(err, store.KindInvalid) {
				t.Fatalf("invalid request: %v", err)
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("invalid request called the backend %d times", got)
			}
		})
	}
}

func TestAgentErrorResponses(t *testing.T) {
	t.Run("typed error", func(t *testing.T) {
		want := &store.Error{Kind: store.KindLocked, Ref: &store.Ref{Namespace: "test", Name: "NAME"}, OSStatus: -25308, Message: "locked"}
		backend := storetest.New()
		backend.Before = func(string, string) error { return fmt.Errorf("wrapped: %w", want) }
		_, err := agentRoundTrip(t, backend, request{Op: "read", Accounts: []string{"test/NAME"}})
		var got *store.Error
		if !errors.As(err, &got) || !reflect.DeepEqual(got, want) {
			t.Fatalf("typed response: %v, want %v", err, want)
		}
	})
	t.Run("ordinary error is sanitized", func(t *testing.T) {
		value := "synthetic-value-must-not-appear"
		backend := storetest.New()
		backend.Before = func(string, string) error { return errors.New("backend failed with " + value) }
		_, err := agentRoundTrip(t, backend, request{Op: "add", Account: "test/NAME", Value: []byte(value)})
		var got *store.Error
		if !errors.As(err, &got) || got.Kind != store.KindKeychain {
			t.Fatalf("unexpected sanitized response: %v", err)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatal("the error contains an input value")
		}
	})
}

func TestClientDirectRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := New(storetest.New())
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

func TestClientFallbackOnlyUnavailable(t *testing.T) {
	ctx := context.Background()
	for _, method := range []struct {
		name string
		call func(*Client) error
	}{
		{"items", func(c *Client) error { _, err := c.Items(ctx); return err }},
		{"read", func(c *Client) error { _, err := c.Read(ctx, []string{"test/NAME"}); return err }},
		{"add", func(c *Client) error { return c.Add(ctx, "test/NAME", []byte("synthetic-value"), store.Details{}) }},
		{"update", func(c *Client) error { return c.Update(ctx, "test/NAME", []byte("synthetic-value")) }},
		{"edit", func(c *Client) error { return c.Edit(ctx, "test/NAME", "test/OTHER", nil, nil) }},
		{"delete", func(c *Client) error { return c.Delete(ctx, "test/NAME") }},
	} {
		// Every other kind takes uncertain's path; falling back on it would
		// repeat a write.
		for _, kind := range []store.Kind{store.KindUnavailable, store.KindUncertain} {
			t.Run(method.name+"/"+string(kind), func(t *testing.T) {
				want := &store.Error{Kind: kind, Message: "direct backend error"}
				backend := storetest.New()
				calls := 0
				backend.Before = func(op, account string) error { calls++; return want }
				client := New(backend)
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

func agentRoundTrip(t *testing.T, backend store.Backend, req request) (response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serveRequest(ctx, server, backend, func(context.Context) error { return nil })
	}()
	defer func() {
		client.Close()
		cancel()
		<-done
	}()
	return exchange(ctx, client, req)
}
