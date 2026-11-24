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

// fakeService stands in for the Maskey service's changes to stores. A
// token replacement waits for hold to close, unless it is nil.
type fakeService struct {
	added   []stores.Addition
	tokens  []string
	targets []string
	changes []string
	removed []stores.Connection
	err     error
	remote  *remote
	hold    chan struct{}
}

func (s *fakeService) AddStore(_ context.Context, addition stores.Addition) (stores.Connection, error) {
	s.added = append(s.added, addition)
	return stores.Connection{}, s.err
}

func (s *fakeService) SetStoreToken(_ context.Context, id, target string, token []byte) error {
	if s.hold != nil {
		<-s.hold
	}
	s.tokens = append(s.tokens, id+" "+string(token))
	s.targets = append(s.targets, target)
	return s.err
}

func (s *fakeService) ChangeStore(_ context.Context, op, id string) error {
	s.changes = append(s.changes, op+" "+id)
	if s.err == nil && op == agent.OpDefaultStore {
		s.remote.config.Default = id
	}
	return s.err
}

func (s *fakeService) RemoveStore(_ context.Context, connection stores.Connection) error {
	s.changes = append(s.changes, agent.OpRemoveStore+" "+connection.ID)
	s.removed = append(s.removed, connection)
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
	for _, part := range []string{" synthetic-", "token "} {
		window.SetClipboard(part)
		window.Command("paste")
	}
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
	click("Access Token")
	window.Command("undo")
	if h.g.settings.token != "" {
		t.Fatal("Undo typed the cleared token again")
	}
	service.err = nil
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

// The answer to Remove… is for the connection the row showed, which the
// removal names, so that the service keeps a store connected again under
// the name while the question stood.
func TestRemoveAnswersForTheConnectionAsked(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	service, window := h.openSettings(r)
	asked, _ := h.g.stores.Find("work")
	if err := window.Click("Actions for work"); err != nil {
		t.Fatal(err)
	}
	if err := window.ChooseMenuItem("Remove…"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	r.config.Stores = []stores.Connection{{ID: "work", Type: stores.Bitwarden, Region: "eu", Project: "second", ProjectName: "Other"}}
	h.reload()
	h.answer("Remove")
	if len(service.removed) != 1 || service.removed[0].Target() != asked.Target() {
		t.Fatalf("removed %+v, asked about %+v", service.removed, asked)
	}
}

// The dialog that replaces an access token is for the connection it opened
// for: once the name means another connection, it closes with the token.
func TestReplaceTokenClosesForAnotherConnection(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	_, window := h.openSettings(r)
	if err := window.Click("Actions for work"); err != nil {
		t.Fatal(err)
	}
	if err := window.ChooseMenuItem("Replace Access Token…"); err != nil {
		t.Fatal(err)
	}
	window.Frame()
	if err := window.Click("New Access Token"); err != nil {
		t.Fatal(err)
	}
	window.Type("synthetic-token")
	// Reading the same connection again leaves the dialog open.
	h.reload()
	if !window.HasText("New Access Token") || h.g.settings.token == "" {
		t.Fatalf("after reading the same connection: showing %q", window.Texts())
	}
	r.config.Stores = []stores.Connection{{ID: "work", Type: stores.Bitwarden, Region: "eu", Project: "second", ProjectName: "Other"}}
	h.reload()
	if window.HasText("New Access Token") || h.g.settings.token != "" || h.g.settings.replacing.ID != "" {
		t.Fatalf("after work connected again: %+v, showing %q", h.g.settings, window.Texts())
	}
}

// A store's access token is replaced in its own dialog, as mk store token
// replaces it: the token is typed again after a failure, which the dialog
// shows, and quitting waits for the replacement to end.
func TestSettingsReplacesToken(t *testing.T) {
	h := newHarness(t)
	service, window := h.openSettings(h.addRemote())
	opened, _ := h.g.stores.Find("work")
	quit := h.exits()
	click := func(label string) {
		t.Helper()
		if err := window.Click(label); err != nil {
			t.Fatal(err)
		}
		window.Frame()
	}
	click("Actions for work")
	if err := window.ChooseMenuItem("Replace Access Token…"); err != nil {
		t.Fatalf("%v, in %q", err, window.Menu())
	}
	window.Frame()
	if replace, ok := window.Access("Replace"); !ok || !replace.Disabled || !window.HasText("Replace the Access Token of “work”") {
		t.Fatalf("Replace without a token: %+v, showing %q", replace, window.Texts())
	}
	// Pasted in two parts, the token is two steps of the field's history.
	click("New Access Token")
	for _, part := range []string{" synthetic-", "token "} {
		window.SetClipboard(part)
		window.Command("paste")
	}
	if field, _ := window.AccessByRole(ui.RoleTextField, "New Access Token"); !field.Password || window.HasText("synthetic-token") {
		t.Fatalf("the access token shows as typed: password %v", field.Password)
	}
	service.err = &store.Error{Kind: store.KindUnauthenticated, Message: "Bitwarden refused the access token"}
	service.hold = make(chan struct{})
	click("Replace")
	if h.g.settings.token != "" || h.g.settings.busy != "work" || !window.HasText("Replacing") || !h.g.beforeExit(quitExit) {
		t.Fatalf("while replacing: %+v, showing %q", h.g.settings, window.Texts())
	}
	close(service.hold)
	h.settle()
	window.Frame()
	if *quit || h.g.closed || h.g.settings.replacing.ID != "work" || !window.HasText("Bitwarden refused the access token.") {
		t.Fatalf("after the failure: quit %v, %+v, showing %q", *quit, h.g.settings, window.Texts())
	}
	// Undo types no cleared token again.
	click("New Access Token")
	window.Command("undo")
	window.Frame()
	if replace, _ := window.Access("Replace"); h.g.settings.token != "" || !replace.Disabled {
		t.Fatalf("after Undo: token typed again, Replace %+v", replace)
	}
	service.err, service.hold = nil, nil
	window.Type("synthetic-token")
	click("Replace")
	h.settle()
	window.Frame()
	if want := []string{"work synthetic-token", "work synthetic-token"}; !slices.Equal(service.tokens, want) ||
		h.g.settings.replacing.ID != "" || h.g.settings.err != "" || window.HasText("New Access Token") {
		t.Fatalf("after replacing: tokens %q, %+v, showing %q", service.tokens, h.g.settings, window.Texts())
	}
	// The service replaces the token only while work means the connection
	// the dialog opened for.
	if want := []string{opened.Target(), opened.Target()}; opened.Target() == "" || !slices.Equal(service.targets, want) {
		t.Fatalf("replaced for targets %q, want %q", service.targets, want)
	}
}

// Settings shows this Mac's stores only once they are read: until then it
// waits, and when they cannot be read it says so, as the main window does,
// instead of showing iCloud Keychain as the only store and the default.
func TestSettingsBeforeTheStoresAreRead(t *testing.T) {
	h := newHarness(t)
	r := h.addRemote()
	h.g.stores, h.g.storesRead = stores.Config{Default: stores.ICloud}, false
	window := ui.NewTester(h.g.settingsView, 560, 620)
	h.g.settings.redraw = window.Frame
	window.Frame()
	if _, loading := window.Access("Loading"); !loading || window.HasText("Default") {
		t.Fatalf("before the stores are read: showing %q", window.Texts())
	}
	h.g.loadStores = func() (stores.Config, error) {
		return stores.Config{}, &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}
	}
	h.reload()
	if !window.HasText("Can’t Read This Mac’s Stores") || !window.HasText("This Mac's store configuration cannot be read.") || window.HasText("Default") {
		t.Fatalf("after a failed read: showing %q", window.Texts())
	}
	h.g.loadStores = func() (stores.Config, error) { return r.config, nil }
	if err := window.Click("Try Again"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if !window.HasText("work") || !window.HasText("Default") {
		t.Fatalf("after Try Again: showing %q", window.Texts())
	}
}
