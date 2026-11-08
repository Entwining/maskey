package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo"
)

// A newer version renames the update menu item and shows a button under the
// sidebar that opens the update window; a week after each version is found
// the window opens on its own, once for that version. Losing the version
// restores both.
func TestUpdateShows(t *testing.T) {
	h := newHarness(t)
	checks := 0
	h.g.updateItem = &mygo.MenuItem{Label: "Check for Updates…"}
	h.g.checkLabel = h.g.updateItem.Label
	h.g.checkForUpdates = func() { checks++ }
	expect := func(when, label, button string) {
		t.Helper()
		h.tt.Frame()
		texts, shown := h.tt.Texts(), ""
		if i := slices.IndexFunc(texts, func(s string) bool { return strings.HasPrefix(s, "Update to") }); i >= 0 {
			shown = texts[i]
		}
		if h.g.updateItem.Label != label || shown != button {
			t.Fatalf("%s: the menu item reads %q, the window shows %q", when, h.g.updateItem.Label, texts)
		}
	}
	remind := func(when string, want int) {
		t.Helper()
		h.g.remindUpdate()
		if checks != want {
			t.Fatalf("%s: the update window opened %d times, want %d", when, checks, want)
		}
	}

	expect("before a check", "Check for Updates…", "")
	remind("with no newer version", 0)
	h.g.showUpdate("0.0.8")
	expect("after a check found 0.0.8", "Update to Maskey 0.0.8…", "Update to 0.0.8")
	h.click("Update to 0.0.8")
	if checks != 1 {
		t.Fatalf("the button opened the update window %d times", checks)
	}

	remind("before a week passed", 1)
	h.g.updateSince = h.g.updateSince.Add(-updateReminder)
	// A later check reports the version again.
	h.g.showUpdate("0.0.8")
	remind("a week later", 2)
	remind("again", 2)

	// A later check can find a newer version in place of the one shown.
	h.g.showUpdate("0.0.9")
	expect("after a check found 0.0.9", "Update to Maskey 0.0.9…", "Update to 0.0.9")
	remind("as 0.0.9 is found", 2)
	h.g.updateSince = h.g.updateSince.Add(-updateReminder)
	remind("a week after finding 0.0.9", 3)

	h.g.showUpdate("")
	expect("after the version was skipped", "Check for Updates…", "")
	remind("after the version was skipped", 3)
	// A failed check clears the version until a later check finds it again.
	h.g.showUpdate("0.0.9")
	h.g.updateSince = h.g.updateSince.Add(-updateReminder)
	remind("a week after finding 0.0.9 again", 3)
}
