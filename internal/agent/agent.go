// Package agent keeps Keychain access outside command sandboxes without
// moving the command or its output masking outside them.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

const requestTimeout = 30 * time.Second

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
}

func (r request) writes() bool {
	return r.Op == "add" || r.Op == "update" || r.Op == "edit" || r.Op == "delete"
}

type response struct {
	OK     bool              `json:"ok"`
	Items  []store.Item      `json:"items,omitempty"`
	Values map[string][]byte `json:"values,omitempty"`
	Error  *store.Error      `json:"error,omitempty"`
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
	direct  store.Backend
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

func New(direct store.Backend) *Client {
	path, err := socketPath()
	return &Client{direct: direct, path: path, pathErr: err}
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
	r, err := c.remote(ctx, request{Op: "read", Accounts: accounts})
	return r.Values, err
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
			if err := authenticate(conn.(*net.UnixConn)); err != nil {
				conn.Close()
				// A service that exits as it retires is no longer there to
				// verify; its earlier mismatch is the reason to report.
				if rejected == nil || errors.Is(err, errPeerMismatch) {
					rejected = identityError(err)
				}
			} else {
				defer conn.Close()
				return exchange(ctx, conn, req)
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
	if sent && req.writes() {
		return &store.Error{Kind: store.KindUncertain, Message: "the Keychain service connection ended after the write was sent; check the entry before deciding whether to repeat it"}
	}
	if ctx.Err() != nil {
		return &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	return unavailable()
}

// exchange starts only after peer authentication, and sends the request only
// after the service's hello.
func exchange(ctx context.Context, conn net.Conn, req request) (response, error) {
	deadline := time.Now().Add(requestTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return response{}, unavailable()
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	decoder := json.NewDecoder(conn)
	var greeting hello
	if err := decoder.Decode(&greeting); err != nil {
		return response{}, connectionError(ctx, req, false)
	}
	if ctx.Err() != nil {
		return response{}, connectionError(ctx, req, false)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, connectionError(ctx, req, true)
	}
	var result response
	if err := decoder.Decode(&result); err != nil {
		return response{}, connectionError(ctx, req, true)
	}
	if result.Error != nil {
		return response{}, result.Error
	}
	if !result.OK {
		return response{}, connectionError(ctx, req, true)
	}
	return result, nil
}

func serveRequest(ctx context.Context, conn net.Conn, backend store.Backend, check func(context.Context) error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_ = conn.SetDeadline(time.Now().Add(requestTimeout))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if json.NewEncoder(conn).Encode(hello{}) != nil {
		return
	}
	var req request
	decoder := json.NewDecoder(conn)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil {
		return
	}
	result := dispatch(ctx, backend, check, req)
	_ = json.NewEncoder(conn).Encode(result)
}

func dispatch(ctx context.Context, backend store.Backend, check func(context.Context) error, req request) response {
	var result response
	var err error
	valid := func(account string) error {
		namespace, name, ok := strings.Cut(account, "/")
		if !ok {
			return &store.Error{Kind: store.KindInvalid, Message: "invalid entry reference"}
		}
		return store.ValidateRef(store.Ref{Namespace: namespace, Name: name})
	}
	switch req.Op {
	case "items":
		result.Items, err = backend.Items(ctx)
	case "read":
		for _, account := range req.Accounts {
			if err = valid(account); err != nil {
				break
			}
		}
		if err == nil {
			result.Values, err = backend.Read(ctx, req.Accounts)
		}
	case "add", "update", "delete":
		err = valid(req.Account)
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
		err = valid(req.Account)
		if err == nil {
			err = valid(req.To)
		}
		if err == nil && req.Value != nil {
			err = store.ValidateValue(req.Value)
		}
		if err == nil {
			err = backend.Edit(ctx, req.Account, req.To, req.Value, req.Details)
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
		result.Values = nil
		result.Items = nil
		return result
	}
	result.OK = true
	return result
}

var _ store.Backend = (*Client)(nil)
