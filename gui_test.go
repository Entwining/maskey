package main

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

const (
	tokenValue = "synthetic-token-5b1c"
	urlValue   = "synthetic-url-9e44"
)

type harness struct {
	t       *testing.T
	backend *storetest.Backend
	g       *gui
	tt      *ui.Tester
	posted  chan func()

	mu     sync.Mutex
	alerts []*sheet // shown and not yet answered, oldest first
	held   int      // operations pause holds
	woken  chan struct{}
	// dialogs counts the owner's authentications, which fail with refusal
	// when it is set.
	dialogs int
	refusal error
	// reads holds what each value read was for.
	reads []string
}

// sheet is an alert the window shows, waiting for its answer.
type sheet struct {
	alert
	answer chan int
}

func (s *sheet) titles() []string {
	titles := make([]string, len(s.buttons))
	for i, b := range s.buttons {
		titles[i] = b.title
	}
	return titles
}

// newHarness shows the window over a fake store with two namespaces. Work
// the window starts off the UI thread posts its results to a channel that
// settle drains on the test goroutine, as Window.Update would.
func newHarness(t *testing.T) *harness {
	t.Helper()
	backend := storetest.New()
	backend.Put("app/API_TOKEN", []byte(tokenValue))
	backend.Put("app/URL", []byte(urlValue))
	backend.Put("db/PASSWORD", []byte("synthetic-db-0a7f"))
	return newHarnessWith(t, backend)
}

func newHarnessWith(t *testing.T, backend *storetest.Backend) *harness {
	t.Helper()
	h := &harness{t: t, backend: backend, posted: make(chan func(), 64), woken: make(chan struct{}, 1)}
	// The clock stands still, so an approval lasts the whole test.
	approval := owner.NewApproval(h.authenticate, func() (time.Duration, bool) { return 0, true })
	h.g = newGUI(context.Background(), store.New(taggedReads{backend, &h.reads}, new(storetest.Recorder)), approval.Approve)
	h.g.post = func(fn func()) { h.posted <- fn }
	h.g.showMessage = h.showMessage
	h.tt = ui.NewTester(h.g.view, 980, 600)
	h.g.refresh()
	h.settle()
	return h
}

type taggedReads struct {
	*storetest.Backend
	reads *[]string
}

func (b taggedReads) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	*b.reads = append(*b.reads, activity.ViaOf(ctx))
	return b.Backend.Read(ctx, accounts)
}

func (h *harness) authenticate(context.Context, string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dialogs++
	return h.refusal
}

// settle applies posted results until no work is pending but alerts
// waiting for an answer and operations pause holds.
func (h *harness) settle() {
	h.t.Helper()
	for {
		h.tt.Frame()
		h.mu.Lock()
		waiting := len(h.alerts) + h.held
		h.mu.Unlock()
		if h.g.pending == waiting {
			return
		}
		select {
		case fn := <-h.posted:
			fn()
		case <-h.woken:
		case <-time.After(5 * time.Second):
			h.t.Fatal("pending work never finished")
		}
	}
}

// wake tells settle that work began to wait.
func (h *harness) wake() {
	select {
	case h.woken <- struct{}{}:
	default:
	}
}

// showMessage stands in for showSheet: it shows nothing, and waits for the
// test to answer.
func (h *harness) showMessage(a alert) (int, error) {
	s := &sheet{a, make(chan int)}
	h.mu.Lock()
	h.alerts = append(h.alerts, s)
	h.mu.Unlock()
	h.wake()
	return <-s.answer, nil
}

func (h *harness) openAlerts() []*sheet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.alerts)
}

// alert returns the oldest alert open.
func (h *harness) alert() *sheet {
	h.t.Helper()
	open := h.openAlerts()
	if len(open) == 0 {
		h.t.Fatal("no alert is open")
	}
	return open[0]
}

// answer clicks the button labeled label in the oldest alert open.
func (h *harness) answer(label string) {
	h.t.Helper()
	a := h.alert()
	i := slices.IndexFunc(a.buttons, func(b alertButton) bool { return b.title == label })
	if i < 0 {
		h.t.Fatalf("no %q in %q", label, a.titles())
	}
	h.press(a, i)
}

// enter and escape answer the oldest alert open as those keys do.
func (h *harness) enter() {
	h.t.Helper()
	a := h.alert()
	i := slices.IndexFunc(a.buttons, func(b alertButton) bool { return b.isDefault })
	if i < 0 {
		h.t.Fatalf("Return chooses nothing in %+v", a.alert)
	}
	h.press(a, i)
}

func (h *harness) escape() {
	h.t.Helper()
	a := h.alert()
	h.press(a, a.cancelButton())
}

// press answers a with its button i, and settles.
func (h *harness) press(a *sheet, i int) {
	h.t.Helper()
	h.mu.Lock()
	h.alerts = slices.DeleteFunc(h.alerts, func(b *sheet) bool { return b == a })
	h.mu.Unlock()
	a.answer <- i
	h.settle()
}

// applyPostedWhile applies posted results while cond holds.
func (h *harness) applyPostedWhile(cond func() bool) {
	h.t.Helper()
	for cond() {
		select {
		case fn := <-h.posted:
			fn()
		case <-time.After(5 * time.Second):
			h.t.Fatal("no result was posted")
		}
	}
}

// pause holds every backend operation op until release is called, and
// closes started when the first begins.
func (h *harness) pause(op string) (started <-chan struct{}, release func()) {
	h.t.Helper()
	begun, released := make(chan struct{}), make(chan struct{})
	var beginOnce sync.Once
	// done and holding, guarded by h.mu, let settle count what this pause
	// holds; release lets it go on the test goroutine.
	done, holding := false, 0
	release = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !done {
			done, h.held = true, h.held-holding
			close(released)
		}
	}
	h.t.Cleanup(release)
	h.backend.Before = func(o, _ string) error {
		if o == op {
			beginOnce.Do(func() { close(begun) })
			h.mu.Lock()
			if !done {
				holding++
				h.held++
			}
			h.mu.Unlock()
			h.wake()
			<-released
		}
		return nil
	}
	return begun, release
}

func (h *harness) waitFor(started <-chan struct{}) {
	h.t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		h.t.Fatal("the operation did not start")
	}
}

func (h *harness) click(s string) {
	h.t.Helper()
	h.clickWithoutSettling(s)
	h.settle()
}

func (h *harness) clickWithoutSettling(s string) {
	h.t.Helper()
	if err := h.tt.Click(s); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) typeInto(label, text string) {
	h.t.Helper()
	h.click(label)
	h.tt.Type(text)
}

// replaceText selects the text of the input labeled label and types text
// over it.
func (h *harness) replaceText(label, text string) {
	h.t.Helper()
	h.click(label)
	h.tt.Command("selectAll")
	h.tt.Type(text)
}

// typeName, typeNamespace and typeValue type over the text of the form's
// fields, focused as the form's errors focus them.
func (h *harness) typeName(text string) { h.typeOver(&h.g.nameField, text) }

func (h *harness) typeNamespace(text string) { h.typeOver(&h.g.namespaceField, text) }

func (h *harness) typeValue(text string) { h.typeOver(&h.g.valueField, text) }

func (h *harness) typeOver(field *ui.Handle, text string) {
	h.t.Helper()
	field.Focus()
	h.tt.Frame()
	h.tt.Command("selectAll")
	h.tt.Type(text)
}

// choose picks item from the context menu of the row of entry.
func (h *harness) choose(entry, item string) {
	h.t.Helper()
	h.chooseWithoutSettling(entry, item)
	h.settle()
}

func (h *harness) chooseWithoutSettling(entry, item string) {
	h.t.Helper()
	if err := h.tt.RightClick(entry); err != nil {
		h.t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem(item); err != nil {
		h.t.Fatalf("%v; menu %q", err, h.tt.Menu())
	}
}

// newInApp opens the form for a new entry in namespace app, which it
// starts in, and types name.
func (h *harness) newInApp(name string) {
	h.t.Helper()
	h.click("app")
	h.click("New Entry")
	h.typeName(name)
}

// addWhileNamesLoad creates app/name and returns once the form has closed,
// while the names that follow stay loading until release.
func (h *harness) addWhileNamesLoad(name string) (loading <-chan struct{}, release func()) {
	h.t.Helper()
	h.newInApp(name)
	h.typeValue("synthetic-new-31d2")
	loading, release = h.pause("items")
	h.clickWithoutSettling("Add")
	h.applyPostedWhile(func() bool { return h.g.form.open })
	h.tt.Frame()
	return loading, release
}

func (h *harness) attributes(account string) store.Attributes {
	h.t.Helper()
	items, err := h.backend.Items(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, item := range items {
		if item.Account == account {
			return item.Attributes
		}
	}
	h.t.Fatalf("no item %s", account)
	return store.Attributes{}
}

func (h *harness) value(account string) (string, bool) {
	values, _ := h.backend.Read(context.Background(), []string{account})
	v, ok := values[account]
	return string(v), ok
}

func TestNamespaceNavigation(t *testing.T) {
	h := newHarness(t)
	all := func() bool { return h.tt.HasText("API_TOKEN") && h.tt.HasText("URL") && h.tt.HasText("PASSWORD") }
	if !all() {
		t.Fatalf("the window opens showing %q", h.tt.Texts())
	}
	h.click("db")
	if !h.tt.HasText("PASSWORD") || h.tt.HasText("API_TOKEN") {
		t.Fatalf("db shows %q", h.tt.Texts())
	}
	h.click("All Entries")
	if !all() {
		t.Fatalf("All Entries shows %q", h.tt.Texts())
	}
	if len(h.backend.Reads) != 0 {
		t.Fatalf("navigation read values: %q", h.backend.Reads)
	}
}

func TestEntriesLineUpWithSidebar(t *testing.T) {
	h := newHarness(t)
	side, ok1 := h.tt.Find("All Entries")
	entry, ok2 := h.tt.Find("API_TOKEN")
	if !ok1 || !ok2 {
		t.Fatalf("All Entries or API_TOKEN is absent: %q", h.tt.Texts())
	}
	if d := (side.Y + side.H/2) - (entry.Y + entry.H/2); d < -0.5 || d > 0.5 {
		t.Fatalf("All Entries at %v and API_TOKEN at %v are not on one line", side, entry)
	}
}

// A search matches names and namespaces in every namespace, not only the one
// chosen; choosing a namespace ends it.
func TestSearchCoversEveryNamespace(t *testing.T) {
	h := newHarness(t)
	for query, want := range map[string][]store.Ref{
		"pass": {{Namespace: "db", Name: "PASSWORD"}},
		"DB":   {{Namespace: "db", Name: "PASSWORD"}},
		"p":    {{Namespace: "app", Name: "API_TOKEN"}, {Namespace: "app", Name: "URL"}, {Namespace: "db", Name: "PASSWORD"}},
	} {
		h.replaceText("Search entries", query)
		if got := h.g.rows(); !slices.Equal(got, want) {
			t.Fatalf("search for %q shows %v", query, got)
		}
	}
	// A result acts on the entry of its own namespace.
	h.replaceText("Search entries", "pass")
	h.click("PASSWORD")
	h.click("Show Value")
	if !h.tt.HasText("synthetic-db-0a7f") {
		t.Fatalf("showing db/PASSWORD from app shows %q", h.tt.Texts())
	}
	h.replaceText("Search entries", "zzz")
	if !h.tt.HasText("No Results") {
		t.Fatalf("an empty search shows %q", h.tt.Texts())
	}
	h.click("app")
	if h.g.query != "" || !h.tt.HasText("URL") {
		t.Fatalf("choosing app kept the search %q: %q", h.g.query, h.tt.Texts())
	}
}

func TestBackendErrorShown(t *testing.T) {
	h := newHarness(t)
	h.click("URL")
	h.backend.Before = func(op, account string) error {
		if op == "read" {
			return &store.Error{Kind: store.KindLocked, OSStatus: -25308, Message: "the Keychain is unavailable until this Mac is unlocked"}
		}
		return nil
	}
	h.click("Show Value")
	if a := h.alert(); a.message != "Couldn’t Read “URL”" || a.detail != "The Keychain is unavailable until this Mac is unlocked (OSStatus -25308)." || h.g.revealed != "" {
		t.Fatalf("alert %+v, revealed %v", a.alert, h.g.revealed != "")
	}
}

func TestListingErrorOffersRetry(t *testing.T) {
	backend := storetest.New()
	failing := true
	backend.Before = func(op, _ string) error {
		if op == "items" && failing {
			return &store.Error{Kind: store.KindPrerequisite, Message: "synthetic missing tools"}
		}
		return nil
	}
	backend.Put("app/URL", []byte(urlValue))
	h := newHarnessWith(t, backend)
	if !h.tt.HasText("Synthetic missing tools.") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	failing = false
	h.click("Try Again")
	if !h.tt.HasText("URL") {
		t.Fatalf("texts after retrying %q", h.tt.Texts())
	}
}

// A shown value is cleared when another entry is chosen or the window
// closes.
func TestRevealedValueCleared(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear func(*harness)
	}{
		{"another entry chosen", func(h *harness) { h.click("URL") }},
		{"window closed", func(h *harness) { h.g.close(); h.tt.Frame() }},
		// The list's keys would act on an entry no longer in view.
		{"hidden by a search", func(h *harness) { h.typeInto("Search entries", "pass") }},
		{"replaced elsewhere", func(h *harness) {
			h.backend.Put("app/API_TOKEN", []byte("synthetic-replaced-5c1e"))
			h.g.refresh()
			h.settle()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.click("API_TOKEN")
			h.click("Show Value")
			if !h.tt.HasText(tokenValue) {
				t.Fatalf("reveal shows %q", h.tt.Texts())
			}
			tc.clear(h)
			if h.tt.HasText(tokenValue) || h.g.revealed != "" {
				t.Fatal("the value stayed")
			}
		})
	}
}

// The window reloads on every focus, which must not hide a value that is
// still the entry's.
func TestRevealSurvivesUnchangedReload(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	h.click("Show Value")
	h.g.refresh()
	h.settle()
	if !h.tt.HasText(tokenValue) {
		t.Fatalf("texts after the reload %q", h.tt.Texts())
	}
}

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
	if got := h.tt.Clipboard(); got != tokenValue {
		t.Fatalf("Cmd-C copied %q", got)
	}
	h.tt.SetClipboard("")
	h.typeInto("Search entries", "tok")
	h.tt.Command("selectAll")
	h.tt.Key(ui.Cmd, ui.KeyC)
	h.settle()
	if got := h.tt.Clipboard(); got == tokenValue {
		t.Fatal("Cmd-C in the search field copied the value")
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

// A value requested for one entry must not show after another is chosen.
func TestStaleRevealIgnored(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	_, release := h.pause("read")
	h.clickWithoutSettling("Show Value")
	h.clickWithoutSettling("URL")
	release()
	h.settle()
	if h.g.revealed != "" || h.tt.HasText(tokenValue) {
		t.Fatalf("a stale reveal shows under %s", h.g.entry)
	}
}
