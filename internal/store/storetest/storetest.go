// Package storetest provides an in-memory store.Backend for tests. It is
// not a second production store: only test code imports it.
package storetest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// Backend keeps items in memory with the Keychain's per-item semantics.
type Backend struct {
	mu    sync.Mutex
	items map[string]item
	// Before, when set, runs before each operation outside the lock; an
	// error it returns fails the operation. Tests use it to inject backend
	// failures and to interleave another writer.
	Before func(op, account string) error
	// Reads records the accounts of each Read call.
	Reads [][]string
	// Now gives the time each write records as the item's modification.
	Now func() time.Time
}

type item struct {
	value []byte
	store.Attributes
}

func New() *Backend { return &Backend{items: map[string]item{}, Now: time.Now} }

func (b *Backend) before(op, account string) error {
	if b.Before == nil {
		return nil
	}
	return b.Before(op, account)
}

func (b *Backend) Items(ctx context.Context) ([]store.Item, error) {
	if err := b.before("items", ""); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var items []store.Item
	for _, account := range slices.Sorted(maps.Keys(b.items)) {
		items = append(items, store.Item{Account: account, Attributes: b.items[account].Attributes})
	}
	return items, nil
}

func (b *Backend) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	if err := b.before("read", ""); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Reads = append(b.Reads, slices.Clone(accounts))
	values := map[string][]byte{}
	for _, account := range accounts {
		if item, ok := b.items[account]; ok {
			values[account] = slices.Clone(item.value)
		}
	}
	return values, nil
}

func (b *Backend) Add(ctx context.Context, account string, value []byte, details store.Details) error {
	if err := b.before("add", account); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[account]; ok {
		return &store.Error{Kind: store.KindExists, Message: "the entry already exists"}
	}
	b.items[account] = item{slices.Clone(value), store.Attributes{Modified: b.Now(), Details: details}}
	return nil
}

func (b *Backend) Update(ctx context.Context, account string, value []byte) error {
	if err := b.before("update", account); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.items[account]
	if !ok {
		return &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	old.value, old.Modified = slices.Clone(value), b.Now()
	b.items[account] = old
	return nil
}

func (b *Backend) Edit(ctx context.Context, account, to string, value []byte, details *store.Details) error {
	if err := b.before("edit", account); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	edited, ok := b.items[account]
	if !ok {
		return &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	if _, ok := b.items[to]; ok && to != account {
		return &store.Error{Kind: store.KindExists, Message: "the entry already exists"}
	}
	if value != nil {
		edited.value = slices.Clone(value)
	}
	if details != nil {
		edited.Details = *details
	}
	edited.Modified = b.Now()
	delete(b.items, account)
	b.items[to] = edited
	return nil
}

func (b *Backend) Delete(ctx context.Context, account string) error {
	if err := b.before("delete", account); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[account]; !ok {
		return &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	delete(b.items, account)
	return nil
}

// Put stores an item without details directly, bypassing the store's account
// encoding.
func (b *Backend) Put(account string, value []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[account] = item{slices.Clone(value), store.Attributes{Modified: b.Now()}}
}

var _ store.Backend = (*Backend)(nil)
