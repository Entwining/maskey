package bitwarden

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

const (
	// snapshotFresh is how long a read uses the snapshot without asking
	// Bitwarden, which bounds how long another Mac's write stays unseen
	// while this one is online. Larger means fewer requests and later news.
	snapshotFresh = 5 * time.Minute
	// refreshPause is how long reads use an older snapshot after Bitwarden
	// could not be reached, instead of each waiting for the network again.
	refreshPause = 30 * time.Second
)

// Grants are the secrets this Mac's owner granted program access, by ID. A
// grant belongs to this Mac and to one secret: another secret given the same
// name later has another ID, so it starts without one.
type Grants interface {
	Granted(ctx context.Context) ([]string, error)
	Set(ctx context.Context, id string, granted bool) error
}

// Backend stores entries as the secrets of one Bitwarden project. A secret's
// key is "<namespace>/<NAME>#<kind>", its value the entry's value, and its
// note the entry's notes; secrets with any other key are not Maskey's.
//
// Bitwarden holds the entries. Reads come from a snapshot of those entries
// that the backend refreshes when it is older than snapshotFresh, and that
// stays in use while Bitwarden cannot be reached. Writes go to Bitwarden
// first and fail without it.
//
// Two locks keep a slow exchange with Bitwarden from holding back what needs
// none. The turn lets one request at a time refresh or write, so that each
// starts from the outcome of the one before. mu guards the state below and
// the grants, and is never held across a request to Bitwarden or while
// waiting for the turn: a read the snapshot answers, and Close, need only mu,
// so neither waits for Bitwarden. A request that needs Bitwarden takes the
// turn, then mu to read the state, which the request before it may have
// changed; it exchanges without mu and takes mu again to install the result.
// A read waiting for the turn also ends its wait when a refresh installs its
// outcome, which may answer it while the turn's holder goes on to write, and
// when Close ends the backend's use.
type Backend struct {
	client  *Client
	project string
	path    string
	grants  Grants
	now     func() time.Time
	turn    *Turn

	mu       sync.Mutex
	loaded   bool
	snapshot *snapshot
	// failed is when Bitwarden last could not be reached for a refresh.
	failed time.Time
	// closed holds once this Mac removed the store or replaced its token.
	closed bool
	// refreshedSignal is closed, and replaced, when a refresh installs its
	// outcome or the backend closes.
	refreshedSignal chan struct{}
}

// NewBackend returns the backend of project, whose snapshot is the file at
// path. turn is the connection's: every backend made for it shares the turn,
// so that one made after another closed waits for the exchange that one is
// still in, instead of reading the project before that write lands.
func NewBackend(client *Client, project, path string, grants Grants, turn *Turn) *Backend {
	return &Backend{client: client, project: project, path: path, grants: grants, now: time.Now, turn: turn, refreshedSignal: make(chan struct{})}
}

type entry struct {
	Secret
	kind   store.ValueKind
	others []string // IDs of other secrets with the same account
}

// decodeKey splits a secret's key into an entry's account and kind. The
// kind is always written out, so a key without a known one is not an entry:
// guessing it could hand an SSH key to a command as an environment variable.
func decodeKey(key string) (account string, kind store.ValueKind, ok bool) {
	i := strings.LastIndexByte(key, '#')
	if i < 0 {
		return "", "", false
	}
	account, kind = key[:i], store.ValueKind(key[i+1:])
	if _, err := store.ParseAccount(account); err != nil || !slices.Contains(store.ValueKinds, kind) {
		return "", "", false
	}
	return account, kind, true
}

func encodeKey(account string, kind store.ValueKind) string { return account + "#" + string(kind) }

// unlocked runs exchange without mu, which its caller holds.
func (b *Backend) unlocked(exchange func()) {
	b.mu.Unlock()
	defer b.mu.Lock()
	exchange()
}

// Close ends the backend's use: an operation that took it before, as one
// that waited for the owner's approval, fails instead of reading the
// project or changing its grants, which may be another connection's by then.
// Close does not wait for an exchange with Bitwarden, since the owner removes
// a store or replaces its token when Bitwarden is slow or refuses it; an
// exchange that ends later installs nothing, and a write still reports its
// outcome.
func (b *Backend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.signalRefreshed()
}

// entries returns the project's entries by account. Bitwarden does not keep
// keys unique, and nothing says which of two secrets with one name is meant:
// such an entry is listed and marked ambiguous, so that it alone fails.
func (b *Backend) entries(ctx context.Context, current bool) (map[string]entry, error) {
	view, err := b.view(ctx, current)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]entry, len(view.Secrets))
	for _, secret := range view.Secrets {
		account, kind, ok := decodeKey(secret.Key)
		if !ok {
			continue
		}
		if first, twice := entries[account]; twice {
			first.others = append(first.others, secret.ID)
			entries[account] = first
			continue
		}
		entries[account] = entry{Secret: secret, kind: kind}
	}
	return entries, nil
}

func ambiguous(account string) error {
	ref, _ := store.ParseAccount(account)
	return &store.Error{Kind: store.KindConflict, Ref: &ref, Message: "two Bitwarden secrets have this name; rename or delete one in Bitwarden"}
}

func (b *Backend) entry(ctx context.Context, account string, current bool) (entry, error) {
	entries, err := b.entries(ctx, current)
	if err != nil {
		return entry{}, err
	}
	found, ok := entries[account]
	if !ok {
		return entry{}, &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	if len(found.others) != 0 {
		return entry{}, ambiguous(account)
	}
	return found, nil
}

func (b *Backend) Items(ctx context.Context) ([]store.Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// The grants are loaded while the request has time, since a read that
	// waited for the turn until its deadline still answers, and read again
	// once the entries are in hand: a change of program access may have
	// completed while this read waited.
	if _, err := b.grants.Granted(ctx); err != nil {
		return nil, err
	}
	entries, err := b.entries(ctx, false)
	if err != nil {
		return nil, err
	}
	granted, err := b.grants.Granted(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]store.Item, 0, len(entries))
	for account, e := range entries {
		// A revision Bitwarden writes otherwise leaves the time unknown.
		modified, _ := time.Parse(time.RFC3339Nano, e.Revision)
		items = append(items, store.Item{Account: account, Attributes: store.Attributes{
			Modified: modified,
			// A grant to any secret of the name shows, so that a revoke
			// reaches the entry and fails there instead of finding
			// nothing to change.
			ProgramAccess: slices.ContainsFunc(append([]string{e.ID}, e.others...), func(id string) bool { return slices.Contains(granted, id) }),
			ID:            e.ID,
			Details:       store.Details{Kind: e.kind, Notes: e.Note},
		}})
	}
	return items, nil
}

func (b *Backend) Read(ctx context.Context, accounts []string) (values map[string]store.Stored, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := b.entries(ctx, false)
	if err != nil {
		return nil, err
	}
	values = map[string]store.Stored{}
	for _, account := range accounts {
		if e, ok := entries[account]; ok {
			if len(e.others) != 0 {
				return nil, ambiguous(account)
			}
			values[account] = store.Stored{Value: slices.Clone(e.Value), Kind: e.kind}
		}
	}
	return values, nil
}

func (b *Backend) Get(ctx context.Context, account string) (value []byte, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// As in Items.
	if _, err := b.grants.Granted(ctx); err != nil {
		return nil, err
	}
	e, err := b.entry(ctx, account, false)
	if err != nil {
		return nil, err
	}
	granted, err := b.grants.Granted(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(granted, e.ID) {
		return nil, &store.Error{Kind: store.KindNotGranted, Message: "program access is not granted"}
	}
	return slices.Clone(e.Value), nil
}

func (b *Backend) Add(ctx context.Context, account string, value []byte, details store.Details) (err error) {
	ref, err := store.ParseAccount(account)
	if err != nil {
		return err
	}
	if err := b.turn.Take(ctx); err != nil {
		return err
	}
	defer b.turn.Release()
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := b.entries(ctx, true)
	if err != nil {
		return err
	}
	if _, exists := entries[account]; exists {
		return &store.Error{Kind: store.KindExists, Message: "the entry already exists"}
	}
	var header Header
	b.unlocked(func() {
		header, err = b.client.Create(ctx, b.project, encodeKey(account, store.ValueKindOf(ref.Name, details.Kind)), value, details.Notes)
	})
	return b.written(header, value, details.Notes, err)
}

func (b *Backend) Update(ctx context.Context, account string, value []byte) (err error) {
	if err := b.turn.Take(ctx); err != nil {
		return err
	}
	defer b.turn.Release()
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.entry(ctx, account, true)
	if err != nil {
		return err
	}
	var header Header
	b.unlocked(func() {
		header, err = b.client.Update(ctx, e.ID, b.project, e.Key, value, e.Note, !bytes.Equal(value, e.Value))
	})
	return b.written(header, value, e.Note, err)
}

func (b *Backend) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) (err error) {
	ref, err := store.ParseAccount(to)
	if err != nil {
		return err
	}
	if err := b.turn.Take(ctx); err != nil {
		return err
	}
	defer b.turn.Release()
	b.mu.Lock()
	defer b.mu.Unlock()
	entries, err := b.entries(ctx, true)
	if err != nil {
		return err
	}
	e, ok := entries[account]
	if !ok {
		return &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	if len(e.others) != 0 {
		return ambiguous(account)
	}
	if _, taken := entries[to]; taken && to != account {
		return &store.Error{Kind: store.KindExists, Message: "the entry already exists"}
	}
	kind, note := e.kind, e.Note
	if details != nil {
		kind, note = store.ValueKindOf(ref.Name, details.Kind), details.Notes
	}
	changed := value != nil && !bytes.Equal(value, e.Value)
	if value == nil {
		value = e.Value
	}
	var header Header
	b.unlocked(func() {
		header, err = b.client.Update(ctx, e.ID, b.project, encodeKey(to, kind), value, note, changed)
	})
	return b.written(header, value, note, err)
}

// SetProgramAccess binds this Mac's grant to the listed UUID so a replacement cannot inherit it.
func (b *Backend) SetProgramAccess(ctx context.Context, account, id string, granted bool) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.entry(ctx, account, false)
	if err != nil {
		return err
	}
	if e.ID != id {
		return &store.Error{Kind: store.KindConflict, Message: "another Bitwarden secret took this name since the entry was listed; its program access was not changed"}
	}
	return b.grants.Set(ctx, e.ID, granted)
}

func (b *Backend) Delete(ctx context.Context, account string) (err error) {
	if err := b.turn.Take(ctx); err != nil {
		return err
	}
	defer b.turn.Release()
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.entry(ctx, account, true)
	if err != nil {
		return err
	}
	b.unlocked(func() { err = b.client.Delete(ctx, e.ID) })
	switch {
	case b.closed:
		return err
	case err != nil:
		return b.unsettled(err)
	}
	b.snapshot.Secrets = slices.DeleteFunc(b.snapshot.Secrets, func(secret Secret) bool { return secret.ID == e.ID })
	b.persist()
	// The ID never names another secret, so a grant left behind permits
	// nothing; removing it only keeps the list from growing.
	_ = b.grants.Set(ctx, e.ID, false)
	return nil
}

// written puts a secret Bitwarden stored into the snapshot, so that the
// next read sees the write without asking Bitwarden again. A backend closed
// during the write keeps nothing of it.
func (b *Backend) written(header Header, value []byte, note string, err error) error {
	switch {
	case b.closed:
		return err
	case err != nil:
		return b.unsettled(err)
	}
	secret := Secret{Header: header, Value: slices.Clone(value), Note: note}
	if i := slices.IndexFunc(b.snapshot.Secrets, func(other Secret) bool { return other.ID == header.ID }); i >= 0 {
		b.snapshot.Secrets[i] = secret
	} else {
		b.snapshot.Secrets = append(b.snapshot.Secrets, secret)
	}
	b.persist()
	return nil
}

// unsettled makes the next read ask Bitwarden after a write whose outcome
// is unknown, since the snapshot may no longer match what is stored, and
// discards the snapshot when the write found the token refused. A write
// that is merely not permitted leaves the token's reads as they were.
func (b *Backend) unsettled(err error) error {
	switch {
	case store.IsKind(err, store.KindUncertain):
		b.snapshot.Refreshed = time.Time{}
		b.persist()
	case store.IsKind(err, store.KindUnauthenticated):
		b.discard()
	}
	return err
}

var _ store.Backend = (*Backend)(nil)
