package main

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Space and Cmd-C act on the chosen entry while the list has the focus,
// and Cmd-C copies text, not the value, in the search field.
func TestListKeys(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	h.tt.Key(0, ui.KeySpace)
	h.settle()
	if !h.tt.HasText(tokenValue) {
		t.Fatalf("Space did not show the value: %q", h.tt.Texts())
	}
	h.tt.Key(0, ui.KeySpace)
	h.settle()
	if h.tt.HasText(tokenValue) {
		t.Fatal("Space did not hide the value")
	}
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.settle()
	if got := h.clipboard.text; got != tokenValue {
		t.Fatalf("Cmd-C copied %q", got)
	}
	h.typeInto("Search entries", "tok")
	h.tt.Command("selectAll")
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.settle()
	if got := h.tt.Clipboard(); got != "tok" || h.clipboard.change != 1 {
		t.Fatalf("Cmd-C in the search field copied %q, and the value %d times", got, h.clipboard.change)
	}
}

// A list key held down acts on its press alone: the value stays shown, is
// read once for showing and once for copying, and the delete asks once.
func TestListKeysIgnoreRepeats(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	h.tt.Key(0, ui.KeySpace)
	h.settle()
	h.tt.KeyRepeat(0, ui.KeySpace)
	h.settle()
	if !h.tt.HasText(tokenValue) {
		t.Fatalf("a held Space hid the value: %q", h.tt.Texts())
	}
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.settle()
	h.tt.KeyRepeat(ui.Cmd, ui.KeyC)
	h.settle()
	if h.clipboard.change != 1 {
		t.Fatalf("a held Cmd-C copied %d times", h.clipboard.change)
	}
	if want := []string{"reveal", "copy"}; !slices.Equal(h.reads, want) {
		t.Fatalf("reads for %q, want %q", h.reads, want)
	}
	h.tt.Key(ui.Cmd, ui.KeyBackspace)
	h.settle()
	h.tt.KeyRepeat(ui.Cmd, ui.KeyBackspace)
	h.settle()
	if n := len(h.openAlerts()); n != 1 {
		t.Fatalf("a held Cmd-Backspace asked %d times", n)
	}
}

// The list's keys follow the row's rules: a value being saved cannot be
// shown.
func TestListKeysWaitForWrites(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-url-new")
	started, release := h.pause("edit")
	h.clickWithoutSettling("Save")
	h.waitFor(started)
	h.clickWithoutSettling("Close")
	h.tt.Frame()
	h.clickWithoutSettling("URL")
	h.tt.Key(0, ui.KeySpace)
	h.tt.Frame()
	if h.g.readAction != "" {
		t.Fatal("Space read a value while it was being saved")
	}
	release()
	h.settle()
}
