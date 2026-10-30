package main

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/keychain"
	"github.com/Entwining/maskey/internal/store"
)

const (
	titleBarHeight = 52
	sidebarWidth   = 200
	listWidth      = 280
	// toolbarButtonWidth fits Edit, Save, Add, Cancel, and Close, which take
	// turns in the detail's two button slots, so that opening, saving, and
	// closing a form move nothing in its toolbar.
	toolbarButtonWidth = 80
	// The search field shrinks from searchWidth to searchMinWidth as the
	// window narrows to its minimum width; searchMinWidth still holds the
	// icon, some text, and the clear button.
	searchWidth    = 220
	searchMinWidth = 124
	monospace      = "monospace"
	hiddenValue    = "••••••••••••"
	// allNamespaces is the sidebar's item for every entry. It cannot name a
	// namespace, so it never stands for one.
	allNamespaces = "*"
)

func runGUI() {
	g := newGUI(store.New(keychain.New(store.Service)))
	mygo.Use(native.Plugin)
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:  "Maskey",
			Width:  980,
			Height: 600,
			// The toolbar's two button slots and the search field at its
			// minimum width fit beside the sidebar and the list.
			MinWidth:  820,
			MinHeight: 420,
			StateKey:  "main",
			// The window controls sit over the sidebar, as in Finder and Notes.
			TitleBarStyle:  mygo.TitleBarHiddenInset,
			TitleBarHeight: titleBarHeight,
			Vibrancy:       mygo.VibrancySidebar,
			Content:        ui.View(g.view),
		})
		g.post = win.Update
		g.showMessage = func(a alert) (int, error) { return showSheet(win, a) }
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleAbout},
				updater.MenuItem(),
				mygo.Separator(),
				{Label: "Install Command Line Tool…", Click: g.onMenu(g.installCLI)},
				mygo.Separator(),
				{Role: mygo.RoleServices},
				mygo.Separator(),
				{Role: mygo.RoleHide},
				{Role: mygo.RoleHideOthers},
				{Role: mygo.RoleUnhide},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}},
			{Role: mygo.RoleFileMenu, Submenu: []*mygo.MenuItem{
				{Label: "New Entry", Accelerator: "CmdOrCtrl+N", Click: g.onMenu(g.openNew)},
				mygo.Separator(),
				{Role: mygo.RoleClose},
			}},
			// MyGo's standard Edit menu, with the entry's Delete… in place of
			// Delete, which MyGo gives the list no way to take, and without
			// Speech: only NSTextView and NSTextField start speaking, and
			// MyGo's text is neither.
			{Role: mygo.RoleEditMenu, Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleUndo},
				{Role: mygo.RoleRedo},
				mygo.Separator(),
				{Role: mygo.RoleCut},
				{Role: mygo.RoleCopy},
				{Role: mygo.RolePaste},
				{Role: mygo.RolePasteAndMatchStyle},
				g.entryItems.delete,
				{Role: mygo.RoleSelectAll},
				mygo.Separator(),
				g.entryItems.edit,
			}},
			{Role: mygo.RoleViewMenu, Submenu: []*mygo.MenuItem{
				g.entryItems.reveal,
				mygo.Separator(),
				{Label: "Reload", Accelerator: "CmdOrCtrl+R", Click: g.onMenu(g.refresh)},
				mygo.Separator(),
				{Role: mygo.RoleToggleFullScreen},
			}},
			{Role: mygo.RoleWindowMenu},
		}))
		// Edits made with mk show up when the window comes back.
		win.OnFocus(g.refresh)
		// Closing the window or quitting waits for an answer about unsaved
		// changes. Both events arrive on the UI thread.
		g.closeWindow, g.quitApp = win.Close, mygo.App.Quit
		win.OnClose(func(e *mygo.CloseEvent) {
			if g.ask(g.closeWindow, true) {
				e.PreventDefault()
			}
		})
		mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
			if g.ask(g.quit, true) {
				e.PreventDefault()
			}
		})
		win.OnClosed(g.close)
		g.refresh()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// gui is the window's state. Keychain calls run on goroutines and publish
// their results through post, which runs a function on the UI thread.
type gui struct {
	store    *store.Store
	post     func(func())
	services ui.Services
	closed   bool
	pending  int
	// closeWindow closes the window at once; quitApp starts a quit, which
	// runs later.
	closeWindow, quitApp func()
	// showMessage shows an alert and waits for the index of the button
	// chosen.
	showMessage func(alert) (int, error)
	entryItems  entryItems

	namespaces []store.Namespace
	refs       []store.Ref // every entry, sorted by namespace and name
	attributes map[store.Ref]store.Attributes
	namespace  string // the sidebar's choice: a namespace or allNamespaces
	listToken  int
	loaded     bool // a listing has finished
	listErr    string

	// A query searches every namespace, not the one chosen.
	query  string
	search ui.Handle
	// The form fields an error can send the focus back to.
	namespaceField ui.Handle
	nameField      ui.Handle
	valueField     ui.Handle
	list           ui.ListState
	row            int
	// entry is the entry chosen, zero for none; while a form is open, the
	// form's entry while the listing has it, zero for a new one.
	entry store.Ref
	pick  store.Ref // the entry to choose once the list includes it
	// moves counts the user's choices of entry, namespace, form, and
	// search, so that a write landing later can tell whether the user has
	// moved on.
	moves int

	// At most one value shows, the chosen entry's; choosing another hides it.
	selToken    int
	readAction  string // "reveal" or "copy" while the chosen entry's value loads
	revealed    string
	copied      bool
	copiedUntil time.Time

	writeAction string // "submit" or "delete"
	writeRef    store.Ref
	writeForm   int

	form form
	// confirmLeave holds while the question about the form's unsaved
	// changes is open.
	confirmLeave bool

	cliInstalling bool
}

// form is the detail pane's editor, of a new entry or of the entry from.
type form struct {
	open bool
	id   int
	from store.Ref // zero for a new entry
	// chosenBefore is the entry chosen before a new entry's form opened,
	// chosen again when it closes.
	chosenBefore store.Ref
	namespace    string
	name         string
	value        string
	original     string // the stored value, once loaded
	kind         string // a label of kinds
	notes        string
	// kindFrom and notesFrom are what the form showed when it opened; the
	// form writes the details only when they differ.
	kindFrom  string
	notesFrom string
	// inferredFrom is the name the kind was inferred from, while the kind
	// follows the name: in a new entry's form, until the user picks one.
	inferredFrom *string
	loading      bool
	// keepValue is set when the stored value holds carriage returns, which
	// the text area would change: the entry can move, but its value stays.
	keepValue bool
	err       formError
}

// ready reports whether submitting the form would write anything.
func (f *form) ready() bool {
	if f.from == (store.Ref{}) {
		return f.namespace != "" && f.name != "" && f.value != ""
	}
	if f.loading || f.original == "" && !f.keepValue {
		return false
	}
	return f.namespace != f.from.Namespace || f.name != f.from.Name || !f.keepValue && f.value != f.original || f.detailsChanged()
}

func (f *form) detailsChanged() bool { return f.kind != f.kindFrom || f.notes != f.notesFrom }

// dirty reports whether the form holds anything the user typed. A value
// still loading or kept as stored is not, and neither, in a new entry's
// form, are its namespace and kind.
func (f *form) dirty() bool {
	if f.from == (store.Ref{}) {
		return f.name != "" || f.value != "" || f.notes != ""
	}
	return f.namespace != f.from.Namespace || f.name != f.from.Name || f.value != f.original || f.detailsChanged()
}

// followName infers the kind from the name, unless the user picked a kind
// other than the one inferred.
func (f *form) followName() {
	if f.inferredFrom == nil {
		return
	}
	if f.kind != kindLabel(store.InferValueKind(*f.inferredFrom)) {
		f.inferredFrom = nil
		return
	}
	name := f.name
	f.kind, f.inferredFrom = kindLabel(store.InferValueKind(name)), &name
}

// formError is a failure shown in a form, under the field it concerns, or
// below the fields when it concerns none.
type formError struct {
	field   string // "namespace", "name", "value", or ""
	message string
}

func (e formError) on(field string) string {
	if e.field == field {
		return e.message
	}
	return ""
}

func newGUI(s *store.Store) *gui {
	g := &gui{store: s, namespace: allNamespaces}
	g.entryItems = newEntryItems(g)
	return g
}

// onMenu runs fn when a menu item is chosen, through post, so that the
// window redraws after it.
func (g *gui) onMenu(fn func()) func(*mygo.MenuItem, *mygo.Window) {
	return func(*mygo.MenuItem, *mygo.Window) { g.post(fn) }
}

func (g *gui) showing(ref store.Ref) bool { return ref == g.entry && g.revealed != "" }

// The value's buttons, the row's menu, and the list's keys share these
// rules: no value is shown or copied while one is read, and none while it
// is written, when it cannot be edited either; none is shown while a form
// has its place; a shown value can always be hidden.
func (g *gui) writing(ref store.Ref) bool { return g.writeAction != "" && g.writeRef == ref }

func (g *gui) canCopy(ref store.Ref) bool { return g.readAction == "" && !g.writing(ref) }

func (g *gui) canReveal(ref store.Ref) bool { return g.showing(ref) || !g.form.open && g.canCopy(ref) }

// selectionChanged invalidates the work started for the old selection.
func (g *gui) selectionChanged() {
	g.selToken++
	g.readAction = ""
	g.revealed = ""
	g.copied, g.copiedUntil = false, time.Time{}
}

// choose makes ref the chosen entry, for an action on its row.
func (g *gui) choose(ref store.Ref) {
	g.moves++
	if ref != g.entry {
		g.entry = ref
		g.selectionChanged()
	}
}

// showNamespace shows a namespace's entries, or every entry, ending a
// search.
func (g *gui) showNamespace(namespace string) {
	g.moves++
	g.namespace, g.query = namespace, ""
	g.entry, g.pick = store.Ref{}, store.Ref{}
	g.selectionChanged()
}

// follow shows ref, just written, in the list and chooses it once a
// listing includes it.
func (g *gui) follow(ref store.Ref) {
	if g.namespace != allNamespaces {
		g.namespace = ref.Namespace
	}
	g.query = ""
	g.pick = ref
	g.refresh()
}

func (g *gui) close() {
	g.closed = true
	g.selectionChanged()
	g.form.value, g.form.original = "", ""
}

// quit closes the window first, so that no work starts after it, then quits
// the app, which another window, such as the updater's, keeps running
// otherwise. Closing the last window quits as well; a second quit is
// harmless.
func (g *gui) quit() {
	g.closeWindow()
	g.quitApp()
}

// refresh reloads the namespaces and the names of every entry; it never
// loads values.
func (g *gui) refresh() {
	g.listToken++
	token := g.listToken
	g.start(func() func() {
		namespaces, refs, attributes, err := g.store.Listing(context.Background())
		return func() {
			if token != g.listToken {
				return
			}
			g.loaded = true
			if err != nil {
				g.listErr = sentence(err)
				return
			}
			g.listErr = ""
			// A value shown before another writer replaced it is no longer
			// the entry's.
			if g.revealed != "" && !attributes[g.entry].Modified.Equal(g.attributes[g.entry].Modified) {
				g.selectionChanged()
			}
			g.namespaces, g.refs, g.attributes = namespaces, refs, attributes
			if !slices.ContainsFunc(namespaces, func(n store.Namespace) bool { return n.Name == g.namespace }) {
				g.namespace = allNamespaces
			}
			if !slices.Contains(refs, g.pick) {
				g.pick = store.Ref{}
			}
			if g.entry != (store.Ref{}) && !slices.Contains(refs, g.entry) {
				g.entry = store.Ref{}
				g.selectionChanged()
			}
		}
	})
}

// start runs work off the UI thread and applies its result. Once the window
// has closed, it starts none and drops results.
func (g *gui) start(work func() func()) {
	if g.closed {
		return
	}
	g.pending++
	go func() {
		apply := work()
		g.post(func() {
			g.pending--
			if !g.closed {
				apply()
			}
		})
	}()
}

// message shows an alert off the UI thread, since it waits for an answer,
// and passes the index of the button chosen to answer, unless nil, on the
// UI thread. An alert that can't show, as while the app quits, answers
// with its cancel button.
func (g *gui) message(a alert, answer func(button int)) {
	g.start(func() func() {
		button, err := g.showMessage(a)
		if err != nil {
			button = a.cancelButton()
		}
		return func() {
			if answer != nil {
				answer(button)
			}
		}
	})
}

var okButton = alertButton{title: "OK", isDefault: true, isCancel: true}

func (g *gui) showAlert(title string, err error) {
	g.message(alert{message: title, detail: sentence(err), buttons: []alertButton{okButton}}, nil)
}

// sentence writes an error as a sentence for the window. A store error's
// entry is left out: the window names the entry where it shows the error.
func sentence(err error) string {
	message := err.Error()
	var e *store.Error
	if errors.As(err, &e) {
		withoutRef := *e
		withoutRef.Ref = nil
		message = withoutRef.Error()
	}
	r, n := utf8.DecodeRuneInString(message)
	message = string(unicode.ToUpper(r)) + message[n:]
	if !strings.HasSuffix(message, ".") {
		message += "."
	}
	return message
}

// rows lists the entries the list shows: the chosen namespace's, or every
// entry, or while searching, every entry whose name or namespace contains
// the query, ignoring case.
func (g *gui) rows() []store.Ref {
	query := strings.ToLower(g.query)
	var refs []store.Ref
	for _, ref := range g.refs {
		if query == "" && (g.namespace == allNamespaces || ref.Namespace == g.namespace) ||
			query != "" && (strings.Contains(strings.ToLower(ref.Name), query) || strings.Contains(strings.ToLower(ref.Namespace), query)) {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (g *gui) view(c *ui.Context) {
	c.SetTheme(theme(c.Theme()))
	t := c.Theme()
	g.services = c.Services()
	if g.copied {
		if g.copiedUntil.IsZero() {
			g.copiedUntil = c.Now().Add(2 * time.Second)
		}
		if c.Now().Before(g.copiedUntil) {
			c.After(g.copiedUntil.Sub(c.Now()))
		} else {
			g.copied = false
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		g.search.Focus(c)
	}
	vibrant := c.Vibrancy()
	if vibrant {
		c.Root().Background(ui.Transparent)
	}
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		side := ui.Column(c).Width(sidebarWidth).Shrink(0)
		if !vibrant {
			side.Background(t.Surface)
		}
		side.Children(func() { g.sidebar(c, vibrant) })
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Width(listWidth).Shrink(0).Background(t.Background).Children(func() { g.listPane(c) })
		ui.Box(c).Width(1).Background(t.Border)
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() { g.detail(c) })
	})
	g.updateEntryItems(g.list.Focused(c))
}

// kind is the kind of value the entry holds.
func (g *gui) kind(ref store.Ref) store.ValueKind {
	return store.ValueKindOf(ref.Name, g.attributes[ref].Kind)
}

// leavePrompt is the question about a form's unsaved changes, and the
// indexes of its Save and discard buttons; saveAt is -1 when it offers no
// Save.
type leavePrompt struct {
	alert
	saveAt, discardAt int
}

// newLeavePrompt asks, as Passwords does, what becomes of the form's unsaved
// changes, of the entry that the window shows beneath it. Leaving the form
// for something else offers to save only what the form's own button would,
// and makes that the default, so that Cmd-Enter in the form and Return here
// agree. Cancel never offers Save, which it has just declined. AppKit puts
// the first button rightmost, the second beside it, and a third at the far
// left.
func (g *gui) newLeavePrompt(offerSave bool) leavePrompt {
	f := &g.form
	edit := f.from != (store.Ref{})
	continueEditing := alertButton{title: "Continue Editing", isCancel: true}
	discard := alertButton{title: "Discard", destructive: true}
	switch {
	case offerSave && g.canSubmit() && edit:
		return leavePrompt{alert{message: "Save changes to this entry?",
			buttons: []alertButton{{title: "Save", isDefault: true}, continueEditing, {title: "Don’t Save", destructive: true}}}, 0, 2}
	case offerSave && g.canSubmit():
		return leavePrompt{alert{message: "Add the new entry?",
			buttons: []alertButton{{title: "Add", isDefault: true}, continueEditing, discard}}, 0, 2}
	}
	p := leavePrompt{alert{message: "Discard the new entry?"}, -1, 1}
	if edit {
		p.message = "Discard changes to this entry?"
	}
	continueEditing.isDefault = true
	p.buttons = []alertButton{continueEditing, discard}
	if offerSave && f.ready() {
		// The form would save but for the other write.
		p.detail = "Another change is still in progress."
	}
	return p
}
