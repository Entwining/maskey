package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func (h *harness) find(label string) ui.Rect {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("%s is absent: %q", label, h.tt.Texts())
	}
	return r
}

func near(a, b float32) bool { return a-b < 0.5 && b-a < 0.5 }

func sameRect(a, b ui.Rect) bool {
	return near(a.X, b.X) && near(a.Y, b.Y) && near(a.W, b.W) && near(a.H, b.H)
}

// Opening, saving, and closing a form only change what the detail's two
// button slots show: Save and Add take Edit's place, Cancel and Close the
// empty slot before it, and the search field stays where it is. Find gives
// a plain button's title, centered in it, so buttons compare by center.
func TestToolbarStaysInPlace(t *testing.T) {
	for _, width := range []int{820, 980, 1400} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(width, 600)
			h.tt.Frame()
			search := h.find("Search entries")
			stays := func(when string) {
				t.Helper()
				if got := h.find("Search entries"); !sameRect(got, search) {
					t.Fatalf("%s, the search field moved from %v to %v", when, search, got)
				}
			}
			at := func(label string, x, y float32) {
				t.Helper()
				if r := h.find(label); !near(r.X+r.W/2, x) || !near(r.Y+r.H/2, y) {
					t.Fatalf("%s at %v is not centered on (%v, %v)", label, r, x, y)
				}
			}
			h.click("URL")
			stays("choosing an entry")
			edit := h.find("Edit")
			x, y := edit.X+edit.W/2, edit.Y+edit.H/2
			h.click("Edit")
			stays("opening the form")
			at("Save", x, y)
			save := h.find("Save")
			if save.W != toolbarButtonWidth || search.X-(save.X+save.W) != 8 {
				t.Fatalf("Save at %v is not in the slot before the search field at %v", save, search)
			}
			cancelX := save.X - 8 - toolbarButtonWidth/2
			at("Cancel", cancelX, y)
			h.typeValue("synthetic-url-new")
			started, release := h.pause("edit")
			h.clickWithoutSettling("Save")
			h.waitFor(started)
			h.tt.Frame()
			at("Close", cancelX, y)
			release()
			h.settle()
			at("Edit", x, y)
			stays("after saving")
			h.click("New Entry")
			at("Add", x, y)
			at("Cancel", cancelX, y)
			stays("opening a new entry")
		})
	}
}

// At the window's minimum width the toolbar holds both buttons and the
// search field with its clear button, inside the window.
func TestToolbarFitsMinimumWidth(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(820, 600)
	h.choose("URL", "Edit")
	h.typeInto("Search entries", "u")
	search, clear, save := h.find("Search entries"), h.find("Clear"), h.find("Save")
	if search.X+search.W > 820-12+0.5 || clear.W < 16 || clear.X+clear.W > search.X+search.W-8 {
		t.Fatalf("the search field at %v overflows the window, or its clear button at %v the field", search, clear)
	}
	if save.X+save.W+8 > search.X+0.5 {
		t.Fatalf("Save at %v overlaps the search field at %v", save, search)
	}
	if cancel, list := h.find("Cancel"), h.find("Entries"); cancel.X < list.X+list.W {
		t.Fatalf("Cancel at %v overlaps the list at %v", cancel, list)
	}
}

// The search field works while a form is open, which keeps its entry and
// draft whatever the list shows. Escape there clears the query, and with no
// query does nothing, rather than closing the form.
func TestSearchWhileEditing(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-draft")
	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	if !h.tt.Focused("Search entries") {
		t.Fatal("Cmd-F did not focus the search field")
	}
	h.tt.Type("pass")
	h.tt.Frame()
	kept := func(when string) {
		t.Helper()
		if f := h.g.form; !f.open || f.from.Name != "URL" || f.value != "synthetic-draft" || h.g.confirmLeave {
			t.Fatalf("%s: form open %v for %s, asking %v", when, f.open, f.from, h.g.confirmLeave)
		}
	}
	if h.tt.HasText("API_TOKEN") {
		t.Fatalf("the search shows %q", h.tt.Texts())
	}
	kept("searching")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	if h.g.query != "" {
		t.Fatalf("Escape left the query %q", h.g.query)
	}
	kept("clearing the search")
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Frame()
	kept("Escape in the empty search field")
}
