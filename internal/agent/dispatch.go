package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// The operations ChangeStore performs on the store a request names.
const (
	OpRemoveStore  = "store_remove"
	OpDefaultStore = "store_default"
	OpSyncStore    = "store_sync"
)

func dispatch(ctx context.Context, registry Stores, recorder activity.Recorder, check func(context.Context) error, approve store.Approve, req request) response {
	var result response
	var err error
	if req.remote() {
		ctx = activity.WithStore(ctx, req.Store)
		// The owner's approval of a grant or revoke takes as long as the
		// owner does; each exchange with the remote has its own limit.
		if !req.changesAccess() {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, remoteTimeout)
			defer cancel()
		}
	}
	switch req.Op {
	case "check":
		err = check(ctx)
	case "stores":
		var config stores.Config
		if config, err = registry.Config(ctx); err == nil {
			result.Config = &config
		}
	case "store_add":
		if req.Connection == nil {
			err = &store.Error{Kind: store.KindInvalid, Message: "the request names no store to add"}
			break
		}
		var connection stores.Connection
		ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
		defer cancel()
		if connection, err = registry.Add(ctx, *req.Connection); err == nil {
			result.Connection = &connection
		}
	case "store_token":
		err = registry.SetToken(ctx, req.Store, req.Value)
	case OpRemoveStore:
		err = registry.Remove(ctx, req.Store)
	case OpDefaultStore:
		err = registry.SetDefault(ctx, req.Store)
	case OpSyncStore:
		err = registry.Sync(ctx, req.Store)
	default:
		var backend store.Backend
		if backend, err = registry.Backend(ctx, req.Store, req.Target); err == nil {
			return dispatchEntry(ctx, store.Recorded(backend, recorder), recorder, approve, req)
		}
		recordUnperformed(ctx, recorder, req, err)
	}
	return settle(result, err)
}

// recordUnperformed enters in the activity record an operation on entries
// that ended with err before a backend performed it.
func recordUnperformed(ctx context.Context, recorder activity.Recorder, req request, err error) {
	switch req.Op {
	case "read":
		store.RecordFailure(activity.WithVia(ctx, req.Via), recorder, store.Operation{Op: req.Op, Accounts: req.Accounts}, err)
	case "grant", "revoke":
		for _, account := range req.Accounts {
			store.RecordFailure(ctx, recorder, store.Operation{Op: req.Op, Account: account}, err)
		}
	case "get", "add", "update", "edit", "delete":
		store.RecordFailure(ctx, recorder, store.Operation{Op: req.Op, Account: req.Account, To: req.To}, err)
	}
}

// dispatchEntry performs an operation on the entries of one store.
func dispatchEntry(ctx context.Context, backend store.Backend, recorder activity.Recorder, approve store.Approve, req request) response {
	var result response
	var err error
	switch req.Op {
	case "items":
		if req.Saved {
			ctx = store.WithSaved(ctx)
		}
		result.Items, err = backend.Items(ctx)
	case "read":
		if !req.readPurpose() {
			err = &store.Error{Kind: store.KindInvalid, Message: "unsupported purpose of a read"}
		} else {
			for _, account := range req.Accounts {
				if _, err = store.ParseAccount(account); err != nil {
					break
				}
			}
		}
		if err == nil {
			result.Values, err = backend.Read(activity.WithVia(ctx, req.Via), req.Accounts)
		}
	case "get":
		_, err = store.ParseAccount(req.Account)
		if err == nil {
			result.Value, err = backend.Get(ctx, req.Account)
		}
	case "add", "update", "delete":
		_, err = store.ParseAccount(req.Account)
		if err == nil && req.Op != "delete" {
			err = store.ValidateValue(req.Value)
		}
		if err == nil {
			switch req.Op {
			case "add":
				var details store.Details
				if req.Details != nil {
					details = *req.Details
				}
				if req.Replacing {
					ctx = store.WithReplacing(ctx)
				}
				err = backend.Add(ctx, req.Account, req.Value, details)
			case "update":
				err = backend.Update(ctx, req.Account, req.Value)
			case "delete":
				err = backend.Delete(ctx, req.Account)
			}
		}
	case "edit":
		_, err = store.ParseAccount(req.Account)
		if err == nil {
			_, err = store.ParseAccount(req.To)
		}
		if err == nil && req.Value != nil {
			err = store.ValidateValue(req.Value)
		}
		if err == nil {
			err = backend.Edit(ctx, req.Account, req.To, req.Value, req.Details)
		}
	case "grant", "revoke":
		refs := make([]store.Ref, len(req.Accounts))
		for i, account := range req.Accounts {
			if refs[i], err = store.ParseAccount(account); err != nil {
				break
			}
		}
		if err == nil {
			if req.remote() {
				// The owner approves the entry of this store, not one of
				// the same name in iCloud Keychain.
				inner := approve
				approve = func(ctx context.Context, reason string) (string, error) {
					return inner(ctx, reason+" in the store "+req.Store)
				}
			}
			result.Changes, result.Auth, err = store.New(backend, recorder).ChangeAccess(ctx, refs, req.Op == "grant", approve)
		}
	default:
		err = &store.Error{Kind: store.KindInvalid, Message: "unsupported Maskey service operation"}
	}
	return settle(result, err)
}

// settle is the response to an operation that ended with err.
func settle(result response, err error) response {
	if err != nil {
		if !errors.As(err, &result.Error) {
			result.Error = &store.Error{Kind: store.KindKeychain, Message: "the Maskey service operation failed"}
		}
		// Changes made before a failure stand, under the owner's approval.
		return response{Changes: result.Changes, Auth: result.Auth, Error: result.Error}
	}
	result.OK = true
	return result
}

// windowKey marks the context of a request from Maskey's window.
type windowKey struct{}

// FromWindow reports whether ctx is that of a request from Maskey's window,
// whose approvals the service keeps apart from mk's.
func FromWindow(ctx context.Context) bool {
	window, _ := ctx.Value(windowKey{}).(bool)
	return window
}

// serveRequest answers one client. ctx describes the program that started
// the client, which a request from mk acts for; window describes the client
// itself, which a request from Maskey's window acts as.
func serveRequest(ctx context.Context, conn net.Conn, window func() activity.Caller, registry Stores, recorder activity.Recorder, check func(context.Context) error, approve store.Approve) {
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	if json.NewEncoder(conn).Encode(hello{}) != nil {
		return
	}
	var req request
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(req.timeout()))
	if req.Window {
		ctx = activity.WithCaller(context.WithValue(ctx, windowKey{}, true), window())
	}
	if req.changesAccess() {
		// The client sends more, a withdrawal, or hangs up only when it gives
		// up on the change. That ends the owner's approval if it is not yet
		// given, before any dialog or reuse of an earlier approval, but never
		// a write the owner approved: a batch stopped halfway leaves entries
		// to check.
		withdrawn := make(chan struct{})
		rest := io.MultiReader(decoder.Buffered(), conn)
		go func() {
			var frame json.RawMessage
			_ = json.NewDecoder(rest).Decode(&frame)
			close(withdrawn)
		}()
		approveRequest := approve
		approve = func(ctx context.Context, reason string) (string, error) {
			select {
			case <-withdrawn:
				return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
			default:
			}
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				select {
				case <-withdrawn:
					cancel()
				case <-ctx.Done():
				}
			}()
			return approveRequest(ctx, reason)
		}
	}
	var warning string
	ctx = activity.WithFailureReport(ctx, func(err error) {
		if warning == "" {
			warning = err.Error()
		}
	})
	result := dispatch(ctx, registry, recorder, check, approve, req)
	result.Warning = warning
	_ = json.NewEncoder(conn).Encode(result)
}

var _ store.Backend = (*Client)(nil)
