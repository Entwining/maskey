package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// Delete… names the entry and its store, which two stores can share, and
// says who loses it: everyone using a Bitwarden project, or every Mac
// syncing the iCloud Keychain. Maskey undoes neither.
func TestDeleteNamesTheStore(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	for _, tc := range []struct {
		id              entryID
		message, detail string
	}{
		{work("app", "API_TOKEN"), "Delete “app/API_TOKEN” from “work”?", "It’s moved to Bitwarden’s trash and leaves the shared project for everyone who uses it. Maskey can’t restore it."},
		{icloud("app", "API_TOKEN"), "Delete “app/API_TOKEN” from “icloud”?", "It’s deleted from this Mac and the other Macs that sync this iCloud Keychain. You can’t undo this action."},
	} {
		h.chooseRow(tc.id, "Delete…")
		if a := h.alert(); a.message != tc.message || a.detail != tc.detail {
			t.Fatalf("Delete… of %v asks %q, %q", tc.id, a.message, a.detail)
		}
		h.escape()
	}
}

// The form's commands for Terminal read the value from a file, since a
// terminal echoes what is typed to mk set --stdin, and name the form's
// store, without which mk set would write to the default store. For a new
// entry they name it only as far as it is a valid reference the store is
// known not to hold: mk set replaces an entry where Add refuses. They lead
// to an entry of the kind the form shows, which mk set infers from a new
// entry's name. An error that names the store goes once the form chooses
// another.
func TestTerminalCommands(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	r.backend.Put("deploy/CRLF", []byte("synthetic-first\r\nsynthetic-second"))
	h.reload()
	h.chooseRow(work("deploy", "CRLF"), "Edit")
	if kept := "In Terminal, mk set --store work --stdin deploy/CRLF < <file> keeps the line endings of a value of several lines."; !slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.HasSuffix(s, kept) }) {
		t.Fatalf("the kept value shows %q", h.tt.Texts())
	}
	h.click("Cancel")
	h.clickScope(scope{"work", "deploy"})
	h.click("New Entry")
	paste := func() string {
		t.Helper()
		h.typeValue("synthetic-draft")
		h.tt.Command("selectAll")
		h.tt.SetClipboard("synthetic-first\r\nsynthetic-second")
		h.tt.Command("paste")
		h.tt.Frame()
		return h.g.form.err.on("value")
	}
	const crlf = "The pasted text has CRLF line endings, which this editor would change to LF. "
	lines := func(ref string) string {
		return "In Terminal, mk set --store work --stdin " + ref + " < <file> keeps the line endings of a value of several lines."
	}
	for _, tc := range []struct{ namespace, name, ref string }{
		{"deploy", "", "deploy/<NAME>"},
		{"deploy", "NEW_HOST", "deploy/NEW_HOST"},
		{"deploy", "API token", "deploy/<NAME>"},
		{"my deploy", "NEW_HOST", "<namespace>/NEW_HOST"},
		{"deploy", "SSH_HOST", "deploy/<NAME>"},
	} {
		h.typeNamespace(tc.namespace)
		h.typeName(tc.name)
		if got, want := paste(), crlf+lines(tc.ref); got != want {
			t.Fatalf("a CRLF paste for %s/%s in work: %q", tc.namespace, tc.name, got)
		}
	}
	// mk set would make deploy/SERVER_KEY an API Key and deploy/SERVER an
	// Other entry.
	h.typeNamespace("deploy")
	h.typeName("SERVER_KEY")
	h.click("API Key")
	h.click("SSH Key")
	if got, want := paste(), crlf+lines("deploy/<NAME>_SSH_KEY"); got != want {
		t.Fatalf("a CRLF paste for an SSH Key: %q", got)
	}
	h.typeName("SERVER")
	h.click("SSH Key")
	h.click("URL")
	if got, want := paste(), crlf+lines("deploy/SERVER")+" Then set its Kind to URL in this window."; got != want {
		t.Fatalf("a CRLF paste for a URL: %q", got)
	}
	choice, _ := h.tt.AccessByRole(ui.RolePopUpButton, "Store")
	h.tt.ClickAt(choice.Bounds.X+choice.Bounds.W/2, choice.Bounds.Y+choice.Bounds.H/2)
	h.tt.Key(0, ui.KeyUp)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if h.g.form.store != stores.ICloud || h.g.form.err != (formError{}) {
		t.Fatalf("after choosing icloud: store %q, error %+v", h.g.form.store, h.g.form.err)
	}
}

// Until a listing of the store has been read, a new entry's command names
// no entry it might replace: the store can hold any name.
func TestTerminalCommandBeforeTheListing(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	r.backend.Before = func(op, _ string) error {
		if op == "items" {
			return &store.Error{Kind: store.KindUnavailable, Message: "Bitwarden cannot be reached"}
		}
		return nil
	}
	h.reload()
	h.showStore("work")
	h.click("New Entry")
	paste := func(name string) string {
		t.Helper()
		h.typeNamespace("app")
		h.typeName(name)
		h.typeValue("synthetic-draft")
		h.tt.Command("selectAll")
		h.tt.SetClipboard("synthetic-first\r\nsynthetic-second")
		h.tt.Command("paste")
		return h.g.form.err.on("value")
	}
	for _, name := range []string{"API_TOKEN", "NEW_HOST"} {
		if got := paste(name); !strings.Contains(got, "--stdin app/<NAME> < <file>") {
			t.Fatalf("%s before work was listed: %q", name, got)
		}
	}
	r.backend.Before = nil
	h.reload()
	if got := paste("NEW_HOST"); !strings.Contains(got, "--stdin app/NEW_HOST < <file>") {
		t.Fatalf("NEW_HOST once work was listed: %q", got)
	}
}

// A new entry whose reference differs only in case from an entry of its
// store gets a note under Name, as mk set warns, and is still added; one
// in another store does not count.
func TestNewEntryBesideItsCaseTwin(t *testing.T) {
	h := newHarness(t)
	h.addRemote()
	h.reload()
	const note = "This creates a separate entry from app/API_TOKEN."
	h.clickScope(scope{"work", "deploy"})
	h.click("New Entry")
	h.typeNamespace("app")
	h.typeName("api_token")
	h.tt.Frame()
	if !h.tt.HasText(note) {
		t.Fatalf("beside work's app/API_TOKEN: showing %q", h.tt.Texts())
	}
	h.typeName("API_TOKEN_2")
	h.tt.Frame()
	if h.tt.HasText(note) {
		t.Fatal("the note stayed after the name changed")
	}
	h.typeName("api_token")
	h.typeValue("synthetic-twin-4e21")
	h.click("Add")
	if h.g.form.open || h.g.entry != work("app", "api_token") {
		t.Fatalf("after Add: form open %v, entry %v", h.g.form.open, h.g.entry)
	}
	// icloud holds app/API_TOKEN too, but no deploy/SSH_HOST.
	h.clickScope(scope{"icloud", "app"})
	h.click("New Entry")
	h.typeNamespace("deploy")
	h.typeName("ssh_host")
	h.tt.Frame()
	if slices.ContainsFunc(h.tt.Texts(), func(s string) bool { return strings.HasPrefix(s, "This creates a separate entry") }) {
		t.Fatalf("a twin in work noted for icloud: %q", h.tt.Texts())
	}
}
