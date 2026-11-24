package main

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// remote is a second store, named work, for the window: its backend, and
// the program access changes asked of it, which the Maskey service
// approves.
type remote struct {
	backend *storetest.Backend
	config  stores.Config
	changes []string
}

// addRemote gives the window's Mac the store work beside iCloud Keychain.
func (h *harness) addRemote() *remote {
	h.t.Helper()
	r := &remote{backend: storetest.New()}
	r.backend.Put("deploy/SSH_HOST", []byte("synthetic-host-51c2"))
	r.backend.Put("app/API_TOKEN", []byte("synthetic-work-7d10"))
	r.config = stores.Config{Default: stores.ICloud, Stores: []stores.Connection{{ID: "work", Type: stores.Bitwarden, Region: "eu", ProjectName: "Deploy"}}}
	h.g.loadStores = func() (stores.Config, error) { return r.config, nil }
	h.g.remoteStore = func(stores.Connection) storeAccess {
		return storeAccess{store.New(r.backend, new(storetest.Recorder)), func(ctx context.Context, ref store.Ref, granted bool) error {
			r.changes = append(r.changes, ref.String())
			return r.backend.SetProgramAccess(ctx, ref.String(), "", granted)
		}}
	}
	return r
}

// work names an entry of the store addRemote adds.
func work(namespace, name string) entryID {
	return entryID{"work", store.Ref{Namespace: namespace, Name: name}}
}

func (r *remote) value(account string) (string, bool) {
	values, _ := r.backend.Read(context.Background(), []string{account})
	v, ok := values[account]
	return string(v.Value), ok
}

// chosen returns the bounds of the nth element with role that the user can
// choose, from the top. Two stores can show a namespace or an entry of one
// name, which Tester.Click cannot tell apart.
func (h *harness) chosen(role ui.Role, n int) ui.Rect {
	h.t.Helper()
	for _, a := range h.tt.AccessAll() {
		if a.Role != role || !a.Selectable {
			continue
		}
		if n == 0 {
			return a.Bounds
		}
		n--
	}
	h.t.Fatalf("no such element with role %v", role)
	return ui.Rect{}
}

// rowBounds returns where the list shows row, an entry or a heading.
func (h *harness) rowBounds(row entryID) ui.Rect {
	h.t.Helper()
	rows := h.g.rows()
	i := slices.Index(rows, row)
	if i < 0 {
		h.t.Fatalf("the list has no %v among %v", row, rows)
	}
	for _, a := range h.tt.AccessAll() {
		if a.Role != ui.RoleListItem {
			continue
		}
		if i == 0 {
			return a.Bounds
		}
		i--
	}
	h.t.Fatalf("the list shows no row for %v", row)
	return ui.Rect{}
}

func (h *harness) clickRow(row entryID) {
	h.t.Helper()
	r := h.rowBounds(row)
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	h.settle()
}

// chooseRow picks item from the context menu of id's row.
func (h *harness) chooseRow(id entryID, item string) {
	h.t.Helper()
	r := h.rowBounds(id)
	h.tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

// sidebarItems lists the sidebar's items and section titles as assistive
// technology names them, an item with its count.
func (h *harness) sidebarItems() []string {
	var items []string
	for _, a := range h.tt.AccessAll() {
		if a.Role == ui.RoleTreeItem {
			items = append(items, a.Name)
		}
	}
	return items
}

// clickScope clicks the sidebar's item of a store's namespace.
func (h *harness) clickScope(s scope) {
	h.t.Helper()
	n := 1 // after All Entries
	for _, connection := range h.g.stores.All() {
		namespaces := h.g.listed[connection.ID].namespaces
		if !h.g.shown(connection.ID) {
			continue
		}
		if connection.ID != s.store {
			n += len(namespaces)
			continue
		}
		i := slices.IndexFunc(namespaces, func(n store.Namespace) bool { return n.Name == s.namespace })
		if i < 0 {
			h.t.Fatalf("%s has no namespace %s", s.store, s.namespace)
		}
		r := h.chosen(ui.RoleTreeItem, n+i)
		h.tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		h.settle()
		return
	}
	h.t.Fatalf("no store %s", s.store)
}

// showStore chooses what the sidebar shows: a store, or All Stores.
func (h *harness) showStore(item string) {
	h.t.Helper()
	h.clickWithoutSettling("Stores Shown")
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
}

func (h *harness) reload() {
	h.t.Helper()
	h.g.refresh()
	h.settle()
}

// A Mac whose only store is iCloud Keychain names no store: its namespaces
// are the sidebar's one section, and neither the list nor the detail nor
// the form has a store to show.
func TestSingleStoreNamesNoStore(t *testing.T) {
	h := newHarness(t)
	h.click("app")
	h.click("URL")
	if items := h.sidebarItems(); !slices.Equal(items, []string{"All Entries 3", "Namespaces", "app 2", "db 1"}) ||
		h.tt.HasText(stores.ICloud) || h.tt.HasText("Store") || !h.tt.HasText("2 entries") {
		t.Fatalf("the sidebar lists %q, showing %q", items, h.tt.Texts())
	}
	h.click("All Entries")
	if rows := h.g.rows(); len(rows) != 3 || slices.ContainsFunc(rows, isHeading) {
		t.Fatalf("rows %v", rows)
	}
	h.click("New Entry")
	if _, ok := h.tt.AccessByRole(ui.RolePopUpButton, "Store"); ok || h.g.form.store != stores.ICloud {
		t.Fatalf("a choice of store %v, for a new entry in %q", ok, h.g.form.store)
	}
}

// With a second store the sidebar lists each store's namespaces under the
// store, All Entries counts and lists every store's entries under a heading
// for each, and a namespace shows only its own store's entries, as a search
// finds every store's.
func TestStoresListedTogether(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	icloudHeading, workHeading := entryID{store: stores.ICloud}, entryID{store: "work"}
	all := []entryID{icloudHeading, icloud("app", "API_TOKEN"), icloud("app", "URL"), icloud("db", "PASSWORD"),
		workHeading, work("app", "API_TOKEN"), work("deploy", "SSH_HOST")}
	if rows := h.g.rows(); !slices.Equal(rows, all) || !h.tt.HasText("5 entries") {
		t.Fatalf("rows %v, showing %q", rows, h.tt.Texts())
	}
	if items := h.sidebarItems(); !slices.Equal(items, []string{"All Entries 5 Stores Shown", "icloud", "app 2", "db 1", "work", "app 1", "deploy 1"}) {
		t.Fatalf("the sidebar lists %q", items)
	}

	h.clickScope(scope{"work", "app"})
	if rows := h.g.rows(); h.g.scope != (scope{"work", "app"}) || !slices.Equal(rows, []entryID{work("app", "API_TOKEN")}) || !h.tt.HasText("1 entry in work") {
		t.Fatalf("scope %v, rows %v, showing %q", h.g.scope, rows, h.tt.Texts())
	}
	h.clickScope(scope{stores.ICloud, "app"})
	if rows := h.g.rows(); !slices.Equal(rows, []entryID{icloud("app", "API_TOKEN"), icloud("app", "URL")}) || !h.tt.HasText("2 entries in icloud") {
		t.Fatalf("rows %v, showing %q", rows, h.tt.Texts())
	}

	h.replaceText("Search entries", "token")
	if rows := h.g.rows(); !slices.Equal(rows, []entryID{icloudHeading, icloud("app", "API_TOKEN"), workHeading, work("app", "API_TOKEN")}) || !h.tt.HasText("2 entries in all namespaces") {
		t.Fatalf("the search lists %v, showing %q", rows, h.tt.Texts())
	}
	h.replaceText("Search entries", "ssh")
	if rows := h.g.rows(); !slices.Equal(rows, []entryID{workHeading, work("deploy", "SSH_HOST")}) {
		t.Fatalf("the search lists %v", rows)
	}
}

// A store's heading in the list is no entry: a click chooses nothing, it
// has no menu, and the list's keys pass over it.
func TestStoreHeadingsAreNotEntries(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	heading := h.rowBounds(entryID{store: "work"})
	h.tt.ClickAt(heading.X+heading.W/2, heading.Y+heading.H/2)
	h.settle()
	h.tt.RightClickAt(heading.X+heading.W/2, heading.Y+heading.H/2)
	if menu := h.tt.Menu(); h.g.entry != (entryID{}) || h.g.row != -1 || len(menu) != 0 {
		t.Fatalf("after clicking the heading: entry %v, row %d, menu %q", h.g.entry, h.g.row, menu)
	}
	h.clickRow(icloud("db", "PASSWORD"))
	h.tt.Key(0, ui.KeyDown)
	h.settle()
	if h.g.entry != work("app", "API_TOKEN") {
		t.Fatalf("Down from the last entry of icloud chose %v", h.g.entry)
	}
	h.tt.Key(0, ui.KeyUp)
	h.settle()
	if h.g.entry != icloud("db", "PASSWORD") {
		t.Fatalf("Up from the first entry of work chose %v", h.g.entry)
	}
	h.tt.Key(0, ui.KeyHome)
	h.settle()
	if h.g.entry != icloud("app", "API_TOKEN") {
		t.Fatalf("Home chose %v", h.g.entry)
	}
}

// Two stores can hold the same reference. Those are two entries, and what
// the window does to the one chosen reaches only that one's store.
func TestSameReferenceInTwoStores(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	h.reload()
	token := work("app", "API_TOKEN")
	h.clickRow(token)
	if h.g.entry != token || !h.tt.HasText("Store") {
		t.Fatalf("entry %v, showing %q", h.g.entry, h.tt.Texts())
	}
	h.click("Show Value")
	if h.g.revealed != "synthetic-work-7d10" || len(h.backend.Reads) != 0 {
		t.Fatalf("the value shown is work's %v; reads of iCloud Keychain %q", h.g.revealed == "synthetic-work-7d10", h.backend.Reads)
	}
	h.click("Hide Value")
	h.click("Copy Value")
	if h.clipboard.text != "synthetic-work-7d10" || len(h.backend.Reads) != 0 {
		t.Fatalf("the value copied is work's %v; reads of iCloud Keychain %q", h.clipboard.text == "synthetic-work-7d10", h.backend.Reads)
	}
	// The service changes a remote entry's program access, without the
	// window's own approval.
	h.click("Program access")
	if !slices.Equal(r.changes, []string{"app/API_TOKEN"}) || h.dialogs != 0 || !h.g.attributes(token).ProgramAccess || h.g.attributes(icloud("app", "API_TOKEN")).ProgramAccess {
		t.Fatalf("changes %v, the window's dialogs %d, listed %+v and %+v", r.changes, h.dialogs, h.g.attributes(token), h.g.attributes(icloud("app", "API_TOKEN")))
	}
	if _, err := h.backend.Get(context.Background(), "app/API_TOKEN"); !store.IsKind(err, store.KindNotGranted) {
		t.Fatalf("the entry in iCloud Keychain changed: %v", err)
	}
	h.chooseRow(token, "Edit")
	if f := h.g.form; !f.open || f.from != token || f.value != "synthetic-work-7d10" {
		t.Fatalf("the form is open %v for %v, holding work's value %v", f.open, f.from, f.value == "synthetic-work-7d10")
	}
	h.typeValue("synthetic-work-new")
	h.click("Save")
	if v, _ := r.value("app/API_TOKEN"); v != "synthetic-work-new" {
		t.Fatal("the edit did not reach work")
	}
	if v, _ := h.value("app/API_TOKEN"); v != tokenValue {
		t.Fatal("the edit changed the entry in iCloud Keychain")
	}

	h.chooseRow(token, "Delete…")
	h.answer("Delete")
	_, inWork := r.value("app/API_TOKEN")
	if _, inICloud := h.value("app/API_TOKEN"); inWork || !inICloud || h.g.has(token) || !h.g.has(icloud("app", "API_TOKEN")) {
		t.Fatalf("after the delete: in work %v, in iCloud Keychain %v, rows %v", inWork, inICloud, h.g.rows())
	}

	// The other entry of that reference is iCloud Keychain's, which the
	// window's own approval covers.
	h.clickRow(icloud("app", "API_TOKEN"))
	h.click("Show Value")
	shown := h.g.revealed == tokenValue
	h.click("Program access")
	if !shown || h.dialogs != 1 || len(r.changes) != 1 {
		t.Fatalf("iCloud Keychain's value shown %v, dialogs %d, changes of work %v", shown, h.dialogs, r.changes)
	}
}

// A store that cannot be listed says so in its section and over the list,
// and neither hides nor waits for the others; what it listed before stays.
func TestFailingStoreLeavesOthersShown(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	failing := true
	r.backend.Before = func(op, _ string) error {
		if op == "items" && failing {
			return &store.Error{Kind: store.KindUnauthenticated, Message: "synthetic refusal"}
		}
		return nil
	}
	h.reload()
	if !h.tt.HasText("URL") || !slices.Contains(h.tt.Texts(), "Couldn’t read") || !h.tt.HasText("Couldn’t read work: Synthetic refusal.") || h.tt.HasText("SSH_HOST") {
		t.Fatalf("showing %q", h.tt.Texts())
	}
	if r := h.rowBounds(icloud("app", "URL")); r.H == 0 {
		t.Fatal("iCloud Keychain's entries take no room")
	}
	failing = false
	h.click("Try Again")
	if !h.tt.HasText("SSH_HOST") || h.tt.HasText("Couldn’t read") {
		t.Fatalf("after trying again: %q", h.tt.Texts())
	}
	failing = true
	h.reload()
	if !h.tt.HasText("SSH_HOST") || !slices.Contains(h.tt.Texts(), "Couldn’t reload") || !h.tt.HasText("Couldn’t reload work: Synthetic refusal.") {
		t.Fatalf("after a failed reload: %q", h.tt.Texts())
	}

	// A store still listing holds back no other.
	listing, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(sync.OnceFunc(func() { close(release) }))
	r.backend.Before = func(op, _ string) error {
		if op == "items" {
			close(listing)
			<-release
		}
		return nil
	}
	h.backend.Put("app/NEW_KEY", []byte("synthetic-new-31d2"))
	h.g.refresh()
	h.applyPostedUntil(listing)
	h.applyPostedWhile(func() bool { return !h.g.has(icloud("app", "NEW_KEY")) })
}

// A new entry goes to the store of the namespace chosen, or under All
// Entries to this Mac's default store, unless the form chooses another.
func TestNewEntryStore(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	h.reload()
	h.click("New Entry")
	if _, ok := h.tt.AccessByRole(ui.RolePopUpButton, "Store"); !ok || h.g.form.store != stores.ICloud {
		t.Fatalf("under All Entries: a choice of store %v, store %q", ok, h.g.form.store)
	}
	h.clickScope(scope{"work", "deploy"})
	h.click("New Entry")
	if f := h.g.form; f.store != "work" || f.namespace != "deploy" {
		t.Fatalf("in work's deploy: store %q, namespace %q", f.store, f.namespace)
	}
	h.clickScope(scope{stores.ICloud, "db"})
	h.click("New Entry")
	if f := h.g.form; f.store != stores.ICloud || f.namespace != "db" {
		t.Fatalf("in icloud's db: store %q, namespace %q", f.store, f.namespace)
	}

	r.config.Default = "work"
	h.showStore("All Stores")
	h.reload()
	h.click("New Entry")
	h.typeNamespace("app")
	h.typeName("NEW_KEY")
	h.typeValue("synthetic-new-31d2")
	h.click("Add")
	_, inICloud := h.value("app/NEW_KEY")
	if v, _ := r.value("app/NEW_KEY"); v != "synthetic-new-31d2" || inICloud || h.g.entry != work("app", "NEW_KEY") {
		t.Fatalf("with work the default: in work %v, in iCloud Keychain %v, entry %v", v == "synthetic-new-31d2", inICloud, h.g.entry)
	}

	// The form's choice overrides both, and the list follows the entry to
	// the store's namespace.
	h.clickScope(scope{"work", "deploy"})
	h.click("New Entry")
	// The field's label and the sidebar show the same texts as the choice
	// and its options, so the choice is opened where it is and the keys
	// choose.
	choice, _ := h.tt.AccessByRole(ui.RolePopUpButton, "Store")
	h.tt.ClickAt(choice.Bounds.X+choice.Bounds.W/2, choice.Bounds.Y+choice.Bounds.H/2)
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyEnter)
	h.typeName("OTHER_KEY")
	h.typeValue("synthetic-other-77aa")
	h.click("Add")
	_, inWork := r.value("deploy/OTHER_KEY")
	if v, _ := h.value("deploy/OTHER_KEY"); v != "synthetic-other-77aa" || inWork || h.g.entry != icloud("deploy", "OTHER_KEY") || h.g.scope != (scope{stores.ICloud, "deploy"}) {
		t.Fatalf("with icloud chosen: in iCloud Keychain %v, in work %v, entry %v, scope %v", v == "synthetic-other-77aa", inWork, h.g.entry, h.g.scope)
	}
}

// A store removed from this Mac leaves the window with everything chosen
// inside it: its namespace, its entry, and the form open on one.
func TestRemovedStoreLeavesNothingChosen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inside func(h *harness)
	}{
		{"an entry in its namespace", func(h *harness) {
			h.clickScope(scope{"work", "app"})
			h.clickRow(work("app", "API_TOKEN"))
			h.click("Show Value")
		}},
		{"an entry under All Entries", func(h *harness) { h.clickRow(work("app", "API_TOKEN")) }},
		{"an entry being edited", func(h *harness) { h.chooseRow(work("app", "API_TOKEN"), "Edit") }},
		{"a new entry for it", func(h *harness) {
			h.clickScope(scope{"work", "deploy"})
			h.click("New Entry")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			r := h.addRemote()
			h.reload()
			tc.inside(h)
			r.config.Stores = nil
			h.reload()
			if h.g.scope != (scope{}) || h.g.entry != (entryID{}) || h.g.form.open || h.g.revealed != "" || h.g.listed["work"] != nil {
				t.Fatalf("scope %v, entry %v, form open %v, a value shown %v", h.g.scope, h.g.entry, h.g.form.open, h.g.revealed != "")
			}
			if items := h.sidebarItems(); !slices.Equal(items, []string{"All Entries 3", "Namespaces", "app 2", "db 1"}) || len(h.g.rows()) != 3 || h.tt.HasText("SSH_HOST") {
				t.Fatalf("the sidebar lists %q, rows %v", items, h.g.rows())
			}
		})
	}
}

// The answer to Delete… is for the entry of the store it was asked of: once
// that store has left this Mac, neither it nor the entry of the same name
// in another store is deleted.
func TestDeleteAnswersForTheStoreAsked(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	h.reload()
	h.chooseRow(work("app", "API_TOKEN"), "Delete…")
	r.config.Stores = nil
	h.reload()
	h.answer("Delete")
	_, inICloud := h.value("app/API_TOKEN")
	if _, inWork := r.value("app/API_TOKEN"); !inICloud || !inWork || len(h.openAlerts()) != 0 {
		t.Fatalf("in iCloud Keychain %v, in work %v, alerts %d", inICloud, inWork, len(h.openAlerts()))
	}
}

// Stores that cannot be read leave the stores shown and the open form, and
// before they are first read the window has no store to add an entry to.
func TestStoresThatCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	h.chooseRow(work("app", "API_TOKEN"), "Edit")
	unreadable := func() (stores.Config, error) {
		return stores.Config{Default: stores.ICloud}, &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}
	}
	h.g.loadStores = unreadable
	h.reload()
	if f := h.g.form; !f.open || f.from != work("app", "API_TOKEN") || len(h.g.stores.Stores) != 1 || !h.tt.HasText("SSH_HOST") ||
		!h.tt.HasText("Couldn’t reload: This Mac's store configuration cannot be read.") {
		t.Fatalf("form open %v for %v, stores %+v, showing %q", f.open, f.from, h.g.stores, h.tt.Texts())
	}

	starting := newHarness(t)
	starting.g.storesRead = false
	starting.g.loadStores = unreadable
	starting.reload()
	starting.g.openNew()
	starting.settle()
	if starting.g.form.open {
		t.Fatal("a form opened before the stores were read")
	}

	// With no entry to keep showing, the window says what it could not read.
	empty := newHarnessWith(t, storetest.New())
	empty.g.loadStores = unreadable
	empty.reload()
	if !empty.tt.HasText("Can’t Read This Mac’s Stores") {
		t.Fatalf("showing %q", empty.tt.Texts())
	}
}

// With no entry to show, the list says which listing failed, each store's
// failure when several did.
func TestStoresWithNothingToShow(t *testing.T) {
	h := newHarnessWith(t, storetest.New())
	r := h.addRemote()
	refuse := func(op, _ string) error {
		if op == "items" {
			return &store.Error{Kind: store.KindUnauthenticated, Message: "synthetic refusal"}
		}
		return nil
	}
	r.backend.Before = refuse
	h.reload()
	if !h.tt.HasText("Can’t Read the Store") || !h.tt.HasText("Synthetic refusal.") {
		t.Fatalf("with work refused: %q", h.tt.Texts())
	}
	h.backend.Before = refuse
	h.reload()
	if !h.tt.HasText("Can’t Read the Stores") || !h.tt.HasText("icloud: Synthetic refusal.\nwork: Synthetic refusal.") {
		t.Fatalf("with both refused: %q", h.tt.Texts())
	}
	h.backend.Before, r.backend.Before = nil, nil
	h.click("Try Again")
	if !h.tt.HasText("SSH_HOST") {
		t.Fatalf("after trying again: %q", h.tt.Texts())
	}
}

// A store without entries has no section in the sidebar and no heading in
// the list, while the other store keeps its ID as its title. The form's
// choice of store still offers it, and its section comes with its first
// entry.
func TestEmptyStoreAppearsWithItsFirstEntry(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	r.backend = storetest.New()
	h.reload()
	if items := h.sidebarItems(); !slices.Equal(items, []string{"All Entries 3 Stores Shown", "icloud", "app 2", "db 1"}) || !isHeading(h.g.rows()[0]) || len(h.g.rows()) != 4 {
		t.Fatalf("the sidebar lists %q, rows %v", items, h.g.rows())
	}
	h.click("New Entry")
	choice, _ := h.tt.AccessByRole(ui.RolePopUpButton, "Store")
	h.tt.ClickAt(choice.Bounds.X+choice.Bounds.W/2, choice.Bounds.Y+choice.Bounds.H/2)
	h.tt.Key(0, ui.KeyDown)
	h.tt.Key(0, ui.KeyEnter)
	h.typeNamespace("deploy")
	h.typeName("FIRST_KEY")
	h.typeValue("synthetic-first-4c9e")
	h.click("Add")
	if v, _ := r.value("deploy/FIRST_KEY"); v != "synthetic-first-4c9e" || h.g.entry != work("deploy", "FIRST_KEY") {
		t.Fatalf("stored in work %v, entry %v", v == "synthetic-first-4c9e", h.g.entry)
	}
	if items := h.sidebarItems(); !slices.Equal(items, []string{"All Entries 4 Stores Shown", "icloud", "app 2", "db 1", "work", "deploy 1"}) {
		t.Fatalf("after the first entry the sidebar lists %q", items)
	}
}
