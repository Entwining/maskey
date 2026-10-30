package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

func TestAddEntry(t *testing.T) {
	h := newHarness(t)
	h.click("New Entry")
	h.replaceText("Namespace", "svc")
	h.typeName("NEW_KEY")
	h.typeValue("synthetic-new-31d2")
	h.click("Add")
	if v, ok := h.value("svc/NEW_KEY"); !ok || v != "synthetic-new-31d2" {
		t.Fatalf("stored %q, %v", v, ok)
	}
	if h.g.form.open || h.g.form.value != "" {
		t.Fatalf("the form stayed open (%v) or kept its value", h.g.form.open)
	}
	if h.g.entry != (store.Ref{Namespace: "svc", Name: "NEW_KEY"}) {
		t.Fatalf("selection %s after creating svc/NEW_KEY", h.g.entry)
	}
}

// A create the name check refuses, or one the store refuses because the
// entry exists, keeps the form and its draft, with the error on the name.
func TestAddFailureKeepsForm(t *testing.T) {
	for _, tc := range []struct{ name, stored string }{
		{name: "1BAD"},
		{name: "FROM_CLI", stored: tokenValue}, // created with mk meanwhile
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.stored != "" {
				h.backend.Put("app/"+tc.name, []byte(tc.stored))
			}
			h.newInApp(tc.name)
			h.typeValue("synthetic-other-77aa")
			h.click("Add")
			if f := h.g.form; !f.open || f.name != tc.name || f.value != "synthetic-other-77aa" {
				t.Fatalf("form after a failed create: open %v, name %q", f.open, f.name)
			}
			if h.g.form.err.field != "name" || !h.tt.HasText(h.g.form.err.message) {
				t.Fatalf("error %+v, texts %q", h.g.form.err, h.tt.Texts())
			}
			if v, ok := h.value("app/" + tc.name); v != tc.stored || ok != (tc.stored != "") {
				t.Fatalf("the failed create stored %q", v)
			}
			// The list shows an entry that blocked the create.
			if tc.stored != "" && !slices.Contains(h.g.rows(), store.Ref{Namespace: "app", Name: tc.name}) {
				t.Fatalf("entries %q after the failed create", h.g.rows())
			}
			message := h.g.form.err.message
			h.typeName("OTHER_NAME")
			h.tt.Frame()
			if h.tt.HasText(message) {
				t.Fatalf("the error %q outlived the name it was about", message)
			}
		})
	}
}

// Closing the form returns the keyboard focus to the list, which chooses the
// row focused before, URL, while the names load; NEW_KEY then sorts into the
// row URL had. The list must not take that for URL moving.
func TestAddEntryChosenOverTheListChoice(t *testing.T) {
	h := newHarness(t)
	loading, release := h.addWhileNamesLoad("NEW_KEY")
	h.waitFor(loading)
	h.clickWithoutSettling("URL")
	release()
	h.settle()
	created := store.Ref{Namespace: "app", Name: "NEW_KEY"}
	if h.g.entry != created || h.g.row != slices.Index(h.g.rows(), created) {
		t.Fatalf("selection %s, row %d, after creating app/NEW_KEY", h.g.entry, h.g.row)
	}
}

// Edit starts from the stored value and saves only a change.
func TestEditValue(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	if h.g.form.value != urlValue {
		t.Fatalf("the editor starts from %q", h.g.form.value)
	}
	var writes []string
	h.backend.Before = func(op, _ string) error {
		if op == "edit" {
			writes = append(writes, op)
		}
		return nil
	}
	h.click("Save")
	if len(writes) != 0 || !h.g.form.open {
		t.Fatalf("saving an unchanged entry wrote %q; editor open %v", writes, h.g.form.open)
	}
	h.typeValue("synthetic-url-new")
	h.click("Save")
	if v, _ := h.value("app/URL"); v != "synthetic-url-new" || !slices.Equal(writes, []string{"edit"}) {
		t.Fatalf("stored %q after writes %q", v, writes)
	}
	if h.g.form.open || h.g.form.value != "" || h.g.form.original != "" {
		t.Fatal("the editor stayed open or kept the value")
	}
}

// Edit renames an entry and moves it to another namespace in one write,
// keeping its value, and the list follows it; an entry in the way keeps the
// form, with the error on the name.
func TestEditMovesEntry(t *testing.T) {
	h := newHarness(t)
	h.click("app")
	h.choose("URL", "Edit")
	h.typeName("PASSWORD")
	h.typeNamespace("db")
	h.click("Save")
	if !h.g.form.open || h.g.form.err.field != "name" || !h.tt.HasText(h.g.form.err.message) {
		t.Fatalf("moving onto db/PASSWORD: form open %v, error %+v", h.g.form.open, h.g.form.err)
	}
	if v, _ := h.value("db/PASSWORD"); v != "synthetic-db-0a7f" {
		t.Fatalf("the entry in the way holds %q", v)
	}
	var writes []string
	h.backend.Before = func(op, _ string) error {
		writes = append(writes, op)
		return nil
	}
	h.typeName("ENDPOINT")
	h.click("Save")
	if len(writes) == 0 || writes[0] != "edit" || slices.Contains(writes, "read") {
		t.Fatalf("saving the move ran %q", writes)
	}
	if v, _ := h.value("db/ENDPOINT"); v != urlValue {
		t.Fatalf("db/ENDPOINT holds %q", v)
	}
	if _, ok := h.value("app/URL"); ok {
		t.Fatal("app/URL remains")
	}
	if h.g.form.open || h.g.namespace != "db" || h.g.entry != (store.Ref{Namespace: "db", Name: "ENDPOINT"}) {
		t.Fatalf("after the move: form open %v, namespace %q, entry %s", h.g.form.open, h.g.namespace, h.g.entry)
	}
}

// Notes are added with an entry and shown with it; a change to them alone
// is enough to save, and keeps the value.
func TestNotes(t *testing.T) {
	h := newHarness(t)
	h.newInApp("NEW_KEY")
	h.typeValue("synthetic-new-31d2")
	h.typeInto("Notes", "Rotate in May")
	h.click("Add")
	if !h.tt.HasText("Rotate in May") {
		t.Fatalf("texts after adding %q", h.tt.Texts())
	}
	h.choose("NEW_KEY", "Edit")
	var writes []string
	h.backend.Before = func(op, _ string) error {
		writes = append(writes, op)
		return nil
	}
	h.replaceText("Notes", "Rotated in May")
	h.click("Save")
	if len(writes) == 0 || writes[0] != "edit" || !h.tt.HasText("Rotated in May") {
		t.Fatalf("saving the notes ran %q; texts %q", writes, h.tt.Texts())
	}
	if v, _ := h.value("app/NEW_KEY"); v != "synthetic-new-31d2" {
		t.Fatalf("the value became %q", v)
	}
}

// A new entry's kind follows its name until the user picks one; the kind
// is stored with the entry and shown with it, and a change to it alone is
// enough to save.
func TestKind(t *testing.T) {
	h := newHarness(t)
	h.newInApp("GITHUB_TOKEN")
	if h.g.form.kind != "API Key" {
		t.Fatalf("kind %q for GITHUB_TOKEN", h.g.form.kind)
	}
	h.typeName("DB_PASSWORD")
	if h.g.form.kind != "Password" {
		t.Fatalf("kind %q after renaming to DB_PASSWORD", h.g.form.kind)
	}
	h.click("Password")
	h.click("Identifier")
	h.typeName("DEPLOY_URL")
	if h.g.form.kind != "Identifier" {
		t.Fatalf("the picked kind became %q when the name changed", h.g.form.kind)
	}
	h.typeValue("synthetic-new-31d2")
	h.click("Add")
	if got := h.attributes("app/DEPLOY_URL").Kind; got != store.ValueIdentifier {
		t.Fatalf("stored kind %q", got)
	}
	if !h.tt.HasText("Identifier") {
		t.Fatalf("texts after adding %q", h.tt.Texts())
	}
	h.choose("DEPLOY_URL", "Edit")
	h.click("Identifier")
	h.click("Other")
	h.click("Save")
	if got := h.attributes("app/DEPLOY_URL").Kind; got != store.ValueSetting {
		t.Fatalf("kind %q after the edit", got)
	}
	if v, _ := h.value("app/DEPLOY_URL"); v != "synthetic-new-31d2" {
		t.Fatalf("the value became %q", v)
	}
}

// The text area would turn CR into LF, so a value holding CR is not
// offered for editing; the entry can still be renamed, keeping it.
func TestEditKeepsCarriageReturns(t *testing.T) {
	const value = "synthetic-first\r\nsynthetic-second"
	h := newHarness(t)
	h.backend.Put("app/URL", []byte(value))
	h.choose("URL", "Edit")
	if h.g.form.value != "" || !h.g.form.keepValue || !h.tt.HasText("carriage returns") {
		t.Fatalf("editor value %q, texts %q", h.g.form.value, h.tt.Texts())
	}
	h.typeName("ENDPOINT")
	h.click("Save")
	if v, _ := h.value("app/ENDPOINT"); v != value {
		t.Fatalf("the renamed entry holds %q", v)
	}
}

func TestEditDeletedEntryKeepsForm(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-url-new")
	h.backend.Delete(context.Background(), "app/URL") // deleted with mk meanwhile
	h.click("Save")
	if f := h.g.form; !f.open || f.value != "synthetic-url-new" || f.err.message == "" || !h.tt.HasText(f.err.message) {
		t.Fatalf("editor open %v, texts %q", f.open, h.tt.Texts())
	}
	if _, ok := h.value("app/URL"); ok {
		t.Fatal("saving recreated the deleted entry")
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Delete…")
	h.answer("Cancel")
	if _, ok := h.value("app/URL"); !ok {
		t.Fatal("canceling deleted the entry")
	}
	h.choose("URL", "Delete…")
	started, release := h.pause("delete")
	h.answer("Delete")
	h.waitFor(started)
	h.tt.Frame()
	// The delete's progress shows in the entry's row.
	row, _ := h.tt.Find("URL")
	if spinner, ok := h.tt.Find("Delete in progress"); !ok || spinner.Y > row.Y+row.H || spinner.Y+spinner.H < row.Y {
		t.Fatalf("the spinner is not on the row: row %v, spinner %v, present %v", row, spinner, ok)
	}
	release()
	h.settle()
	if _, ok := h.value("app/URL"); ok {
		t.Fatal("the entry still exists")
	}
	if _, ok := h.value("app/API_TOKEN"); !ok {
		t.Fatal("another entry was deleted")
	}
	if h.g.entry != (store.Ref{}) || slices.Contains(h.tt.Texts(), "URL") {
		t.Fatalf("the deleted entry still shows: %q", h.tt.Texts())
	}
}

// The form's Delete asks first, with no button for Return, then deletes the
// entry edited and closes the form.
func TestDeleteFromForm(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.click("Delete")
	if a := h.alert(); a.message != "Delete this entry?" || a.detail != "You can’t undo this action." || !slices.Equal(a.buttons, []alertButton{
		{title: "Delete", destructive: true}, {title: "Cancel", isCancel: true},
	}) {
		t.Fatalf("Delete asks %+v", a.alert)
	}
	h.answer("Delete")
	if _, ok := h.value("app/URL"); ok || h.g.form.open {
		t.Fatalf("after confirming: entry present %v, form open %v", ok, h.g.form.open)
	}
}

// Deleting a namespace's last entry deletes the namespace; the window goes
// on to every entry.
func TestDeletingLastEntryLeavesNamespace(t *testing.T) {
	h := newHarness(t)
	h.click("db")
	h.choose("PASSWORD", "Delete…")
	h.answer("Delete")
	if slices.Contains(h.tt.Texts(), "db") || h.g.namespace != allNamespaces || !h.tt.HasText("API_TOKEN") {
		t.Fatalf("namespace %q, texts %q", h.g.namespace, h.tt.Texts())
	}
}

// A write's outcome shows even when another entry was chosen meanwhile.
func TestDeleteOutcomeShownAfterSelectionChanges(t *testing.T) {
	h := newHarness(t)
	_, release := h.pause("delete")
	held := h.backend.Before
	h.backend.Before = func(op, account string) error {
		if err := held(op, account); err != nil || op != "delete" {
			return err
		}
		return &store.Error{Kind: store.KindUncertain, Message: "synthetic uncertain delete"}
	}
	h.choose("URL", "Delete…")
	h.answer("Delete")
	h.click("API_TOKEN")
	release()
	h.settle()
	if a := h.alert(); a.message != "Couldn’t Confirm That “URL” Was Deleted" || a.detail != "Synthetic uncertain delete." {
		t.Fatalf("the uncertain outcome was not shown: %+v", a.alert)
	}
}

// A form opened while the names of an entry just created load keeps its
// own entry chosen, none for a new one, when they arrive.
func TestEditKeepsItsEntry(t *testing.T) {
	h := newHarness(t)
	_, release := h.addWhileNamesLoad("NEW_KEY")
	h.chooseWithoutSettling("URL", "Edit")
	release()
	h.settle()
	if h.g.entry != urlRef {
		t.Fatalf("beside the form for URL, %s is chosen", h.g.entry)
	}
	h.typeValue("synthetic-url-new")
	h.click("Save")
	if v, _ := h.value("app/URL"); v != "synthetic-url-new" {
		t.Fatalf("app/URL holds %q", v)
	}

	h = newHarness(t)
	_, release = h.addWhileNamesLoad("NEW_KEY")
	h.clickWithoutSettling("New Entry")
	release()
	h.settle()
	if !h.g.form.open || h.g.form.from != (store.Ref{}) || h.g.entry != (store.Ref{}) {
		t.Fatalf("form open %v for %s, entry %s", h.g.form.open, h.g.form.from, h.g.entry)
	}
}

// Switching namespaces while a created entry's listing loads drops the
// choice of that entry, which must not happen on returning to its namespace.
func TestNamespaceSwitchDropsPendingChoice(t *testing.T) {
	h := newHarness(t)
	_, release := h.addWhileNamesLoad("NEW_KEY")
	h.clickWithoutSettling("db")
	release()
	h.settle()
	h.click("app")
	if h.g.namespace != "app" || h.g.entry != (store.Ref{}) {
		t.Fatalf("selection %s in %s after switching away and back", h.g.entry, h.g.namespace)
	}
}

func TestInstallCLI(t *testing.T) {
	home := t.TempDir()
	app := filepath.Join(t.TempDir(), "Maskey")
	os.WriteFile(app, nil, 0o755)
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(app, alias)

	link, err := installCLI(home, alias)
	if err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != mustEval(t, app) {
		t.Fatalf("mk links to %q, want the resolved executable %q", target, app)
	}

	// An earlier link, as to a moved app, is replaced.
	os.Remove(link)
	os.Symlink("/old/Maskey", link)
	if _, err := installCLI(home, app); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != mustEval(t, app) {
		t.Fatalf("mk still links to %q", target)
	}

	// A file of the user's is never replaced.
	os.Remove(link)
	os.WriteFile(link, []byte("user file"), 0o755)
	if _, err := installCLI(home, app); err == nil {
		t.Fatal("installCLI replaced a regular file")
	}
	if b, _ := os.ReadFile(link); string(b) != "user file" {
		t.Fatal("the user's file changed")
	}

	// Nor is one at the name of the temporary link.
	os.Remove(link)
	temporary := fmt.Sprintf("%s.maskey-new-%d", link, os.Getpid())
	os.WriteFile(temporary, []byte("user file"), 0o755)
	if _, err := installCLI(home, app); err == nil {
		t.Fatal("installCLI replaced a file at its temporary name")
	}
	if b, _ := os.ReadFile(temporary); string(b) != "user file" {
		t.Fatal("the file at the temporary name changed")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
