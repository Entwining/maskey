package main

import (
	"context"
	"slices"
	"strings"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// storeAccess is one store as the window uses it.
type storeAccess struct {
	store *store.Store
	// setAccess grants or revokes an entry's program access once the owner
	// approves.
	setAccess func(ctx context.Context, ref store.Ref, granted bool) error
}

// storeService changes this Mac's stores, which only the Maskey service
// does: it keeps their access tokens.
type storeService interface {
	AddStore(ctx context.Context, addition stores.Addition) (stores.Connection, error)
	ChangeStore(ctx context.Context, op, id string) error
}

// open returns the store named id, as the connection the window last read
// under that name: what the window shows of the store, and asks the user to
// confirm, is that connection's, so its actions reach no other.
func (g *gui) open(id string) storeAccess {
	if id == stores.ICloud {
		return g.icloud
	}
	connection, _ := g.stores.Find(id)
	// The service takes a request without a store's name for iCloud
	// Keychain's.
	connection.ID = id
	return g.remoteStore(connection)
}

// target is what the store named id connects to, as the window last read
// this Mac's stores, or "" once the store left.
func (g *gui) target(id string) string {
	connection, ok := g.stores.Find(id)
	if !ok {
		return ""
	}
	return connection.Target()
}

// entryID names an entry: a reference in one store. Two stores can hold the
// same reference, and those are two entries, so whatever acts on an entry
// opens the store its entryID names. The zero entryID names none.
type entryID struct {
	store string
	ref   store.Ref
}

// scope is the sidebar's choice: one namespace of one store, or, zero,
// every entry of the stores shown.
type scope struct{ store, namespace string }

// item is the scope's ID among the sidebar's items. A store's ID never
// contains ":", so the first one ends it.
func (s scope) item() string {
	if s == (scope{}) {
		return allNamespaces
	}
	return s.store + ":" + s.namespace
}

func scopeOf(item string) scope {
	if item == allNamespaces {
		return scope{}
	}
	id, namespace, _ := strings.Cut(item, ":")
	return scope{id, namespace}
}

// listing is what one listing of a store finds.
type listing struct {
	namespaces []store.Namespace
	refs       []store.Ref // sorted by namespace and name
	attributes map[store.Ref]store.Attributes
}

// listedStore is what the window shows of one store: its latest listing
// that succeeded, which stays while a later one fails.
type listedStore struct {
	listing
	loaded bool   // a listing has finished
	read   bool   // a listing has succeeded
	err    string // why the latest listing failed, "" if it did not
}

// attempt names the listing that failed: the store's first reading, or a
// reload of what the window shows of it.
func (s *listedStore) attempt() string {
	if s.read {
		return "reload"
	}
	return "read"
}

// severalStores reports whether this Mac has a store besides iCloud
// Keychain. Only then does the window name stores at all.
func (g *gui) severalStores() bool { return len(g.stores.Stores) > 0 }

// shown reports whether the window shows the store named id: every store,
// or the one the sidebar's choice of store narrows it to.
func (g *gui) shown(id string) bool { return g.filter == "" || g.filter == id }

// showStore narrows the window to the store named id, or with "" shows
// every store, and shows all of its entries.
func (g *gui) showStore(id string) {
	g.filter = id
	g.showScope(scope{})
}

// refresh reads this Mac's stores, then lists each one's namespaces and
// entries on a goroutine of its own, so that a slow or failing store holds
// back no other; it never loads values.
func (g *gui) refresh() {
	g.listToken++
	token := g.listToken
	g.start(func() func() {
		config, err := g.loadStores()
		return func() {
			if token != g.listToken {
				return
			}
			if err != nil {
				// Stores that cannot be read say nothing of those shown,
				// which stay.
				g.storesErr = sentence(err)
				return
			}
			g.setStores(config)
			for _, connection := range config.All() {
				id, opened := connection.ID, g.open(connection.ID).store
				list := func(ctx context.Context, then func(listing, error)) {
					g.start(func() func() {
						namespaces, refs, attributes, err := opened.Listing(ctx)
						return func() {
							if token == g.listToken {
								then(listing{namespaces, refs, attributes}, err)
							}
						}
					})
				}
				current := func() {
					list(g.ctx, func(found listing, err error) { g.applyListing(id, found, err) })
				}
				if id == stores.ICloud || g.listed[id].read {
					current()
					continue
				}
				// A remote store answers only as fast as its remote, so one
				// the window shows nothing of yet lists the copy saved on
				// this Mac first, if it has one.
				list(store.WithSaved(g.ctx), func(found listing, err error) {
					if err == nil {
						g.applyListing(id, found, nil)
					}
					current()
				})
			}
		}
	})
}

// setStores takes this Mac's stores as just read. A store no longer among
// them goes with everything the window held of it, and so does one whose
// name now means another connection: what the window held was not that
// connection's.
func (g *gui) setStores(config stores.Config) {
	held := make(map[string]string, len(g.listed))
	for id := range g.listed {
		held[id] = g.target(id)
	}
	g.stores, g.storesRead, g.storesErr = config, true, ""
	if g.settings.redraw != nil {
		g.settings.redraw()
	}
	for id, target := range held {
		if g.target(id) != target {
			g.forget(id)
		}
	}
	for _, connection := range config.All() {
		if g.listed[connection.ID] == nil {
			g.listed[connection.ID] = new(listedStore)
		}
	}
}

// forget drops what the window held of the store named id, which left this
// Mac or was connected again to something else, with the form open on it. A
// choice inside it counts as moved away from, so that a write landing later
// leaves the window where this put it.
func (g *gui) forget(id string) {
	delete(g.listed, id)
	moved := false
	if g.filter == id {
		g.filter, moved = "", true
	}
	if g.scope.store == id {
		g.scope, moved = scope{}, true
	}
	if g.pick.store == id {
		g.pick = entryID{}
	}
	if g.form.open && g.form.store == id {
		g.closeForm()
		moved = true
	}
	if g.entry.store == id {
		g.entry, moved = entryID{}, true
		g.selectionChanged()
	}
	if moved {
		g.moves++
	}
}

// applyListing shows what a listing of the store named id found, or why it
// failed, leaving the other stores as they are. Everything a store shows
// arrives here.
func (g *gui) applyListing(id string, found listing, err error) {
	s := g.listed[id]
	s.loaded = true
	if err != nil {
		s.err = sentence(err)
		return
	}
	s.read, s.err = true, ""
	chosen := g.entry.ref
	// A value shown before another writer replaced it is no longer the
	// entry's; a read still running for the entry reads again.
	if g.entry.store == id && g.revealed != "" && !found.attributes[chosen].Modified.Equal(s.attributes[chosen].Modified) {
		g.revealed = ""
	}
	s.listing = found
	if g.scope.store == id && !slices.ContainsFunc(found.namespaces, func(n store.Namespace) bool { return n.Name == g.scope.namespace }) {
		g.scope = scope{}
	}
	if g.pick.store == id && !slices.Contains(found.refs, g.pick.ref) {
		g.pick = entryID{}
	}
	if g.entry.store == id && !slices.Contains(found.refs, chosen) {
		g.entry = entryID{}
		g.selectionChanged()
	}
}

// has reports whether the listings include id.
func (g *gui) has(id entryID) bool {
	s := g.listed[id.store]
	return s != nil && slices.Contains(s.refs, id.ref)
}

// attributes returns what the listings say of id, nothing once its store
// has left this Mac.
func (g *gui) attributes(id entryID) store.Attributes {
	if s := g.listed[id.store]; s != nil {
		return s.attributes[id.ref]
	}
	return store.Attributes{}
}

// count is how many entries the stores shown hold together.
func (g *gui) count() int {
	n := 0
	for id, s := range g.listed {
		if g.shown(id) {
			n += len(s.refs)
		}
	}
	return n
}

// rows lists what the list shows of the stores shown: the chosen
// namespace's entries, or every entry, or while searching, every entry
// whose name or namespace contains the query, ignoring case. Where the list
// mixes stores, each store's entries follow a heading, an entryID naming
// the store and no entry.
func (g *gui) rows() []entryID {
	query := strings.ToLower(g.query)
	all := query == "" && g.scope == (scope{})
	var rows []entryID
	for _, connection := range g.stores.All() {
		id := connection.ID
		if !g.shown(id) {
			continue
		}
		headed := g.filter != "" || !g.severalStores() || query == "" && !all
		for _, ref := range g.listed[id].refs {
			if all || query == "" && g.scope == (scope{id, ref.Namespace}) ||
				query != "" && (strings.Contains(strings.ToLower(ref.Name), query) || strings.Contains(strings.ToLower(ref.Namespace), query)) {
				if !headed {
					rows, headed = append(rows, entryID{store: id}), true
				}
				rows = append(rows, entryID{id, ref})
			}
		}
	}
	return rows
}

func isHeading(row entryID) bool { return row.ref == (store.Ref{}) }
