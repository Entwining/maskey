package agent

import (
	"context"
	"errors"

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
		if backend, err = registry.Backend(ctx, req.Store); err == nil {
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

var _ store.Backend = (*Client)(nil)
