// Package agent keeps Keychain access outside command sandboxes without
// moving the command or its output masking outside them.
package agent

import (
	"context"
	"encoding/json"
	"errors"
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

type Client struct {
	direct store.Backend
	// failed receives the service's failures to record this process's
	// operations.
	failed  func(error)
	path    string
	pathErr error
	// remoteOnly is set once direct access is unavailable: a sandbox lasts
	// as long as the process, and each direct try launches the helper.
	remoteOnly atomic.Bool
}

// fallsBack reports whether err sends a call to the service, and makes
// later calls go there directly.
func (c *Client) fallsBack(err error) bool {
	if !store.IsKind(err, store.KindUnavailable) {
		return false
	}
	c.remoteOnly.Store(true)
	return true
}

func New(direct store.Backend, failed func(error)) *Client {
	path, err := socketPath()
	return &Client{direct: direct, failed: failed, path: path, pathErr: err}
}

func (c *Client) Items(ctx context.Context) ([]store.Item, error) {
	if !c.remoteOnly.Load() {
		items, err := c.direct.Items(ctx)
		if !c.fallsBack(err) {
			return items, err
		}
	}
	r, err := c.remote(ctx, request{Op: "items"})
	return r.Items, err
}

func (c *Client) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	if !c.remoteOnly.Load() {
		values, err := c.direct.Read(ctx, accounts)
		if !c.fallsBack(err) {
			return values, err
		}
	}
	r, err := c.remote(ctx, request{Op: "read", Accounts: accounts, Via: activity.ViaOf(ctx)})
	return r.Values, err
}

func (c *Client) Get(ctx context.Context, account string) ([]byte, error) {
	if !c.remoteOnly.Load() {
		value, err := c.direct.Get(ctx, account)
		if !c.fallsBack(err) {
			return value, err
		}
	}
	r, err := c.remote(ctx, request{Op: "get", Account: account})
	return r.Value, err
}

func (c *Client) Add(ctx context.Context, account string, value []byte, details store.Details) error {
	if !c.remoteOnly.Load() {
		if err := c.direct.Add(ctx, account, value, details); !c.fallsBack(err) {
			return err
		}
	}
	_, err := c.remote(ctx, request{Op: "add", Account: account, Value: value, Details: &details})
	return err
}

func (c *Client) Update(ctx context.Context, account string, value []byte) error {
	if !c.remoteOnly.Load() {
		if err := c.direct.Update(ctx, account, value); !c.fallsBack(err) {
			return err
		}
	}
	_, err := c.remote(ctx, request{Op: "update", Account: account, Value: value})
	return err
}

func (c *Client) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) error {
	if !c.remoteOnly.Load() {
		if err := c.direct.Edit(ctx, account, to, value, details); !c.fallsBack(err) {
			return err
		}
	}
	_, err := c.remote(ctx, request{Op: "edit", Account: account, To: to, Value: value, Details: details})
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
	if !c.remoteOnly.Load() {
		if err := c.direct.Delete(ctx, account); !c.fallsBack(err) {
			return err
		}
	}
	_, err := c.remote(ctx, request{Op: "delete", Account: account})
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
