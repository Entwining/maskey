// Package agent keeps Keychain access outside command sandboxes without
// moving the command or its output masking outside them.
package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// requestTimeout bounds a request apart from the owner's approval: the
// helper run it starts, or the wait for the service's answer after a client
// withdraws a change.
const requestTimeout = keychain.Timeout

// remoteTimeout bounds a request's exchanges with a remote store, so that
// the service answers before the client's requestTimeout ends its wait.
const remoteTimeout = requestTimeout - 10*time.Second

var (
	errPeerUnavailable = errors.New("agent peer code could not be verified")
	errPeerMismatch    = errors.New("agent peer is not the same Maskey executable")
)

// launchdThrottle is launchd's default ThrottleInterval, the least time
// between two starts of a job; the service's job keeps the default.
const launchdThrottle = 10 * time.Second

// restartWait bounds how long a client waits for a service that is
// restarting on a newer build; see serve. The restart can take one throttle:
// macOS 27 pins a job it loads at login to its executable's cdhash, which
// every ad hoc signed build changes, so launchd refuses the new build's
// first start and starts it again launchdThrottle later.
const restartWait = launchdThrottle + 5*time.Second

type request struct {
	Op string `json:"op"`
	// Store names the store the operation is for, or is "" for iCloud
	// Keychain.
	Store string `json:"store,omitempty"`
	// Target is what the client resolved a remote store's name to; see
	// stores.Connection.Target. It can only make the service refuse.
	Target   string         `json:"target,omitempty"`
	Account  string         `json:"account,omitempty"`
	To       string         `json:"to,omitempty"`
	Accounts []string       `json:"accounts,omitempty"`
	Value    []byte         `json:"value,omitempty"`
	Details  *store.Details `json:"details,omitempty"`
	// Via is what a read is for: run, or from the window reveal, copy, or
	// edit.
	Via string `json:"via,omitempty"`
	// Window says the client is Maskey's window, acting for the person using
	// it. Peers are the same executable, so the service takes its word.
	Window bool `json:"window,omitempty"`
	// Replacing says an update follows an add that finds the entry, so
	// that the add is not recorded as a failure.
	Replacing bool `json:"replacing,omitempty"`
	// Saved says a listing is answered from the store's saved copy; see
	// store.WithSaved.
	Saved bool `json:"saved,omitempty"`
	// Connection is the store that store_add adds.
	Connection *stores.Addition `json:"connection,omitempty"`
}

// writes reports whether a lost answer to r leaves something to check.
func (r request) writes() bool { return keychain.Writes(r.Op) }

// remote reports whether r is for a store other than iCloud Keychain.
func (r request) remote() bool { return r.Store != "" && r.Store != stores.ICloud }

// readPurpose reports whether the service performs a read for r.Via. Only
// the window reveals, copies, or edits a value, and it reads iCloud Keychain
// itself.
func (r request) readPurpose() bool {
	switch r.Via {
	case "", "run":
		return true
	case "reveal", "copy", "edit":
		return r.Window && r.remote()
	}
	return false
}

func (r request) changesAccess() bool { return r.Op == "grant" || r.Op == "revoke" }

// changesStores reports whether r changes this Mac's stores, not an entry.
func (r request) changesStores() bool {
	switch r.Op {
	case "store_add", "store_token", OpRemoveStore, OpDefaultStore, OpSyncStore:
		return true
	}
	return false
}

// timeout bounds r from its sending to its response.
func (r request) timeout() time.Duration {
	if r.changesAccess() {
		return owner.DialogWait + requestTimeout
	}
	return requestTimeout
}

type response struct {
	OK      bool                    `json:"ok"`
	Items   []store.Item            `json:"items,omitempty"`
	Value   []byte                  `json:"value,omitempty"`
	Values  map[string]store.Stored `json:"values,omitempty"`
	Changes []store.AccessChange    `json:"changes,omitempty"`
	Auth    string                  `json:"auth,omitempty"`
	// Connection is the store that store_add added, and Config this Mac's
	// stores.
	Connection *stores.Connection `json:"connection,omitempty"`
	Config     *stores.Config     `json:"config,omitempty"`
	Error      *store.Error       `json:"error,omitempty"`
	// Warning is the service's failure to record the request, which names
	// files, never a value.
	Warning string `json:"warning,omitempty"`
	// reached holds once the service received the request, whose outcome
	// it records, also when its answer is lost.
	reached bool
}

// withdrawal is the frame a client sends after a grant or revoke to withdraw
// it; the service answers whether the owner had approved it.
type withdrawal struct {
	Cancel bool `json:"cancel"`
}

// hello is the service's first message, sent only after it authenticates the
// client: a connection that ends before it has not received the request.
type hello struct{}

// ServiceArg is the one argument the service's job starts Maskey with.
const ServiceArg = "--service"

// LegacyArg is the argument of the Keychain service, the service's name
// through 0.0.10. An update leaves that job's configuration in place, so
// launchd starts the new build with it; Install then replaces that job
// with the service under its present name.
const LegacyArg = "--keychain-service"

// socketPath is in Application Support rather than Caches, which cleanup
// tools may empty while the service runs, leaving clients nothing to reach.
func socketPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, store.Service, "service.sock"), nil
}

// ServiceItem is the Maskey service's job or one of its files, and what
// uninstalling the service does with it: Status is removed, absent, kept, or
// failed, or "" in ServiceItems for an item RemoveService would remove.
type ServiceItem struct {
	Item   string
	Status string
	Detail string
	// MayRun marks a job that uninstalling did not stop, or cannot tell it
	// would: while one may run, what Maskey's jobs write stays.
	MayRun bool
}

// Stores are this Mac's stores as the service holds them.
type Stores interface {
	Backend(ctx context.Context, id, target string) (store.Backend, error)
	Config(ctx context.Context) (stores.Config, error)
	Add(ctx context.Context, addition stores.Addition) (stores.Connection, error)
	SetToken(ctx context.Context, id string, token []byte) error
	Remove(ctx context.Context, id string) error
	SetDefault(ctx context.Context, id string) error
	Sync(ctx context.Context, id string) error
}

type Client struct {
	// store names the store the client is for, or is "" for iCloud
	// Keychain, and target is what the name meant when the client was made.
	// direct is nil for a remote store, which only the service reaches: it
	// holds the store's session and is the one writer of its snapshot.
	store, target string
	direct        store.Backend
	// window marks the requests of Maskey's window.
	window bool
	// failed receives the failures to record this process's operations
	// that the service reports, and those of operations performed directly
	// because the service was unavailable.
	failed  func(error)
	path    string
	pathErr error
	// recorder is this process's activity record, and recordable whether
	// the process can write it.
	recorder   activity.Recorder
	recordable bool
	// directUnavailable and serviceUnavailable mark a side that answered
	// unavailable, which later calls skip: a sandbox lasts as long as the
	// process, each direct try launches the helper, and each service try
	// may wait for a restart.
	directUnavailable  atomic.Bool
	serviceUnavailable atomic.Pointer[store.Error]
}

// New returns the client of iCloud Keychain, which direct reaches in this
// process. The operations direct performs are entered in recorder; those
// the service performs, the service records.
func New(direct store.Backend, recorder activity.Recorder, failed func(error), recordable bool) *Client {
	path, err := socketPath()
	return &Client{direct: store.Recorded(direct, recorder), failed: failed, path: path, pathErr: err, recorder: recorder, recordable: recordable}
}

// NewRemote returns the client of the remote store connection, for mk. Its
// requests reach that connection only: the service refuses them once the
// store's name means another.
func NewRemote(connection stores.Connection, recorder activity.Recorder, failed func(error), recordable bool) *Client {
	path, err := socketPath()
	return &Client{store: connection.ID, target: connection.Target(), failed: failed, path: path, pathErr: err, recorder: recorder, recordable: recordable}
}

// NewWindowRemote returns the client of the remote store connection for
// Maskey's window: the service records its requests as the window's, asks
// the owner to approve its changes to program access apart from mk's, and
// reads values for it to reveal, copy, or edit.
func NewWindowRemote(connection stores.Connection, recorder activity.Recorder, failed func(error), recordable bool) *Client {
	c := NewRemote(connection, recorder, failed, recordable)
	c.window = true
	return c
}

// perform runs req directly or, if req is recorded and this process cannot
// record it, in the service, which records it from outside the sandbox. It
// tries the other side only when the first answers unavailable, which means
// the operation reached no entry: once a write is sent, a lost answer is
// uncertain, and a repeat on the other side could write twice.
func (c *Client) perform(ctx context.Context, req request, direct func(context.Context) (response, error)) (response, error) {
	if c.direct == nil {
		return c.served(ctx, req)
	}
	if (c.recordable || !keychain.Records(req.Op)) && !c.directUnavailable.Load() {
		r, err := direct(store.WithFallback(ctx))
		if !store.IsKind(err, store.KindUnavailable) {
			return r, err
		}
		c.directUnavailable.Store(true)
	}
	serviceErr := c.serviceUnavailable.Load()
	// reached holds when the service received req and records how it
	// ended, which a service that could not be reached does not.
	reached := false
	if serviceErr == nil {
		r, err := c.remote(ctx, req)
		if !errors.As(err, &serviceErr) || serviceErr.Kind != store.KindUnavailable {
			if store.IsKind(err, store.KindCancelled) && !r.reached {
				c.unperformed(ctx, req, err)
			}
			return r, err
		}
		reached = r.reached
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
	if !reached {
		c.unperformed(ctx, req, serviceErr)
	}
	return response{}, serviceErr
}

// served has the service perform req, which nothing else can.
func (c *Client) served(ctx context.Context, req request) (response, error) {
	r, err := c.remote(ctx, req)
	// A request the service received is the service's to record.
	if (store.IsKind(err, store.KindUnavailable) || store.IsKind(err, store.KindCancelled)) && !r.reached {
		c.unperformed(ctx, req, err)
	}
	return r, err
}

// unperformed records req, which no process performed, as when the service
// could not be reached or the request was cancelled before it was sent:
// nothing else knows of it.
func (c *Client) unperformed(ctx context.Context, req request, err error) {
	if !c.recordable {
		return
	}
	recordUnperformed(activity.WithStore(ctx, c.store), c.recorder, req, err)
}

func (c *Client) Items(ctx context.Context) ([]store.Item, error) {
	r, err := c.perform(ctx, request{Op: "items", Saved: store.Saved(ctx)}, func(ctx context.Context) (r response, err error) {
		r.Items, err = c.direct.Items(ctx)
		return r, err
	})
	return r.Items, err
}

func (c *Client) Read(ctx context.Context, accounts []string) (map[string]store.Stored, error) {
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
	_, err := c.perform(ctx, request{Op: "add", Account: account, Value: value, Details: &details, Replacing: store.Replacing(ctx)}, func(ctx context.Context) (response, error) {
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
func (c *Client) SetProgramAccess(context.Context, string, string, bool) error {
	return &store.Error{Kind: store.KindInvalid, Message: "program access changes only with the owner's approval"}
}

// ChangeProgramAccess grants or revokes program access to refs through the
// Maskey service, even where the Keychain is reachable directly, so that
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
	r, err := c.served(ctx, req)
	return r.Changes, r.Auth, err
}

func (c *Client) Delete(ctx context.Context, account string) error {
	_, err := c.perform(ctx, request{Op: "delete", Account: account}, func(ctx context.Context) (response, error) {
		return response{}, c.direct.Delete(ctx, account)
	})
	return err
}

// CheckService asks the Maskey service for its host check. Success also
// shows that the service runs this build, because peers authenticate each
// other before any request.
func (c *Client) CheckService(ctx context.Context) error {
	_, err := c.remote(ctx, request{Op: "check"})
	return err
}

// AddStore connects a remote store, which the service signs in to before it
// keeps the connection.
func (c *Client) AddStore(ctx context.Context, addition stores.Addition) (stores.Connection, error) {
	r, err := c.remote(ctx, request{Op: "store_add", Connection: &addition})
	if err != nil || r.Connection == nil {
		return stores.Connection{}, cmp.Or(err, unavailable())
	}
	return *r.Connection, nil
}

// SetStoreToken replaces the access token of the store named id, which the
// service signs in with before it keeps it.
func (c *Client) SetStoreToken(ctx context.Context, id string, token []byte) error {
	_, err := c.remote(ctx, request{Op: "store_token", Store: id, Value: token})
	return err
}

// ChangeStore removes the store named id, makes it the default, or reads it
// from its remote now, as op says.
func (c *Client) ChangeStore(ctx context.Context, op, id string) error {
	_, err := c.remote(ctx, request{Op: op, Store: id})
	return err
}

// Stores asks the service for this Mac's stores, for a process that may not
// read their configuration itself.
func (c *Client) Stores(ctx context.Context) (stores.Config, error) {
	r, err := c.remote(ctx, request{Op: "stores"})
	if err != nil || r.Config == nil {
		return stores.Config{Default: stores.ICloud}, cmp.Or(err, unavailable())
	}
	return *r.Config, nil
}

func (c *Client) remote(ctx context.Context, req request) (response, error) {
	if req.Store == "" {
		req.Store, req.Target = c.store, c.target
	}
	req.Window = c.window
	if c.pathErr != nil {
		return response{}, unavailable()
	}
	// A service still running a replaced build exits when it rejects this
	// build, and launchd starts the new one. Nothing is sent before
	// authentication, so waiting for that restart cannot repeat a request.
	giveUp := time.Now().Add(restartWait)
	var rejected error
	refused := false
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
		} else if errors.Is(err, syscall.ECONNREFUSED) {
			// A socket that refuses connections belongs to a service that
			// ended without removing it, as one that retires or crashes
			// does, and that launchd starts again.
			refused = true
		} else if rejected == nil && !refused {
			// Only a socket missing from the start says that no service is
			// installed: the new build replaces a stale socket, which
			// leaves none for a moment.
			return response{}, connectionError(ctx, req, false)
		}
		if time.Now().After(giveUp) {
			if rejected == nil {
				return response{}, connectionError(ctx, req, false)
			}
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
		return &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service runs a different Maskey build"}
	}
	return &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service identity could not be verified"}
}

func unavailable() error {
	return &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service is not reachable"}
}

func connectionError(ctx context.Context, req request, sent bool) error {
	if sent && req.changesAccess() {
		return &store.Error{Kind: store.KindUncertain, Message: "the Maskey service connection ended after the change was sent; it may be incomplete or still running"}
	}
	if sent && req.changesStores() {
		return &store.Error{Kind: store.KindUncertain, Message: "the Maskey service connection ended after the change to this Mac's stores was sent; it may have been made"}
	}
	if sent && req.writes() {
		return &store.Error{Kind: store.KindUncertain, Message: "the Maskey service connection ended after the write was sent; check the entry before deciding whether to repeat it"}
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
	if !req.writes() {
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
		return response{reached: true}, connectionError(ctx, req, true)
	}
	result.reached = true
	if result.Error != nil {
		return result, result.Error
	}
	if !result.OK {
		return response{reached: true}, connectionError(ctx, req, true)
	}
	return result, nil
}
