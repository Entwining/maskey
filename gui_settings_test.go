package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// fakeService stands in for the Maskey service's changes to stores.
type fakeService struct {
	added   []stores.Addition
	changes []string
	err     error
	remote  *remote
}

func (s *fakeService) AddStore(_ context.Context, addition stores.Addition) (stores.Connection, error) {
	s.added = append(s.added, addition)
	return stores.Connection{}, s.err
}

func (s *fakeService) ChangeStore(_ context.Context, op, id string) error {
	s.changes = append(s.changes, op+" "+id)
	if s.err == nil && op == agent.OpDefaultStore {
		s.remote.config.Default = id
	}
	return s.err
}

// openSettings shows the Settings window, whose alerts the harness answers.
func (h *harness) openSettings(r *remote) (*fakeService, *ui.Tester) {
	h.t.Helper()
	service := &fakeService{remote: r}
	h.g.service = service
	h.reload()
	window := ui.NewTester(h.g.settingsView, 560, 620)
	h.g.settings.redraw = window.Frame
	h.g.settings.ask = h.showMessage
	window.Frame()
	return service, window
}

func TestSettingsConnectsBitwarden(t *testing.T) {
	h := newHarness(t)
	service, window := h.openSettings(h.addRemote())
	click := func(label string) {
		t.Helper()
		if err := window.Click(label); err != nil {
			t.Fatal(err)
		}
		h.settle()
		window.Frame()
	}
	if window.HasText("Access Token") {
		t.Fatal("the form shows before it is asked for")
	}
	click("Add Bitwarden Store…")
	if add, ok := window.Access("Add"); !ok || !add.Disabled {
		t.Fatalf("Add without a token: %+v", add)
	}
	click("Name")
	window.Type("ci")
	click("European Union")
	click("Access Token")
	window.Type(" synthetic-token ")
	if window.HasText("synthetic-token") {
		t.Fatal("the access token shows as typed")
	}
	service.err = &store.Error{Kind: store.KindUnauthenticated, Message: "Bitwarden refused the access token"}
	click("Add")
	want := stores.Addition{ID: "ci", Type: stores.Bitwarden, Region: "eu", Token: []byte("synthetic-token")}
	if len(service.added) != 1 || service.added[0].ID != want.ID || service.added[0].Region != want.Region || string(service.added[0].Token) != string(want.Token) {
		t.Fatalf("added %+v", service.added)
	}
	// The token is typed again after a failure; the rest of the form stays.
	if h.g.settings.token != "" || h.g.settings.name != "ci" || !h.g.settings.adding || !window.HasText("Bitwarden refused the access token.") {
		t.Fatalf("after the failure: %+v, showing %q", h.g.settings, window.Texts())
	}
	service.err = nil
	click("Access Token")
	window.Type("synthetic-token")
	click("Add")
	if len(service.added) != 2 || h.g.settings.name != "" || h.g.settings.err != "" || h.g.settings.adding || window.HasText("Access Token") {
		t.Fatalf("after connecting: added %d, %+v", len(service.added), h.g.settings)
	}
}

func TestSettingsChangesStores(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	service, window := h.openSettings(r)
	if !window.HasText("Bitwarden Secrets Manager · EU · Deploy") {
		t.Fatalf("showing %q", window.Texts())
	}
	choose := func(id, item string) {
		t.Helper()
		if err := window.Click("Actions for " + id); err != nil {
			t.Fatal(err)
		}
		if err := window.ChooseMenuItem(item); err != nil {
			t.Fatalf("%s of %s: %v, in %q", item, id, err, window.Menu())
		}
		h.settle()
		window.Frame()
	}
	// The default iCloud Keychain has nothing to change.
	if _, ok := window.Access("Actions for icloud"); ok {
		t.Fatal("the default iCloud Keychain has actions")
	}
	// The Settings window draws the stores again once they are read.
	drawn := ""
	h.g.settings.redraw = func() { drawn = h.g.stores.Default; window.Frame() }
	choose("work", "Make Default")
	if drawn != "work" {
		t.Fatalf("Settings last drew the default %q", drawn)
	}
	choose("work", "Sync Now")
	// The default moved, so iCloud Keychain can become it again, and
	// nothing else.
	if err := window.Click("Actions for icloud"); err != nil || h.g.stores.Default != "work" {
		t.Fatalf("default %q, %v, showing %q", h.g.stores.Default, err, window.Texts())
	}
	if items := window.Menu(); !slices.Equal(items, []string{"Make Default"}) {
		t.Fatalf("iCloud Keychain's actions: %q", items)
	}
	window.CloseMenu()

	choose("work", "Remove…")
	if a := h.alert(); !slices.Equal(a.titles(), []string{"Remove", "Cancel"}) {
		t.Fatalf("alert %q", a.titles())
	}
	h.answer("Cancel")
	window.Frame()
	choose("work", "Remove…")
	service.err = errors.New("the Maskey service is not running")
	h.answer("Remove")
	window.Frame()
	want := []string{"store_default work", "store_sync work", "store_remove work"}
	if !slices.Equal(service.changes, want) || !window.HasText("The Maskey service is not running.") {
		t.Fatalf("changes %v, showing %q", service.changes, window.Texts())
	}
}
