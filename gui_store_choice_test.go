package main

import (
	"image"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// The sidebar's choice of store shows one store alone, as a Mac with one
// store shows its own: its namespaces without the store's name, its entries
// without a heading, and a search and a new entry within it.
func TestStoreShownAlone(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	r.config.Stores = append(r.config.Stores, stores.Connection{ID: "vault", Type: stores.Bitwarden})
	empty := storetest.New()
	h.g.remoteStore = func(connection stores.Connection) storeAccess {
		if connection.ID == "vault" {
			return storeAccess{store: store.New(empty, new(storetest.Recorder))}
		}
		return storeAccess{store: store.New(r.backend, new(storetest.Recorder))}
	}
	h.reload()
	h.clickWithoutSettling("Stores Shown")
	if menu := h.tt.Menu(); !slices.Equal(menu, []string{"All Stores", "-", "icloud", "vault", "work"}) {
		t.Fatalf("the choice offers %q", menu)
	}
	h.tt.CloseMenu()

	h.showStore("work")
	alone := []entryID{work("app", "API_TOKEN"), work("deploy", "SSH_HOST")}
	if items := h.sidebarItems(); !slices.Equal(items, []string{"work 2", "Namespaces", "app 1", "deploy 1"}) || !slices.Equal(h.g.rows(), alone) || !h.tt.HasText("2 entries") || h.tt.HasText("URL") {
		t.Fatalf("work alone: the sidebar lists %q, rows %v, showing %q", items, h.g.rows(), h.tt.Texts())
	}
	h.typeInto("Search entries", "ss")
	if rows := h.g.rows(); !slices.Equal(rows, alone[1:]) {
		t.Fatalf("a search in work finds %v", rows)
	}
	// The first item is the way back from a namespace, and its menu is not
	// in the way of that click.
	h.clickScope(scope{"work", "deploy"})
	h.clickWithoutSettling("Stores Shown")
	h.tt.CloseMenu()
	h.settle()
	if h.g.scope != (scope{"work", "deploy"}) {
		t.Fatalf("opening the menu chose %+v", h.g.scope)
	}
	h.click("work")
	if h.g.scope != (scope{}) || h.g.filter != "work" || !slices.Equal(h.g.rows(), alone) {
		t.Fatalf("after the first item: scope %+v, showing %q alone, rows %v", h.g.scope, h.g.filter, h.g.rows())
	}
	h.click("New Entry")
	if h.g.form.store != "work" {
		t.Fatalf("a new entry goes to %q", h.g.form.store)
	}
	h.click("Cancel")

	// A store without entries has no section among the others, and is
	// shown alone like any other.
	h.showStore("vault")
	if len(h.g.rows()) != 0 || !h.tt.HasText("No Entries") {
		t.Fatalf("an empty store alone: rows %v, showing %q", h.g.rows(), h.tt.Texts())
	}

	// A store that left this Mac leaves every store showing.
	h.showStore("work")
	r.config.Stores = nil
	h.reload()
	if h.g.filter != "" || !h.tt.HasText("URL") {
		t.Fatalf("after work left: showing %q alone, %q", h.g.filter, h.tt.Texts())
	}
}

// A store switch keeps the namespace and the entry chosen while their store
// is shown, and gives way to all of the entries shown once it is hidden,
// never to the namespace of the same name in the store shown instead.
func TestStoreSwitchKeepsWhatStaysShown(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	r.config.Stores = append(r.config.Stores, stores.Connection{ID: "vault", Type: stores.Bitwarden})
	empty := storetest.New()
	h.g.remoteStore = func(connection stores.Connection) storeAccess {
		if connection.ID == "vault" {
			return storeAccess{store: store.New(empty, new(storetest.Recorder))}
		}
		return storeAccess{store: store.New(r.backend, new(storetest.Recorder))}
	}
	h.reload()
	h.clickScope(scope{stores.ICloud, "app"})
	h.clickRow(icloud("app", "URL"))
	h.click("Show Value")
	for _, shown := range []string{stores.ICloud, stores.ICloud, "All Stores"} {
		h.showStore(shown)
		if h.g.scope != (scope{stores.ICloud, "app"}) || h.g.entry != icloud("app", "URL") || !h.tt.HasText(urlValue) {
			t.Fatalf("after %s: scope %+v, entry %v, showing %q", shown, h.g.scope, h.g.entry, h.tt.Texts())
		}
	}
	// A search ends, and the namespace it hid is chosen again.
	h.typeInto("Search entries", "pass")
	h.showStore(stores.ICloud)
	if h.g.query != "" || h.g.scope != (scope{stores.ICloud, "app"}) {
		t.Fatalf("after a search: query %q, scope %+v", h.g.query, h.g.scope)
	}
	h.showStore("work")
	if h.g.scope != (scope{}) || !slices.Equal(h.g.rows(), []entryID{work("app", "API_TOKEN"), work("deploy", "SSH_HOST")}) {
		t.Fatalf("after work: scope %+v, rows %v", h.g.scope, h.g.rows())
	}
	// Straight from an entry whose value shows to a store without entries,
	// whose empty list chooses nothing in its place.
	h.showStore(stores.ICloud)
	h.clickRow(icloud("app", "URL"))
	h.click("Show Value")
	h.showStore("vault")
	if h.g.entry != (entryID{}) || h.g.revealed != "" || h.tt.HasText(urlValue) {
		t.Fatalf("after vault: entry %v, revealed %v", h.g.entry, h.g.revealed != "")
	}
}

// differs reports whether two frames differ inside r.
func differs(a, b *image.RGBA, r ui.Rect) bool {
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return true
			}
		}
	}
	return false
}

// The menu of stores has a button as high as the sidebar's first item, over
// the item's end, which the pointer, Tab, and assistive technology each
// reach apart from the item, and which shows the pointer over it and the
// keyboard focus.
func TestStoreMenuButton(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	h.clickScope(scope{stores.ICloud, "db"})
	item, ok1 := h.tt.AccessByRole(ui.RoleTreeItem, "All Entries 5")
	button, ok2 := h.tt.AccessByRole(ui.RoleMenuButton, "Stores Shown")
	if !ok1 || !ok2 || button.HasPopup != ui.PopupMenu {
		t.Fatalf("the item %v and the menu's button %+v", ok1, button)
	}
	b, i := button.Bounds, item.Bounds
	if b.H != i.H || b.W < i.H || b.Y != i.Y || b.X+b.W != i.X+i.W {
		t.Fatalf("the button at %v over the item at %v", b, i)
	}

	// The pointer: a press near the button's corner opens the menu and
	// chooses nothing, and the rest of the item shows every entry.
	rest := h.tt.Image()
	h.tt.Move(b.X+b.W/2, b.Y+b.H/2)
	h.tt.Frame()
	if !differs(rest, h.tt.Image(), b) {
		t.Fatal("the button looks the same with the pointer over it")
	}
	h.tt.ClickAt(b.X+2, b.Y+2)
	if menu := h.tt.Menu(); len(menu) == 0 || h.g.scope != (scope{stores.ICloud, "db"}) {
		t.Fatalf("a press at the button's corner: menu %q, scope %+v", menu, h.g.scope)
	}
	h.tt.CloseMenu()
	h.tt.ClickAt(b.X-8, b.Y+b.H/2)
	h.settle()
	if h.g.scope != (scope{}) {
		t.Fatalf("a press before the button chose %+v", h.g.scope)
	}

	// The keyboard: Tab goes from the sidebar to the button, which rings
	// it, and Space opens the menu.
	h.tt.Move(0, 0)
	h.tt.Frame()
	unfocused := h.tt.Image()
	h.tt.Key(0, ui.KeyTab)
	h.tt.Frame()
	if a, _ := h.tt.AccessByRole(ui.RoleMenuButton, "Stores Shown"); !a.Focused {
		t.Fatal("Tab from the sidebar leaves the button")
	}
	// The item's look follows the sidebar's focus, so the ring tells only
	// past the item's end, level with the button's middle.
	ring := ui.Rect{X: b.X + b.W, Y: b.Y + b.H/4, W: 3, H: b.H / 2}
	if !differs(unfocused, h.tt.Image(), ring) {
		t.Fatal("the button looks the same with the keyboard focus")
	}
	h.tt.Key(0, ui.KeySpace)
	if err := h.tt.ChooseMenuItem("work"); err != nil {
		t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
	h.settle()
	if h.g.filter != "work" {
		t.Fatalf("the menu from the keyboard shows %q", h.g.filter)
	}
}
