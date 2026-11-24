package main

import (
	"slices"
	"testing"

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
	if items := h.sidebarItems(); !slices.Equal(items, []string{"work 2 Stores Shown", "Namespaces", "app 1", "deploy 1"}) || !slices.Equal(h.g.rows(), alone) || !h.tt.HasText("2 entries") || h.tt.HasText("URL") {
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
