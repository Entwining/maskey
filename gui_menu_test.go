package main

import (
	"slices"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The Edit and View menus' commands for the chosen entry act as the list's
// keys do, only while the list has the focus. Delete… shows its key only
// while it can act, leaving Command-Backspace to a focused text field.
// Edit ▸ Copy, chosen with the mouse, sends the command Command-C sends.
func TestEntryItems(t *testing.T) {
	h := newHarness(t)
	m := h.g.entryItems
	keys := []struct {
		item          *mygo.MenuItem
		key           string
		whileDisabled bool
	}{
		{m.reveal, "CmdOrCtrl+Y", true},
		{m.edit, "CmdOrCtrl+L", true},
		{m.delete, "CmdOrCtrl+Backspace", false},
	}
	expect := func(when string, enabled ...*mygo.MenuItem) {
		t.Helper()
		for _, k := range keys {
			on := slices.Contains(enabled, k.item)
			want := k.key
			if !on && !k.whileDisabled {
				want = ""
			}
			if k.item.IsEnabled() != on || k.item.Accelerator != want {
				t.Errorf("%s: %s enabled %v, key %q", when, k.item.Label, k.item.IsEnabled(), k.item.Accelerator)
			}
		}
	}
	// A menu item posts its command, as Window.Update does to redraw after it.
	click := func(item *mygo.MenuItem) {
		t.Helper()
		item.Click(item, nil)
		(<-h.posted)()
		h.settle()
	}

	expect("no entry chosen")
	h.click("URL")
	h.focusList()
	expect("the list focused", m.reveal, m.edit, m.delete)
	click(m.reveal)
	if !h.g.showing(urlRef) || m.reveal.Label != "Hide Value" {
		t.Fatalf("Show Value: showing %v, item %q", h.g.showing(urlRef), m.reveal.Label)
	}
	h.tt.Command("copy")
	h.settle()
	if got := h.tt.Clipboard(); got != urlValue {
		t.Fatalf("Edit ▸ Copy copied %q", got)
	}
	click(m.delete)
	if a := h.alert(); a.message != "Delete this entry?" {
		t.Fatalf("Delete… asks %q", a.message)
	}
	h.escape()

	h.tt.Key(ui.Cmd, ui.KeyF)
	h.tt.Frame()
	expect("the search field focused")

	h.focusList()
	click(m.edit)
	if f := h.g.form; !f.open || f.from != urlRef {
		t.Fatalf("Edit Entry: form open %v for %s", f.open, f.from)
	}
	h.focusList()
	// No value shows behind the form, and the form already edits the entry.
	expect("the list focused beside the entry's form", m.delete)
}
