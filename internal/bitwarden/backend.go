package bitwarden

import (
	"bytes"
	"context"
	"os"
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
type Backend struct {
	client  *Client
	project string
	path    string
	grants  Grants
	now     func() time.Time

	mu       sync.Mutex
	loaded   bool
	snapshot *snapshot
	// failed is when Bitwarden last could not be reached for a refresh.
	failed time.Time
	// closed holds once this Mac removed the store or replaced its token.
	closed bool
}

// NewBackend returns the backend of project, whose snapshot is the file at
// path.
func NewBackend(client *Client, project, path string, grants Grants) *Backend {
	return &Backend{client: client, project: project, path: path, grants: grants, now: time.Now}
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

// view returns the snapshot to answer from. With current set it is one just
// read from Bitwarden, as a write needs to see what exists.
func (b *Backend) view(ctx context.Context, current bool) (*snapshot, error) {
	if b.closed {
		return nil, &store.Error{Kind: store.KindConflict, Message: "the store's connection changed while the command ran; run the command again"}
	}
	if !b.loaded {
		b.snapshot, b.loaded = b.client.readSnapshot(b.path, b.project), true
	}
	have, now := b.snapshot, b.now()
	if store.Saved(ctx) && !current {
		if have == nil {
			return nil, &store.Error{Kind: store.KindNotFound, Message: "the store has no saved copy on this Mac"}
		}
		return have, nil
	}
	within := func(since time.Time, limit time.Duration) bool {
		age := now.Sub(since)
		return age >= 0 && age < limit
	}
	if have != nil && !current && (within(have.Refreshed, snapshotFresh) || within(b.failed, refreshPause)) {
		return have, nil
	}
	fresh, err := b.refresh(ctx, have)
	switch {
	case err == nil:
		b.snapshot, b.failed = fresh, time.Time{}
		b.persist(ctx)
		return fresh, nil
	case store.IsKind(err, store.KindUnauthenticated), store.IsKind(err, store.KindPermissionDenied), store.IsKind(err, store.KindNotFound):
		b.discard()
		return nil, err
	case have == nil || current:
		return nil, err
	case store.IsKind(err, store.KindNetwork), store.IsKind(err, store.KindRateLimited), store.IsKind(err, store.KindRemote):
		b.failed = now
		return have, nil
	}
	return nil, err
}

// Close ends the backend's use: an operation that took it before, as one
// that waited for the owner's approval, fails instead of reading the
// project or changing its grants, which may be another connection's by then.
func (b *Backend) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
}

// Refresh reads the project from Bitwarden now.
func (b *Backend) Refresh(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.view(ctx, true)
	return err
}

// refresh reads the project from Bitwarden, fetching only the secrets that
// changed since have, so that an unchanged secret is not retrieved again.
func (b *Backend) refresh(ctx context.Context, have *snapshot) (*snapshot, error) {
	headers, err := b.client.Index(ctx, b.project)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		// Bitwarden lists no secrets for a project the account cannot
		// read, as for an empty one.
		if _, err := b.client.Project(ctx, b.project); err != nil {
			return nil, err
		}
	}
	headers = slices.DeleteFunc(headers, func(h Header) bool {
		_, _, ok := decodeKey(h.Key)
		return !ok
	})
	known := map[string]Secret{}
	if have != nil {
		for _, secret := range have.Secrets {
			known[secret.ID] = secret
		}
	}
	var changed []string
	for _, header := range headers {
		if secret, ok := known[header.ID]; !ok || secret.Header != header {
			changed = append(changed, header.ID)
		}
	}
	if len(changed) > 0 {
		read, err := b.client.Read(ctx, changed)
		if store.IsKind(err, store.KindNotFound) {
			return nil, remoteError("the Bitwarden project changed while it was read")
		}
		if err != nil {
			return nil, err
		}
		for _, secret := range read {
			// A secret moved to another project since the list is not
			// this store's, though the token can read it.
			if secret.Project != "" && secret.Project != b.project {
				return nil, remoteError("the Bitwarden project changed while it was read")
			}
			known[secret.ID] = secret
		}
	}
	fresh := &snapshot{Project: b.project, Refreshed: b.now(), Secrets: make([]Secret, 0, len(headers))}
	for _, header := range headers {
		secret := known[header.ID]
		// The secret may have been renamed between the index and get-by-ids.
		if _, _, ok := decodeKey(secret.Key); ok {
			fresh.Secrets = append(fresh.Secrets, secret)
		}
	}
	return fresh, nil
}

// persist writes the snapshot. One that cannot be written leaves no file,
// rather than an older one that another process would take for current.
func (b *Backend) persist(ctx context.Context) {
	if b.client.writeSnapshot(ctx, b.path, b.snapshot) != nil {
		os.Remove(b.path)
	}
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
	b.mu.Lock()
	defer b.mu.Unlock()
	ref, err := store.ParseAccount(account)
	if err != nil {
		return err
	}
	entries, err := b.entries(ctx, true)
	if err != nil {
		return err
	}
	if _, exists := entries[account]; exists {
		return &store.Error{Kind: store.KindExists, Message: "the entry already exists"}
	}
	header, err := b.client.Create(ctx, b.project, encodeKey(account, store.ValueKindOf(ref.Name, details.Kind)), value, details.Notes)
	return b.written(ctx, header, value, details.Notes, err)
}

func (b *Backend) Update(ctx context.Context, account string, value []byte) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.entry(ctx, account, true)
	if err != nil {
		return err
	}
	header, err := b.client.Update(ctx, e.ID, b.project, e.Key, value, e.Note, !bytes.Equal(value, e.Value))
	return b.written(ctx, header, value, e.Note, err)
}

func (b *Backend) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) (err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ref, err := store.ParseAccount(to)
	if err != nil {
		return err
	}
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
	header, err := b.client.Update(ctx, e.ID, b.project, encodeKey(to, kind), value, note, changed)
	return b.written(ctx, header, value, note, err)
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
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.entry(ctx, account, true)
	if err != nil {
		return err
	}
	if err := b.client.Delete(ctx, e.ID); err != nil {
		return b.unsettled(ctx, err)
	}
	b.snapshot.Secrets = slices.DeleteFunc(b.snapshot.Secrets, func(secret Secret) bool { return secret.ID == e.ID })
	b.persist(ctx)
	// The ID never names another secret, so a grant left behind permits
	// nothing; removing it only keeps the list from growing.
	_ = b.grants.Set(ctx, e.ID, false)
	return nil
}

// written puts a secret Bitwarden stored into the snapshot, so that the
// next read sees the write without asking Bitwarden again.
func (b *Backend) written(ctx context.Context, header Header, value []byte, note string, err error) error {
	if err != nil {
		return b.unsettled(ctx, err)
	}
	secret := Secret{Header: header, Value: slices.Clone(value), Note: note}
	if i := slices.IndexFunc(b.snapshot.Secrets, func(other Secret) bool { return other.ID == header.ID }); i >= 0 {
		b.snapshot.Secrets[i] = secret
	} else {
		b.snapshot.Secrets = append(b.snapshot.Secrets, secret)
	}
	b.persist(ctx)
	return nil
}

// unsettled makes the next read ask Bitwarden after a write whose outcome
// is unknown, since the snapshot may no longer match what is stored, and
// discards the snapshot when the write found the token refused. A write
// that is merely not permitted leaves the token's reads as they were.
func (b *Backend) unsettled(ctx context.Context, err error) error {
	switch {
	case store.IsKind(err, store.KindUncertain):
		b.snapshot.Refreshed = time.Time{}
		b.persist(ctx)
	case store.IsKind(err, store.KindUnauthenticated):
		b.discard()
	}
	return err
}

// discard forgets the snapshot once Bitwarden answers that this Mac may no
// longer read the project: what it read before is no longer its to hand
// out, offline too.
func (b *Backend) discard() {
	b.snapshot = nil
	os.Remove(b.path)
}

var _ store.Backend = (*Backend)(nil)
