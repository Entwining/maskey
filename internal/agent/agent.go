// Package agent keeps Keychain access outside command sandboxes without
// moving the command or its output masking outside them.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/store"
)

// requestTimeout bounds a request apart from the owner's approval: the
// helper run it starts, or the wait for the service's answer after a client
// withdraws a change.
const requestTimeout = keychain.Timeout

var (
	errPeerUnavailable = errors.New("agent peer code could not be verified")
	errPeerMismatch    = errors.New("agent peer is not the same Maskey executable")
)

// restartWait bounds how long a client waits for a service that is
// restarting on a newer build; see serve.
const restartWait = 5 * time.Second

type request struct {
	Op       string         `json:"op"`
	Account  string         `json:"account,omitempty"`
	To       string         `json:"to,omitempty"`
	Accounts []string       `json:"accounts,omitempty"`
	Value    []byte         `json:"value,omitempty"`
	Details  *store.Details `json:"details,omitempty"`
	// Via is what a read is for; the service accepts only run, which a
	// sandboxed command is.
	Via string `json:"via,omitempty"`
}

func (r request) changesAccess() bool { return r.Op == "grant" || r.Op == "revoke" }

// timeout bounds r from its sending to its response.
func (r request) timeout() time.Duration {
	if r.changesAccess() {
		return owner.DialogWait + requestTimeout
	}
	return requestTimeout
}

type response struct {
	OK      bool                 `json:"ok"`
	Items   []store.Item         `json:"items,omitempty"`
	Value   []byte               `json:"value,omitempty"`
	Values  map[string][]byte    `json:"values,omitempty"`
	Changes []store.AccessChange `json:"changes,omitempty"`
	Auth    string               `json:"auth,omitempty"`
	Error   *store.Error         `json:"error,omitempty"`
	// Warning is the service's failure to record the request, which names
	// files, never a value.
	Warning string `json:"warning,omitempty"`
}

// withdrawal is the frame a client sends after a grant or revoke to withdraw
// it; the service answers whether the owner had approved it.
type withdrawal struct {
	Cancel bool `json:"cancel"`
}

// hello is the service's first message, sent only after it authenticates the
// client: a connection that ends before it has not received the request.
type hello struct{}

// socketPath is in Application Support rather than Caches, which cleanup
// tools may empty while the service runs, leaving clients nothing to reach.
func socketPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, store.Service, "keychain.sock"), nil
}

// ServiceItem is the Keychain service's job or one of its files, and what
// uninstalling the service does with it: Status is removed, absent, kept, or
// failed, or "" in ServiceItems for an item RemoveService would remove.
type ServiceItem struct {
	Item   string
	Status string
	Detail string
}

type Client struct {
	direct store.Backend
	// failed receives the failures to record this process's operations
	// that the service reports, and those of operations performed directly
	// because the service was unavailable.
	failed  func(error)
	path    string
	pathErr error
	// recordable is whether this process can write the activity record.
	recordable bool
	// directUnavailable and serviceUnavailable mark a side that answered
	// unavailable, which later calls skip: a sandbox lasts as long as the
	// process, each direct try launches the helper, and each service try
	// may wait for a restart.
	directUnavailable  atomic.Bool
	serviceUnavailable atomic.Pointer[store.Error]
}

func New(direct store.Backend, failed func(error), recordable bool) *Client {
	path, err := socketPath()
	return &Client{direct: direct, failed: failed, path: path, pathErr: err, recordable: recordable}
}

// perform runs req directly or, if req is recorded and this process cannot
// record it, in the service, which records it from outside the sandbox. It
// tries the other side only when the first answers unavailable, which means
// the operation reached no entry: once a write is sent, a lost answer is
// uncertain, and a repeat on the other side could write twice.
func (c *Client) perform(ctx context.Context, req request, direct func(context.Context) (response, error)) (response, error) {
	if (c.recordable || !keychain.Records(req.Op)) && !c.directUnavailable.Load() {
		r, err := direct(ctx)
		if !store.IsKind(err, store.KindUnavailable) {
			return r, err
		}
		c.directUnavailable.Store(true)
	}
	serviceErr := c.serviceUnavailable.Load()
	if serviceErr == nil {
		r, err := c.remote(ctx, req)
		if !errors.As(err, &serviceErr) || serviceErr.Kind != store.KindUnavailable {
			return r, err
		}
		c.serviceUnavailable.Store(serviceErr)
	}
	if !c.directUnavailable.Load() {
		// Only an operation this process cannot record gets here, so the
		// warning that it was not recorded names the service's failure as
		// the cause.
		r, err := direct(activity.WithFailureReport(ctx, func(err error) { c.failed(fmt.Errorf("%w (%w)", err, serviceErr)) }))
		if !store.IsKind(err, store.KindUnavailable) {
			return r, err
		}
		c.directUnavailable.Store(true)
	}
	// Neither side is reachable; the service's reason is the one to act on.
	return response{}, serviceErr
}

func (c *Client) Items(ctx context.Context) ([]store.Item, error) {
	r, err := c.perform(ctx, request{Op: "items"}, func(ctx context.Context) (r response, err error) {
		r.Items, err = c.direct.Items(ctx)
		return r, err
	})
	return r.Items, err
}

func (c *Client) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	r, err := c.perform(ctx, request{Op: "read", Accounts: accounts, Via: activity.ViaOf(ctx)}, func(ctx context.Context) (r response, err error) {
		r.Values, err = c.direct.Read(ctx, accounts)
		return r, err
	})
	return r.Values, err
}

func (c *Client) Get(ctx context.Context, account string) ([]byte, error) {
	r, err := c.perform(ctx, request{Op: "get", Account: account}, func(ctx context.Context) (r response, err error) {
		r.Value, err = c.direct.Get(ctx, account)
		return r, err
	})
	return r.Value, err
}

func (c *Client) Add(ctx context.Context, account string, value []byte, details store.Details) error {
	_, err := c.perform(ctx, request{Op: "add", Account: account, Value: value, Details: &details}, func(ctx context.Context) (response, error) {
		return response{}, c.direct.Add(ctx, account, value, details)
	})
	return err
}

func (c *Client) Update(ctx context.Context, account string, value []byte) error {
	_, err := c.perform(ctx, request{Op: "update", Account: account, Value: value}, func(ctx context.Context) (response, error) {
		return response{}, c.direct.Update(ctx, account, value)
	})
	return err
}

func (c *Client) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) error {
	_, err := c.perform(ctx, request{Op: "edit", Account: account, To: to, Value: value, Details: details}, func(ctx context.Context) (response, error) {
		return response{}, c.direct.Edit(ctx, account, to, value, details)
	})
	return err
}

// SetProgramAccess refuses: program access changes only through
// ChangeProgramAccess, after the owner approves them.
func (c *Client) SetProgramAccess(context.Context, string, bool) error {
	return &store.Error{Kind: store.KindInvalid, Message: "program access changes only with the owner's approval"}
}

// ChangeProgramAccess grants or revokes program access to refs through the
// Keychain service, even where the Keychain is reachable directly, so that
// one process holds the owner's approval for every mk. It returns the
// changes made before any failure and how the owner approved them.
func (c *Client) ChangeProgramAccess(ctx context.Context, refs []store.Ref, granted bool) ([]store.AccessChange, string, error) {
	req := request{Op: "revoke", Accounts: make([]string, len(refs))}
	if granted {
		req.Op = "grant"
	}
	for i, ref := range refs {
		req.Accounts[i] = ref.String()
	}
	r, err := c.remote(ctx, req)
	return r.Changes, r.Auth, err
}

func (c *Client) Delete(ctx context.Context, account string) error {
	_, err := c.perform(ctx, request{Op: "delete", Account: account}, func(ctx context.Context) (response, error) {
		return response{}, c.direct.Delete(ctx, account)
	})
	return err
}

// CheckService asks the Keychain service for its host check. Success also
// shows that the service runs this build, because peers authenticate each
// other before any request.
func (c *Client) CheckService(ctx context.Context) error {
	_, err := c.remote(ctx, request{Op: "check"})
	return err
}

func (c *Client) remote(ctx context.Context, req request) (response, error) {
	if c.pathErr != nil {
		return response{}, unavailable()
	}
	// A service still running a replaced build exits when it rejects this
	// build, and launchd starts the new one. Nothing is sent before
	// authentication, so waiting for that restart cannot repeat a request.
	giveUp := time.Now().Add(restartWait)
	var rejected error
	for {
		conn, err := (&net.Dialer{Timeout: requestTimeout}).DialContext(ctx, "unix", c.path)
		if err == nil {
			if _, err := authenticate(conn.(*net.UnixConn)); err != nil {
				conn.Close()
				// A service that exits as it retires is no longer there to
				// verify; its earlier mismatch is the reason to report.
				if rejected == nil || errors.Is(err, errPeerMismatch) {
					rejected = identityError(err)
				}
			} else {
				defer conn.Close()
				r, err := exchange(ctx, conn, req)
				if r.Warning != "" {
					c.failed(errors.New(r.Warning))
				}
				return r, err
			}
		} else if rejected == nil {
			return response{}, connectionError(ctx, req, false)
		}
		if time.Now().After(giveUp) {
			return response{}, rejected
		}
		select {
		case <-ctx.Done():
			return response{}, connectionError(ctx, req, false)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func identityError(err error) error {
	if errors.Is(err, errPeerMismatch) {
		return &store.Error{Kind: store.KindUnavailable, Message: "the Keychain service runs a different Maskey build"}
	}
	return &store.Error{Kind: store.KindUnavailable, Message: "the Keychain service identity could not be verified"}
}

func unavailable() error {
	return &store.Error{Kind: store.KindUnavailable, Message: "the Keychain service is not reachable"}
}

func connectionError(ctx context.Context, req request, sent bool) error {
	if sent && keychain.Writes(req.Op) {
		return &store.Error{Kind: store.KindUncertain, Message: "the Keychain service connection ended after the write was sent; check the entry before deciding whether to repeat it"}
	}
	if ctx.Err() != nil {
		return &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	return unavailable()
}

// exchange starts only after peer authentication, and sends the request only
// after the service's hello. Once a write is sent, only the service knows
// whether it happened, so a cancellation waits for its answer; for a grant
// or revoke, it first withdraws the change if the owner has not approved it.
func exchange(ctx context.Context, conn net.Conn, req request) (response, error) {
	deadline := time.Now().Add(req.timeout())
	if err := conn.SetDeadline(deadline); err != nil {
		return response{}, unavailable()
	}
	decoder := json.NewDecoder(conn)
	var greeting hello
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	err := decoder.Decode(&greeting)
	if !stop() || err != nil || ctx.Err() != nil {
		return response{}, connectionError(ctx, req, false)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, connectionError(ctx, req, true)
	}
	if !keychain.Writes(req.Op) {
		defer context.AfterFunc(ctx, func() { conn.Close() })()
	} else if req.changesAccess() {
		defer context.AfterFunc(ctx, func() {
			if answer := time.Now().Add(requestTimeout); answer.Before(deadline) {
				_ = conn.SetDeadline(answer)
			}
			_ = json.NewEncoder(conn).Encode(withdrawal{Cancel: true})
		})()
	}
	var result response
	if err := decoder.Decode(&result); err != nil {
		return response{}, connectionError(ctx, req, true)
	}
	if result.Error != nil {
		return result, result.Error
	}
	if !result.OK {
		return response{}, connectionError(ctx, req, true)
	}
	return result, nil
}

func serveRequest(ctx context.Context, conn net.Conn, backend store.Backend, recorder activity.Recorder, check func(context.Context) error, approve store.Approve) {
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
	result := dispatch(ctx, backend, recorder, check, approve, req)
	result.Warning = warning
	_ = json.NewEncoder(conn).Encode(result)
}

func dispatch(ctx context.Context, backend store.Backend, recorder activity.Recorder, check func(context.Context) error, approve store.Approve, req request) response {
	var result response
	var err error
	switch req.Op {
	case "items":
		result.Items, err = backend.Items(ctx)
	case "read":
		if req.Via != "" && req.Via != "run" {
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
			result.Changes, result.Auth, err = store.New(backend, recorder).ChangeAccess(ctx, refs, req.Op == "grant", approve)
		}
	case "check":
		err = check(ctx)
	default:
		err = &store.Error{Kind: store.KindInvalid, Message: "unsupported Keychain service operation"}
	}
	if err != nil {
		if !errors.As(err, &result.Error) {
			result.Error = &store.Error{Kind: store.KindKeychain, Message: "the Keychain service operation failed"}
		}
		// Changes made before a failure stand, under the owner's approval.
		return response{Changes: result.Changes, Auth: result.Auth, Error: result.Error}
	}
	result.OK = true
	return result
}

var _ store.Backend = (*Client)(nil)
