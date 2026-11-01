package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/owner"
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

// Once a request is sent, only the service's answer settles a write: a
// cancelled client withdraws a grant or revoke and waits for that answer,
// waits for any other write's, and is uncertain without one. A read is
// cancelled at once, and unavailable without an answer.
func TestExchangeAfterRequestSent(t *testing.T) {
	add := request{Op: "add", Account: "test/NAME", Value: []byte("synthetic-new-value")}
	grant := request{Op: "grant", Accounts: []string{"test/NAME"}}
	read := request{Op: "read", Accounts: []string{"test/NAME"}}
	cancelled := &response{Error: &store.Error{Kind: store.KindCancelled, Message: "cancelled"}}
	changed := &response{OK: true, Changes: []store.AccessChange{{Ref: store.Ref{Namespace: "test", Name: "NAME"}, Changed: true}}, Auth: "dialog"}
	for _, tc := range []struct {
		name   string
		req    request
		cancel bool
		answer *response // nil closes the connection without one
		want   store.Kind
	}{
		{"add lost", add, false, nil, store.KindUncertain},
		{"update lost", request{Op: "update", Account: "test/NAME", Value: []byte("synthetic-new-value")}, false, nil, store.KindUncertain},
		{"delete lost", request{Op: "delete", Account: "test/NAME"}, false, nil, store.KindUncertain},
		{"grant lost", grant, false, nil, store.KindUncertain},
		{"read lost", read, false, nil, store.KindUnavailable},
		{"add cancelled, answered", add, true, &response{OK: true}, ""},
		{"add cancelled, lost", add, true, nil, store.KindUncertain},
		{"grant withdrawn", grant, true, cancelled, store.KindCancelled},
		{"grant approved before the withdrawal", grant, true, changed, ""},
		{"grant withdrawn, lost", grant, true, nil, store.KindUncertain},
		{"read cancelled", read, true, nil, store.KindCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer client.Close()
			// What the client sent after the request.
			after := make(chan string, 1)
			go func() {
				defer server.Close()
				decoder := json.NewDecoder(server)
				var req request
				if json.NewEncoder(server).Encode(hello{}) != nil || decoder.Decode(&req) != nil || req.Op != tc.req.Op {
					after <- "no request"
					return
				}
				if tc.cancel {
					cancel()
				}
				var frame withdrawal
				if tc.cancel && tc.req.changesAccess() && (decoder.Decode(&frame) != nil || !frame.Cancel) {
					after <- "no withdrawal"
					return
				}
				if tc.answer == nil {
					after <- ""
					return
				}
				json.NewEncoder(server).Encode(tc.answer)
				rest, _ := io.ReadAll(io.MultiReader(decoder.Buffered(), server))
				after <- strings.TrimSpace(string(rest))
			}()
			result, err := exchange(ctx, client, tc.req)
			client.Close()
			if store.KindOf(err) != tc.want || err == nil && tc.want != "" {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if tc.answer == changed && !reflect.DeepEqual(result.Changes, changed.Changes) {
				t.Errorf("changes %+v, want %+v", result.Changes, changed.Changes)
			}
			if extra := <-after; extra != "" {
				t.Errorf("the client sent %s", extra)
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
	backend.SetProgramAccess(context.Background(), account, true)
	result, err = agentRoundTrip(t, backend, request{Op: "get", Account: account})
	if err != nil || !bytes.Equal(result.Value, second) {
		t.Fatalf("get: %+v, %v", result, err)
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
		{"invalid get reference", request{Op: "get", Account: "test/NOT-VALID"}},
		{"invalid reference after a valid read", request{Op: "read", Accounts: []string{"test/NAME", "bad/name/extra"}}},
		{"empty value", request{Op: "add", Account: "test/NAME"}},
		{"NUL value", request{Op: "update", Account: "test/NAME", Value: []byte("synthetic\x00value")}},
		{"invalid edit target", request{Op: "edit", Account: "test/NAME", To: "test/NOT-VALID"}},
		{"invalid grant reference", request{Op: "grant", Accounts: []string{"test/NAME", "test/NOT-VALID"}}},
		{"read for the window", request{Op: "read", Accounts: []string{"test/NAME"}, Via: "reveal"}},
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

// The service has no field for program access outside grant and revoke, nor
// one that claims the owner's approval, so a request that sets either is
// dropped unanswered and writes nothing.
func TestAgentRejectsClientSuppliedAccessAndApproval(t *testing.T) {
	for _, line := range []string{
		`{"op":"edit","account":"test/NAME","to":"test/NAME","program_access":true}`,
		`{"op":"grant","accounts":["test/NAME"],"auth":"dialog"}`,
	} {
		backend := storetest.New()
		backend.Put("test/NAME", []byte("synthetic-value"))
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			serveRequest(context.Background(), server, backend, nil, nil, nil)
		}()
		decoder := json.NewDecoder(client)
		if err := decoder.Decode(&hello{}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(client, line+"\n"); err != nil {
			t.Fatal(err)
		}
		var result response
		if err := decoder.Decode(&result); err == nil {
			t.Fatalf("the service answered %s with %+v", line, result)
		}
		client.Close()
		<-done
		if items, err := backend.Items(context.Background()); err != nil || items[0].ProgramAccess {
			t.Fatalf("after %s: %+v, %v", line, items, err)
		}
	}
}

// Changes and the approval cross the socket, and the changes made before a
// failure survive it with their approval.
func TestAgentChangesProgramAccess(t *testing.T) {
	backend := storetest.New()
	backend.Put("test/A", []byte("synthetic-a"))
	backend.Put("test/B", []byte("synthetic-b"))
	approvals := 0
	approve := func(context.Context, string) (string, error) { approvals++; return "dialog", nil }
	result, err := roundTrip(t, backend, nil, approve, request{Op: "grant", Accounts: []string{"test/A"}})
	if want := []store.AccessChange{{Ref: store.Ref{Namespace: "test", Name: "A"}, Changed: true}}; err != nil || !reflect.DeepEqual(result.Changes, want) || result.Auth != "dialog" {
		t.Fatalf("grant: %+v, %v", result, err)
	}
	if result, err := agentRoundTrip(t, backend, request{Op: "get", Account: "test/A"}); err != nil || string(result.Value) != "synthetic-a" {
		t.Fatalf("get after the grant: %v", err)
	}
	backend.Before = func(op, account string) error {
		if op == "revoke" && account == "test/B" {
			return &store.Error{Kind: store.KindUncertain, Message: "uncertain"}
		}
		return nil
	}
	backend.SetProgramAccess(context.Background(), "test/B", true)
	result, err = roundTrip(t, backend, nil, approve, request{Op: "revoke", Accounts: []string{"test/A", "test/B"}})
	if want := []store.AccessChange{{Ref: store.Ref{Namespace: "test", Name: "A"}, Changed: true}}; !store.IsKind(err, store.KindUncertain) || !reflect.DeepEqual(result.Changes, want) || result.Auth != "dialog" || approvals != 2 {
		t.Fatalf("failed revoke: %+v, %v after %d approvals", result, err, approvals)
	}
}

// accessHook runs before each change to program access with its context.
type accessHook struct {
	*storetest.Backend
	before func(ctx context.Context)
}

func (b accessHook) SetProgramAccess(ctx context.Context, account string, granted bool) error {
	b.before(ctx)
	return b.Backend.SetProgramAccess(ctx, account, granted)
}

// startRequest serves one request over a pipe and returns the client's end
// after sending req.
func startRequest(t *testing.T, backend store.Backend, recorder activity.Recorder, approve store.Approve, req request) (net.Conn, *json.Decoder, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serveRequest(context.Background(), server, backend, recorder, nil, approve)
	}()
	t.Cleanup(func() {
		client.Close()
		<-done
	})
	decoder := json.NewDecoder(client)
	if err := decoder.Decode(&hello{}); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(client).Encode(req); err != nil {
		t.Fatal(err)
	}
	return client, decoder, done
}

// A client withdraws a change while the owner is asked by sending a
// withdrawal, or by hanging up, as on Ctrl-C: the dialog closes, nothing is
// written, and the refusal is recorded. One still connected learns that.
func TestAgentWithdrawsPendingChange(t *testing.T) {
	for _, hangUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("hang up=%v", hangUp), func(t *testing.T) {
			backend := storetest.New()
			backend.Put("test/NAME", []byte("synthetic-value"))
			recorder := new(storetest.Recorder)
			asked := make(chan struct{})
			approve := func(ctx context.Context, _ string) (string, error) {
				close(asked)
				<-ctx.Done()
				return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
			}
			client, decoder, done := startRequest(t, backend, recorder, approve, request{Op: "grant", Accounts: []string{"test/NAME"}})
			<-asked
			if hangUp {
				client.Close()
			} else if err := json.NewEncoder(client).Encode(withdrawal{Cancel: true}); err != nil {
				t.Fatal(err)
			}
			if !hangUp {
				var result response
				if err := decoder.Decode(&result); err != nil || result.Error == nil || result.Error.Kind != store.KindCancelled || result.Changes != nil {
					t.Fatalf("answer to the withdrawal: %+v, %v", result, err)
				}
				client.Close()
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("the withdrawal did not end the request")
			}
			if items, _ := backend.Items(context.Background()); items[0].ProgramAccess {
				t.Fatal("a withdrawn change was written")
			}
			if records := recorder.Records(); len(records) != 1 || records[0].Op != "grant" || records[0].Entry != "test/NAME" || records[0].Result != "cancelled" {
				t.Fatalf("records %+v, want the refused grant", records)
			}
		})
	}
}

// A withdrawal that arrives before the approval is asked for, as on Ctrl-C
// during the listing, ends the change without asking, even while an earlier
// approval holds.
func TestAgentWithdrawalBeforeApproval(t *testing.T) {
	for _, holds := range []bool{false, true} {
		t.Run(fmt.Sprintf("approval holds=%v", holds), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dialogs := 0
				approval := owner.NewApproval(func(context.Context, string) error { dialogs++; return nil }, func() (time.Duration, bool) { return 0, true }, "")
				if holds {
					if _, err := approval.Approve(context.Background(), "earlier change"); err != nil {
						t.Fatal(err)
					}
					dialogs = 0
				}
				backend := storetest.New()
				backend.Put("test/NAME", []byte("synthetic-value"))
				listing, release := make(chan struct{}), make(chan struct{})
				backend.Before = func(op, _ string) error {
					if op == "items" {
						close(listing)
						<-release
					}
					return nil
				}
				recorder := new(storetest.Recorder)
				client, decoder, _ := startRequest(t, backend, recorder, approval.Approve, request{Op: "grant", Accounts: []string{"test/NAME"}})
				<-listing
				if err := json.NewEncoder(client).Encode(withdrawal{Cancel: true}); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				close(release)
				var result response
				if err := decoder.Decode(&result); err != nil || result.Error == nil || result.Error.Kind != store.KindCancelled || dialogs != 0 {
					t.Fatalf("answer to the withdrawal: %+v, %v after %d dialogs", result, err, dialogs)
				}
				backend.Before = nil
				if items, _ := backend.Items(context.Background()); items[0].ProgramAccess {
					t.Fatal("a withdrawn change was written")
				}
				if records := recorder.Records(); len(records) != 1 || records[0].Op != "grant" || records[0].Result != "cancelled" {
					t.Fatalf("records %+v, want the refused grant", records)
				}
			})
		})
	}
}

// A change the owner approved is written whatever the client does after,
// with no deadline but the helper's own, and answered to a client still
// connected.
func TestAgentFinishesApprovedChange(t *testing.T) {
	for _, hangUp := range []bool{false, true} {
		t.Run(fmt.Sprintf("hang up=%v", hangUp), func(t *testing.T) {
			reached, release := make(chan struct{}), make(chan struct{})
			written := make(chan context.Context, 1)
			backend := accessHook{storetest.New(), func(ctx context.Context) {
				close(reached)
				<-release
				written <- ctx
			}}
			backend.Put("test/NAME", []byte("synthetic-value"))
			approve := func(context.Context, string) (string, error) { return "dialog", nil }
			client, decoder, done := startRequest(t, backend, nil, approve, request{Op: "grant", Accounts: []string{"test/NAME"}})
			<-reached
			if hangUp {
				client.Close()
			} else if err := json.NewEncoder(client).Encode(withdrawal{Cancel: true}); err != nil {
				t.Fatal(err)
			}
			// Let a withdrawal that would end the write arrive first.
			time.Sleep(50 * time.Millisecond)
			close(release)
			ctx := <-written
			if _, ok := ctx.Deadline(); ok || ctx.Err() != nil {
				t.Fatalf("the write's context: %v", ctx.Err())
			}
			if !hangUp {
				var result response
				if err := decoder.Decode(&result); err != nil || !result.OK || len(result.Changes) != 1 || result.Auth != "dialog" {
					t.Fatalf("answer: %+v, %v", result, err)
				}
				client.Close()
			}
			<-done
			if items, _ := backend.Items(context.Background()); !items[0].ProgramAccess {
				t.Fatal("the approved change was not written")
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

func agentRoundTrip(t *testing.T, backend store.Backend, req request) (response, error) {
	t.Helper()
	return roundTrip(t, backend, nil, nil, req)
}

func roundTrip(t *testing.T, backend store.Backend, recorder activity.Recorder, approve store.Approve, req request) (response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		serveRequest(ctx, server, backend, recorder, func(context.Context) error { return nil }, approve)
	}()
	defer func() {
		client.Close()
		cancel()
		<-done
	}()
	return exchange(ctx, client, req)
}
