package main

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Two alerts can wait at once: Cmd-Backspace asks to delete the form's
// entry, and a quit then asks about the form's changes. The question about
// the form no longer holds once the delete has closed it or still runs, so
// its Save writes nothing and the quit waits for the user to ask again.
func TestLeavingAnswerAfterDelete(t *testing.T) {
	for _, when := range []string{"after the delete", "during the delete"} {
		t.Run(when, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			var release func()
			if when == "during the delete" {
				_, release = h.pause("delete")
			}
			var mu sync.Mutex
			var ops []string
			held := h.backend.Before
			h.backend.Before = func(op, account string) error {
				mu.Lock()
				ops = append(ops, op)
				mu.Unlock()
				if held != nil {
					return held(op, account)
				}
				return nil
			}
			h.focusList()
			h.tt.Key(ui.Cmd, ui.KeyBackspace)
			h.settle()
			left := false
			if !h.g.ask(func() { left = true }, true) {
				t.Fatal("quitting with a changed form asked nothing")
			}
			h.settle()
			h.answer("Delete")
			h.answer("Save")
			if release != nil {
				release()
				h.settle()
			}
			if _, kept := h.value("app/URL"); kept || left || h.g.form.open || h.g.confirmLeave || len(h.openAlerts()) != 0 {
				t.Fatalf("kept %v, left %v, form open %v, asking %v, alerts %d", kept, left, h.g.form.open, h.g.confirmLeave, len(h.openAlerts()))
			}
			mu.Lock()
			defer mu.Unlock()
			if slices.Contains(ops, "edit") {
				t.Fatalf("the stale Save wrote: %q", ops)
			}
		})
	}
}

// An alert answered after the window closed changes nothing.
func TestAnswerAfterClosing(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Delete…")
	h.g.close()
	h.answer("Delete")
	if _, kept := h.value("app/URL"); !kept {
		t.Fatal("the answer deleted the entry after the window closed")
	}
}

// An alert that can't show, as while the app quits, counts as its cancel
// button: the prompt about changes keeps the form rather than saving it.
func TestAlertThatCannotShow(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-url-new")
	h.g.showMessage = func(alert) (int, error) {
		return 0, errors.New("synthetic loop stopped")
	}
	left := false
	h.g.ask(func() { left = true }, true)
	h.settle()
	if v, _ := h.value("app/URL"); v != urlValue || left || !h.g.form.open || h.g.form.value != "synthetic-url-new" || h.g.confirmLeave {
		t.Fatalf("stored %q, left %v, form open %v, asking %v", v, left, h.g.form.open, h.g.confirmLeave)
	}
}

func TestCancelsAlert(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keyCode   uint16
		modifiers uint
		character string
		cancels   bool
	}{
		{"Escape", escapeKeyCode, 0, "\x1b", true},
		{"Command-Period", 47, nsCommand, ".", true},
		{"Command-Shift typing a period", 47, nsCommand | nsShift, ".", true},
		{"Command-Shift typing >", 47, nsCommand | nsShift, ">", false},
		{"Shift-Escape", escapeKeyCode, nsShift, "\x1b", false},
		{"Period", 47, 0, ".", false},
		{"Command-Option-Period", 47, nsCommand | nsOption, ".", false},
		// Caps Lock and the function key leave the key as it is.
		{"Escape with Caps Lock", escapeKeyCode, 1 << 16, "\x1b", true},
	} {
		if got := cancelsAlert(tc.keyCode, tc.modifiers, tc.character); got != tc.cancels {
			t.Errorf("%s: cancels %v", tc.name, got)
		}
	}
}
