package bitwarden

import (
	"context"
	"os"
	"slices"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// view returns the snapshot to answer from. Its caller holds mu, which view
// releases while it waits for the turn or asks Bitwarden. With current set
// it is one just read from Bitwarden, as a write needs to see what exists,
// and the caller holds the turn.
func (b *Backend) view(ctx context.Context, current bool) (*snapshot, error) {
	if current {
		return b.fetch(ctx, true)
	}
	var took bool
	var waited error
	defer func() {
		if took {
			b.turn.Release()
		}
	}()
	for {
		// A request that holds or held the turn may have refreshed the
		// snapshot, or failed to and started refreshPause.
		if have, err := b.cached(ctx); have != nil || err != nil {
			return have, err
		}
		switch {
		case took:
			return b.fetch(ctx, false)
		case waited != nil:
			// The read gave up waiting and asked Bitwarden nothing. Past its
			// deadline it answers from the snapshot, as when Bitwarden cannot
			// be reached, but starts no refreshPause: only a refresh that
			// failed shows that Bitwarden cannot be reached.
			if b.snapshot != nil && store.IsKind(waited, store.KindNetwork) {
				return b.snapshot, nil
			}
			return nil, waited
		}
		refreshed := b.refreshedSignal
		b.unlocked(func() { took, waited = b.turn.TakeUnless(ctx, refreshed) })
	}
}

// cached returns the snapshot that answers a read without asking Bitwarden,
// or nil when the read must ask.
func (b *Backend) cached(ctx context.Context) (*snapshot, error) {
	if b.closed {
		return nil, closedError()
	}
	b.load()
	have, now := b.snapshot, b.now()
	if store.Saved(ctx) {
		if have == nil {
			return nil, &store.Error{Kind: store.KindNotFound, Message: "the store has no saved copy on this Mac"}
		}
		return have, nil
	}
	if have != nil && (within(now, have.Refreshed, snapshotFresh) || within(now, b.failed, refreshPause)) {
		return have, nil
	}
	return nil, nil
}

func within(now, since time.Time, limit time.Duration) bool {
	age := now.Sub(since)
	return age >= 0 && age < limit
}

func (b *Backend) load() {
	if !b.loaded {
		b.snapshot, b.loaded = b.client.readSnapshot(b.path, b.project), true
	}
}

func closedError() error {
	return &store.Error{Kind: store.KindConflict, Message: "the store's connection changed while the command ran; run the command again"}
}

// fetch reads the project from Bitwarden and installs what it read. Its
// caller holds the turn and mu, which fetch releases while it asks.
func (b *Backend) fetch(ctx context.Context, current bool) (*snapshot, error) {
	if b.closed {
		return nil, closedError()
	}
	b.load()
	have, now := b.snapshot, b.now()
	var fresh *snapshot
	var err error
	b.unlocked(func() { fresh, err = b.refresh(ctx, have) })
	return b.refreshed(have, fresh, err, current, now)
}

// refreshed installs the outcome of a refresh that began at now from have,
// and returns the snapshot to answer from.
func (b *Backend) refreshed(have, fresh *snapshot, err error, current bool, now time.Time) (*snapshot, error) {
	switch {
	case b.closed:
		return nil, closedError()
	case err == nil:
		b.snapshot, b.failed = fresh, time.Time{}
		b.signalRefreshed()
		b.persist()
		return fresh, nil
	case store.IsKind(err, store.KindUnauthenticated), store.IsKind(err, store.KindPermissionDenied), store.IsKind(err, store.KindNotFound):
		b.discard()
		return nil, err
	case have == nil || current:
		return nil, err
	case store.IsKind(err, store.KindNetwork), store.IsKind(err, store.KindRateLimited), store.IsKind(err, store.KindRemote):
		b.failed = now
		b.signalRefreshed()
		return have, nil
	}
	return nil, err
}

// signalRefreshed wakes the reads waiting for the turn to check the state
// again.
func (b *Backend) signalRefreshed() {
	close(b.refreshedSignal)
	b.refreshedSignal = make(chan struct{})
}

// Refresh reads the project from Bitwarden now.
func (b *Backend) Refresh(ctx context.Context) error {
	if err := b.turn.Take(ctx); err != nil {
		return err
	}
	defer b.turn.Release()
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
func (b *Backend) persist() {
	if b.client.writeSnapshot(b.path, b.snapshot) != nil {
		os.Remove(b.path)
	}
}

// discard forgets the snapshot once Bitwarden answers that this Mac may no
// longer read the project: what it read before is no longer its to hand
// out, offline too.
func (b *Backend) discard() {
	b.snapshot = nil
	os.Remove(b.path)
}
