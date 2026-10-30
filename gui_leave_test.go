package main

import (
	"slices"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

var (
	urlRef      = store.Ref{Namespace: "app", Name: "URL"}
	passwordRef = store.Ref{Namespace: "db", Name: "PASSWORD"}
)

// The actions that replace an open form, and how to tell each happened.
var leavings = []struct {
	name     string
	act      func(h *harness)
	happened func(g *gui) bool
}{
	{"another row", func(h *harness) { h.clickWithoutSettling("PASSWORD") },
		func(g *gui) bool { return !g.form.open && g.entry == passwordRef }},
	{"a namespace", func(h *harness) { h.clickWithoutSettling("db") },
		func(g *gui) bool { return !g.form.open && g.namespace == "db" }},
	{"New Entry", func(h *harness) { h.clickWithoutSettling("New Entry") },
		func(g *gui) bool { return g.form.open && g.form.from == (store.Ref{}) && g.form.value == "" }},
	{"Edit on another row", func(h *harness) { h.chooseWithoutSettling("PASSWORD", "Edit") },
		func(g *gui) bool { return g.form.open && g.form.from == passwordRef && g.entry == passwordRef }},
}

// A form without changes is left at once; one with changes asks first, and
// goes on as answered: Continue Editing, or Escape, keeps the form and the
// list as they were; Don't Save drops the draft; Save writes it. Either of
// the last goes where the user went, not to the entry saved.
func TestLeavingForm(t *testing.T) {
	const asked = "Save changes to this entry?"
	for _, l := range leavings {
		t.Run(l.name, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			l.act(h)
			h.settle()
			if h.g.confirmLeave || len(h.openAlerts()) != 0 || !l.happened(h.g) {
				t.Fatalf("leaving an unchanged form: asked %v, alerts %d", h.g.confirmLeave, len(h.openAlerts()))
			}

			h = newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-draft")
			row := h.g.row
			for _, stay := range []func(){
				func() { h.answer("Continue Editing") },
				h.escape,
			} {
				l.act(h)
				h.settle()
				if a := h.alert(); a.message != asked || a.detail != "" || !slices.Equal(a.buttons, []alertButton{
					{title: "Save", isDefault: true}, {title: "Continue Editing", isCancel: true}, {title: "Don’t Save", destructive: true},
				}) {
					t.Fatalf("leaving a changed form asks %+v", a.alert)
				}
				stay()
				if f := h.g.form; h.g.confirmLeave || !f.open || f.from != urlRef || f.value != "synthetic-draft" || h.g.entry != urlRef || h.g.row != row || h.g.namespace != allNamespaces {
					t.Fatalf("staying: asking %v, form open %v for %s, entry %s, row %d", h.g.confirmLeave, f.open, f.from, h.g.entry, h.g.row)
				}
			}
			l.act(h)
			h.settle()
			h.answer("Don’t Save")
			if !l.happened(h.g) || h.g.form.value == "synthetic-draft" {
				t.Fatalf("Don’t Save: entry %s, form open %v for %s", h.g.entry, h.g.form.open, h.g.form.from)
			}
			if v, _ := h.value("app/URL"); v != urlValue {
				t.Fatalf("Don’t Save stored %q", v)
			}

			h = newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-saved")
			l.act(h)
			h.settle()
			h.enter() // Save
			if v, _ := h.value("app/URL"); v != "synthetic-saved" || !l.happened(h.g) {
				t.Fatalf("Save stored %q; entry %s, form open %v for %s", v, h.g.entry, h.g.form.open, h.g.form.from)
			}
		})
	}
}

// Asking about a new entry offers to add it when it can be added, and only
// to discard it otherwise. The list shows no entry meanwhile, so that the
// one chosen before leaves the form too.
func TestLeavingNewEntry(t *testing.T) {
	h := newHarness(t)
	h.newInApp("NEW_KEY")
	h.click("API_TOKEN")
	if a := h.alert(); a.message != "Discard the new entry?" || !slices.Equal(a.buttons, []alertButton{
		{title: "Continue Editing", isDefault: true, isCancel: true}, {title: "Discard", destructive: true},
	}) {
		t.Fatalf("leaving a new entry without a value asks %+v", a.alert)
	}
	h.answer("Discard")
	chosen := store.Ref{Namespace: "app", Name: "API_TOKEN"}
	if h.g.form.open || h.g.entry != chosen {
		t.Fatalf("Discard: form open %v, entry %s", h.g.form.open, h.g.entry)
	}

	h.click("New Entry")
	h.typeName("NEW_KEY")
	h.typeValue("synthetic-new-31d2")
	if h.g.row != -1 {
		t.Fatalf("the list shows row %d beside a new entry", h.g.row)
	}
	h.click("API_TOKEN")
	if a := h.alert(); a.message != "Add the new entry?" || !slices.Equal(a.titles(), []string{"Add", "Continue Editing", "Discard"}) {
		t.Fatalf("leaving a complete new entry asks %+v", a.alert)
	}
	h.enter()
	if v, _ := h.value("app/NEW_KEY"); v != "synthetic-new-31d2" || h.g.form.open || h.g.entry != chosen {
		t.Fatalf("Add stored %q; form open %v, entry %s", v, h.g.form.open, h.g.entry)
	}
}

// A save the prompt starts that fails keeps the form, with its error, and
// goes nowhere.
func TestLeavingSaveFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(h *harness)
	}{
		{"backend error", func(h *harness) {
			h.typeValue("synthetic-url-new")
			h.backend.Before = func(op, _ string) error {
				if op == "edit" {
					return &store.Error{Kind: store.KindLocked, Message: "synthetic locked"}
				}
				return nil
			}
		}},
		{"entry in the way", func(h *harness) { h.typeName("API_TOKEN") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			tc.change(h)
			h.click("PASSWORD")
			h.enter()
			if f := h.g.form; !f.open || f.from != urlRef || f.err.message == "" || !h.tt.HasText(f.err.message) {
				t.Fatalf("form open %v, error %+v, texts %q", f.open, f.err, h.tt.Texts())
			}
			if h.g.entry != urlRef || h.g.confirmLeave || len(h.openAlerts()) != 0 {
				t.Fatalf("entry %s, asking %v, alerts %d", h.g.entry, h.g.confirmLeave, len(h.openAlerts()))
			}
		})
	}
}

// Leaving a form whose save runs asks nothing, as Close does; the save
// still lands, without taking the user back from where they went.
func TestLeavingDuringSave(t *testing.T) {
	for _, l := range leavings[:2] {
		t.Run(l.name, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			started, release := h.pause("edit")
			h.clickWithoutSettling("Save")
			h.waitFor(started)
			l.act(h)
			h.tt.Frame()
			if h.g.confirmLeave || !l.happened(h.g) {
				t.Fatalf("asking %v, form open %v, entry %s", h.g.confirmLeave, h.g.form.open, h.g.entry)
			}
			release()
			h.settle()
			if v, _ := h.value("app/URL"); v != "synthetic-url-new" || !l.happened(h.g) {
				t.Fatalf("stored %q; after the save landed: entry %s, namespace %q", v, h.g.entry, h.g.namespace)
			}
			if h.g.namespace == allNamespaces && !h.tt.HasText("URL") {
				t.Fatalf("the list lacks the saved entry: %q", h.tt.Texts())
			}
		})
	}
}

// Choosing again, while a new entry is added, the entry chosen before it is
// a choice like any other, which the added entry does not override.
func TestChoosingBackDuringAdd(t *testing.T) {
	h := newHarness(t)
	h.click("app")
	h.click("URL")
	h.click("New Entry")
	h.typeName("NEW_KEY")
	h.typeValue("synthetic-new-31d2")
	started, release := h.pause("add")
	h.clickWithoutSettling("Add")
	h.waitFor(started)
	h.clickWithoutSettling("URL")
	h.tt.Frame()
	release()
	h.settle()
	if v, _ := h.value("app/NEW_KEY"); v != "synthetic-new-31d2" || h.g.form.open || h.g.entry != urlRef || !h.tt.HasText("NEW_KEY") {
		t.Fatalf("added %q; form open %v, entry %s, texts %q", v, h.g.form.open, h.g.entry, h.tt.Texts())
	}
}

// A search typed while a save runs is a choice too, which the saved entry
// does not end.
func TestSearchDuringSave(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-url-new")
	started, release := h.pause("edit")
	h.clickWithoutSettling("Save")
	h.waitFor(started)
	h.clickWithoutSettling("Search entries")
	h.tt.Type("pass")
	h.tt.Frame()
	release()
	h.settle()
	if v, _ := h.value("app/URL"); v != "synthetic-url-new" || h.g.query != "pass" {
		t.Fatalf("stored %q; query %q", v, h.g.query)
	}
}

// Opening another form while a save runs is a choice: the save lands
// without choosing its entry or doing what the prompt's Save deferred,
// which would replace that form.
func TestNewEntryDuringSave(t *testing.T) {
	for _, from := range []string{"the form", "the prompt"} {
		t.Run(from, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			if from == "the prompt" {
				h.click("PASSWORD")
			}
			started, release := h.pause("edit")
			if from == "the prompt" {
				h.enter()
			} else {
				h.clickWithoutSettling("Save")
			}
			h.waitFor(started)
			h.clickWithoutSettling("New Entry")
			h.tt.Frame()
			release()
			h.settle()
			if v, _ := h.value("app/URL"); v != "synthetic-url-new" || !h.g.form.open || h.g.form.from != (store.Ref{}) || h.g.entry != (store.Ref{}) {
				t.Fatalf("stored %q; form open %v for %s, entry %s", v, h.g.form.open, h.g.form.from, h.g.entry)
			}
		})
	}
}

// Closing the form while the prompt's save runs is not a choice: once the
// save lands, the list goes where the user chose to go.
func TestClosingDuringPromptSave(t *testing.T) {
	for _, closing := range []string{"Close", "Escape"} {
		t.Run(closing, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			h.click("PASSWORD")
			started, release := h.pause("edit")
			h.enter()
			h.waitFor(started)
			if closing == "Close" {
				h.clickWithoutSettling("Close")
			} else {
				h.tt.Key(0, ui.KeyEscape)
			}
			h.tt.Frame()
			if h.g.form.open {
				t.Fatalf("%s left the form open", closing)
			}
			release()
			h.settle()
			if v, _ := h.value("app/URL"); v != "synthetic-url-new" || h.g.form.open || h.g.entry != passwordRef {
				t.Fatalf("stored %q; form open %v, entry %s", v, h.g.form.open, h.g.entry)
			}
		})
	}
}

// While another write runs, the prompt about a complete form offers only to
// discard it, says why, and keeps its buttons when that write ends.
func TestLeavingWhileAnotherWriteRuns(t *testing.T) {
	const why = "Another change is still in progress."
	h := newHarness(t)
	h.choose("API_TOKEN", "Delete…")
	started, release := h.pause("delete")
	h.answer("Delete")
	h.waitFor(started)
	h.clickWithoutSettling("New Entry")
	h.tt.Frame()
	h.typeName("NEW_KEY")
	h.typeNamespace("app")
	h.typeValue("synthetic-new-31d2")
	h.click("PASSWORD")
	a := h.alert()
	if a.detail != why || !slices.Equal(a.titles(), []string{"Continue Editing", "Discard"}) {
		t.Fatalf("while the delete runs, the prompt asks %+v", a.alert)
	}
	release()
	h.settle()
	// Closing the window or quitting asks while the prompt is open.
	left := false
	if !h.g.ask(func() { left = true }, true) {
		t.Fatal("leaving again while the prompt is open went ahead")
	}
	h.settle()
	if open := h.openAlerts(); len(open) != 1 || open[0] != a {
		t.Fatalf("asking again opened %d alerts", len(open))
	}
	h.enter() // Continue Editing
	if _, added := h.value("app/NEW_KEY"); added || left || !h.g.form.open || h.g.form.value != "synthetic-new-31d2" || h.g.confirmLeave {
		t.Fatalf("Enter: added %v, left %v, form open %v, asking %v", added, left, h.g.form.open, h.g.confirmLeave)
	}
}

// The prompt's Save before the window closes, or the app quits, starts
// nothing once the window has closed; quitting then quits the app, which
// another window, such as the updater's, would otherwise keep running.
func TestSaveBeforeClosing(t *testing.T) {
	for _, leaving := range []string{"closing the window", "quitting"} {
		t.Run(leaving, func(t *testing.T) {
			h := newHarness(t)
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			var mu sync.Mutex
			var ops []string
			h.backend.Before = func(op, _ string) error {
				mu.Lock()
				defer mu.Unlock()
				ops = append(ops, op)
				return nil
			}
			// close stands in for the window's Close, whose Closed handler it is.
			quitAfterClosing := false
			h.g.closeWindow, h.g.quitApp = h.g.close, func() { quitAfterClosing = h.g.closed }
			then := h.g.closeWindow
			if leaving == "quitting" {
				then = h.g.quit
			}
			if !h.g.ask(then, true) {
				t.Fatal("leaving a changed form asked nothing")
			}
			h.settle()
			h.enter() // Save
			mu.Lock()
			started := slices.Clone(ops)
			mu.Unlock()
			if v, _ := h.value("app/URL"); v != "synthetic-url-new" || !h.g.closed || !slices.Equal(started, []string{"edit"}) {
				t.Fatalf("stored %q; closed %v; backend operations %q", v, h.g.closed, started)
			}
			if leaving == "quitting" && !quitAfterClosing {
				t.Fatal("the app did not quit after the window closed")
			}
		})
	}
}

// Cancel and Escape, from the field being typed in, close a form without
// changes at once, and ask before discarding changes, offering no Save even
// when the form could save: Cancel is not a way to save.
func TestFormDraftDismissal(t *testing.T) {
	for _, opening := range []string{"New Entry", "Edit"} {
		for _, dismiss := range []string{"Cancel", "Escape"} {
			t.Run(opening+"/"+dismiss, func(t *testing.T) {
				h := newHarness(t)
				open := func() {
					if opening == "Edit" {
						h.choose("URL", opening)
					} else {
						h.click(opening)
					}
				}
				leave := func() {
					if dismiss == "Cancel" {
						h.click("Cancel")
					} else {
						h.tt.Key(0, ui.KeyEscape)
						h.settle()
					}
				}
				open()
				leave()
				if h.g.form.open || h.g.confirmLeave || len(h.openAlerts()) != 0 {
					t.Fatalf("an unchanged form: open %v, asking %v, alerts %d", h.g.form.open, h.g.confirmLeave, len(h.openAlerts()))
				}

				open()
				h.typeValue("synthetic-draft")
				asked := "Discard the new entry?"
				if opening == "Edit" {
					asked = "Discard changes to this entry?"
				}
				for _, stay := range []func(){func() { h.answer("Continue Editing") }, h.escape} {
					leave()
					if a := h.alert(); a.message != asked || !slices.Equal(a.titles(), []string{"Continue Editing", "Discard"}) {
						t.Fatalf("a changed form asks %+v", a.alert)
					}
					stay()
					if !h.g.form.open || h.g.form.value != "synthetic-draft" || h.g.confirmLeave {
						t.Fatalf("staying: form open %v, draft kept %v, asking %v", h.g.form.open, h.g.form.value == "synthetic-draft", h.g.confirmLeave)
					}
				}
				leave()
				h.answer("Discard")
				if h.g.form.open || h.g.form.value+h.g.form.original != "" || h.g.confirmLeave {
					t.Fatalf("form open %v, asking %v, or the value stayed", h.g.form.open, h.g.confirmLeave)
				}
				if v, _ := h.value("app/URL"); v != urlValue {
					t.Fatalf("discarding stored %q", v)
				}
			})
		}
	}
}

// The form's entry stays chosen even while a search hides it; clicking or
// editing that entry is not leaving the form, and no value shows behind the
// form.
func TestFormsOwnEntry(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-draft")
	h.click("URL")
	h.tt.Key(0, ui.KeySpace)
	h.settle()
	if len(h.backend.Reads) != 1 || h.g.revealed != "" {
		t.Fatalf("Space behind the form read %q", h.backend.Reads)
	}
	h.typeInto("Search entries", "pass")
	if h.g.entry != urlRef {
		t.Fatalf("a search hiding the form's entry chose %s", h.g.entry)
	}
	h.replaceText("Search entries", "")
	if h.g.entry != urlRef || h.g.row != slices.Index(h.g.rows(), urlRef) {
		t.Fatalf("after the search, entry %s, row %d", h.g.entry, h.g.row)
	}
	h.click("URL")
	h.choose("URL", "Edit")
	if f := h.g.form; h.g.confirmLeave || !f.open || f.value != "synthetic-draft" {
		t.Fatalf("asking %v, form open %v with %q", h.g.confirmLeave, f.open, f.value)
	}
}

// Copying or deleting another entry from its row's menu leaves the form,
// and the entry chosen, as they are, so the list's keys still act on the
// form's entry. Deleting another entry names it, as the window doesn't
// show it.
func TestRowMenuBesideForm(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-draft")
	row := h.g.row
	h.choose("PASSWORD", "Copy Value")
	if got := h.tt.Clipboard(); got != "synthetic-db-0a7f" {
		t.Fatalf("Copy Value copied %q", got)
	}
	// The card of the form's entry, back once the form closes, did not copy.
	if f := h.g.form; h.g.confirmLeave || !f.open || f.from != urlRef || f.value != "synthetic-draft" || h.g.entry != urlRef || h.g.row != row || h.g.copied {
		t.Fatalf("asking %v, form open %v for %s, entry %s, row %d, copied %v", h.g.confirmLeave, f.open, f.from, h.g.entry, h.g.row, h.g.copied)
	}
	h.focusList()
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.settle()
	if got := h.tt.Clipboard(); got != urlValue {
		t.Fatalf("Cmd-C beside the form copied %q", got)
	}
	h.tt.Key(ui.Cmd, ui.KeyBackspace)
	h.settle()
	if a := h.alert(); a.message != "Delete this entry?" {
		t.Fatalf("Cmd-Backspace beside the form asks %q", a.message)
	}
	h.escape()
	h.choose("PASSWORD", "Delete…")
	if f := h.g.form; h.alert().message != "Delete “PASSWORD” from db?" || !f.open || f.value != "synthetic-draft" || h.g.entry != urlRef {
		t.Fatalf("Delete…: asking %q, form open %v, entry %s", h.alert().message, f.open, h.g.entry)
	}
}

// A new entry's form chooses no entry, so the list's keys act on none, and
// closing it chooses again the entry chosen before.
func TestNewEntryChoosesNone(t *testing.T) {
	h := newHarness(t)
	h.click("URL")
	h.click("New Entry")
	h.focusList()
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.tt.Key(ui.Cmd, ui.KeyBackspace)
	h.settle()
	if h.g.entry != (store.Ref{}) || h.g.row != -1 || len(h.backend.Reads) != 0 || len(h.openAlerts()) != 0 {
		t.Fatalf("beside a new entry: entry %s, row %d, reads %q, alerts %d", h.g.entry, h.g.row, h.backend.Reads, len(h.openAlerts()))
	}
	h.click("Cancel")
	if h.g.form.open || h.g.entry != urlRef || h.g.row != slices.Index(h.g.rows(), urlRef) {
		t.Fatalf("Cancel: form open %v, entry %s, row %d", h.g.form.open, h.g.entry, h.g.row)
	}
}

// Closing a new entry's form chooses again the entry chosen before only
// while it exists: here the last one, deleted beside the form, which leaves
// no list to unchoose it.
func TestNewEntryAfterDeletingTheChosen(t *testing.T) {
	backend := storetest.New()
	backend.Put("app/URL", []byte(urlValue))
	h := newHarnessWith(t, backend)
	h.click("URL")
	h.click("New Entry")
	h.choose("URL", "Delete…")
	h.answer("Delete")
	h.click("Cancel")
	if _, kept := h.value("app/URL"); kept || h.g.form.open || h.g.entry != (store.Ref{}) || h.tt.HasText("URL") {
		t.Fatalf("kept %v; form open %v, entry %s, texts %q", kept, h.g.form.open, h.g.entry, h.tt.Texts())
	}
}

// focusList clicks the entries list below its last row.
func (h *harness) focusList() {
	h.t.Helper()
	r := h.find("Entries")
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H-4)
	h.settle()
	if !h.tt.Focused("Entries") {
		h.t.Fatal("the list did not take the focus")
	}
}

func TestFormDirty(t *testing.T) {
	inferred := ""
	edit := func(change func(f *form)) form {
		f := form{open: true, from: urlRef, namespace: "app", name: "URL", value: urlValue, original: urlValue,
			kind: "URL", kindFrom: "URL"}
		change(&f)
		return f
	}
	for _, tc := range []struct {
		name  string
		form  form
		dirty bool
	}{
		{"edit unchanged", edit(func(*form) {}), false},
		{"edit loading", edit(func(f *form) { f.value, f.original, f.loading = "", "", true }), false},
		{"edit keeping its value", edit(func(f *form) { f.value, f.original, f.keepValue = "", "", true }), false},
		{"edit renamed", edit(func(f *form) { f.name = "ENDPOINT" }), true},
		{"edit moved", edit(func(f *form) { f.namespace = "db" }), true},
		{"edit value", edit(func(f *form) { f.value = "synthetic-url-new" }), true},
		{"edit kind", edit(func(f *form) { f.kind = "Other" }), true},
		{"edit notes", edit(func(f *form) { f.notes = "Rotate" }), true},
		{"new, as opened", form{open: true, namespace: "app", kind: "API Key", inferredFrom: &inferred}, false},
		{"new, kind picked", form{open: true, namespace: "app", kind: "Other"}, false},
		{"new name", form{open: true, name: "NEW_KEY"}, true},
		{"new value", form{open: true, value: "synthetic-new-31d2"}, true},
		{"new notes", form{open: true, notes: "Rotate"}, true},
	} {
		if got := tc.form.dirty(); got != tc.dirty {
			t.Errorf("%s: dirty %v", tc.name, got)
		}
	}
}
