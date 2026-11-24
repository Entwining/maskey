package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/cli"
	"github.com/Entwining/maskey/internal/sshagent"
	"github.com/Entwining/maskey/internal/store"
)

// read loads id's value and passes it to use unless another entry was
// chosen meanwhile. A listing that shows id replaced while it loaded can
// mean the value read is the one replaced, so id is read again.
func (g *gui) read(id entryID, action string, use func(value []byte)) {
	token, modified := g.selToken, g.attributes(id).Modified
	g.readAction = action
	opened := g.open(id.store).store
	g.start(func() func() {
		value, err := opened.Value(activity.WithVia(g.ctx, action), id.ref)
		return func() {
			if token != g.selToken {
				return
			}
			g.readAction = ""
			if err != nil {
				g.showAlert(fmt.Sprintf("Couldn’t Read “%s”", id.ref.Name), err)
				return
			}
			if !g.attributes(id).Modified.Equal(modified) {
				g.read(id, action, use)
				return
			}
			use(value)
		}
	})
}

func (g *gui) toggleReveal() {
	if g.revealed != "" {
		g.revealed = ""
		return
	}
	g.read(g.entry, "reveal", func(value []byte) { g.revealed = string(value) })
}

const clipboardClearDelay = 90 * time.Second

// valueClipboard is the clipboard as the window's Copy uses it. The change
// count identifies a write, so clearing it leaves a later copy alone, even
// one of the same value.
type valueClipboard interface {
	// write replaces the clipboard's contents with value and returns the
	// clipboard's change count after the write.
	write(value []byte) (change int, err error)
	// clearIf clears the clipboard if its change count is still change.
	clearIf(change int)
}

// copy puts id's value on the clipboard; only the chosen entry's card
// confirms it.
func (g *gui) copy(id entryID) {
	chosen := id == g.entry
	g.copied = false
	g.read(id, "copy", func(value []byte) {
		change, err := g.clipboard.write(value)
		if err != nil {
			g.showAlert(fmt.Sprintf("Couldn’t Copy “%s”", id.ref.Name), err)
			return
		}
		clipboard := g.clipboard
		g.clearCopy = func() { clipboard.clearIf(change) }
		g.clearLater(clipboardClearDelay, g.clearCopy)
		if chosen {
			g.copied, g.copiedUntil = true, time.Time{}
		}
	})
}

// openNew opens the form for a new entry in the store and namespace chosen,
// or without one chosen in the store shown alone or else this Mac's default,
// once the open form is left.
func (g *gui) openNew() {
	// Until this Mac's stores are read, the default one is not known, so a
	// new entry has no store to go to.
	if !g.storesRead {
		return
	}
	g.leave(func() {
		to, namespace := g.scope.store, g.scope.namespace
		if g.scope == (scope{}) {
			to = cmp.Or(g.filter, g.stores.Default)
		}
		chosenBefore := g.entry
		g.choose(entryID{})
		g.selectionChanged()
		g.pick = entryID{}
		g.form = form{open: true, id: g.form.id + 1, store: to, chosenBefore: chosenBefore, namespaceFrom: namespace, namespace: namespace, kind: kindLabel(store.InferValueKind("")), inferredFrom: new(string)}
	})
}

// openEdit chooses id and opens the form for it, once the open form is
// left, and loads the value it starts from.
func (g *gui) openEdit(id entryID) {
	if g.canEdit(id) {
		g.leave(func() { g.edit(id) })
	}
}

func (g *gui) canEdit(id entryID) bool {
	return id != (entryID{}) && !g.writing(id) && (!g.form.open || g.form.from != id)
}

func (g *gui) edit(id entryID) {
	g.choose(id)
	g.selectionChanged()
	g.pick = entryID{}
	ref, details := id.ref, g.attributes(id).Details
	kind := kindLabel(store.ValueKindOf(ref.Name, details.Kind))
	g.form = form{open: true, id: g.form.id + 1, from: id, store: id.store, namespace: ref.Namespace, name: ref.Name,
		kind: kind, kindFrom: kind, notes: details.Notes, notesFrom: details.Notes, loading: true}
	formID := g.form.id
	opened := g.open(id.store).store
	g.start(func() func() {
		value, err := opened.Value(activity.WithVia(g.ctx, "edit"), ref)
		return func() {
			if !g.form.open || formID != g.form.id {
				return
			}
			g.form.loading = false
			switch {
			case err != nil:
				g.form.err = formError{message: sentence(err)}
			case strings.ContainsRune(string(value), '\r'):
				g.form.keepValue = true
				_, _, g.form.keptKey = sshagent.Key(value)
			default:
				g.form.value, g.form.original = string(value), string(value)
			}
		}
	})
}

// closeForm drops the form; a new entry's form gives the list back the entry
// chosen before it opened, unless the listing no longer has it.
func (g *gui) closeForm() {
	if g.form.open && g.form.from == (entryID{}) && g.has(g.form.chosenBefore) {
		g.entry = g.form.chosenBefore
	}
	g.form = form{id: g.form.id}
}

// write is a store write the window runs; one runs at a time.
type write struct {
	action   writeAction
	entry    entryID
	form     int  // the form a submit writes
	accessTo bool // the program access an access write sets
}

type writeAction int

const (
	submitWrite writeAction = iota
	deleteWrite
	accessWrite
)

// exit is how the window goes once its writes finish: closing it, or
// quitting the app, which closes it as well.
type exit int

const (
	noExit exit = iota
	closeExit
	quitExit
)

// startWrite runs w as the window's write, unless another runs: then an
// answer given to a question asked before that write started does nothing.
// Once w's result applies, a close or quit that waited for it goes ahead.
func (g *gui) startWrite(w *write, work func() func()) {
	if g.write != nil {
		return
	}
	g.write = w
	g.start(func() func() {
		apply := work()
		return func() {
			g.write = nil
			apply()
			g.resumeExit()
		}
	})
}

// busy reports whether the window is changing something outside it, which
// ending the process would leave unrecorded and unreported. A closed window
// starts and applies nothing, so it waits for nothing, whatever the menu
// bar, which outlives it, asked for.
func (g *gui) busy() bool { return !g.closed && (g.write != nil || g.cliInstalling) }

// beforeExit answers a request to close the window or quit, and reports
// whether to prevent it for now. While the window is busy, the exit waits
// until resumeExit; a write or installation that fails cancels it, so that
// its alert, or the form's error, shows. Otherwise the user is asked about
// unsaved changes. MyGo takes a prevented quit as cancelled and resumes it
// as a plain quit: a logout or restart stops, and the updater's Relaunch
// quits without starting Maskey again.
func (g *gui) beforeExit(e exit) bool {
	if g.busy() {
		g.exit = max(g.exit, e)
		return true
	}
	if e == quitExit {
		return g.ask(g.quit, true)
	}
	return g.ask(g.closeWindow, true)
}

// resumeExit closes or quits again, once nothing holds the exit back.
func (g *gui) resumeExit() {
	e := g.exit
	if e == noExit || g.busy() {
		return
	}
	g.exit = noExit
	if e == quitExit {
		g.quitApp()
	} else {
		g.closeWindow()
	}
}

// submitting reports whether the open form's write is running.
func (g *gui) submitting() bool {
	return g.form.open && g.write != nil && g.write.action == submitWrite && g.write.form == g.form.id
}

// canSubmit reports whether the form's primary action is available: the
// form would write something, and no other write runs.
func (g *gui) canSubmit() bool { return g.form.ready() && g.write == nil }

// leave runs then, which replaces the form, at once or, when the form holds
// unsaved changes, once the user has answered for them.
func (g *gui) leave(then func()) {
	if !g.ask(then, true) {
		g.closeForm()
		then()
	}
}

// cancelForm closes the form, once the user agrees to discard its unsaved
// changes.
func (g *gui) cancelForm() {
	if !g.ask(func() {}, false) {
		g.closeForm()
	}
}

// unsaved reports whether leaving the form waits on an answer about its
// unsaved changes. A write of the form already has its draft, or deletes
// its entry, so leaving then is what Close does.
func (g *gui) unsaved() bool {
	f := &g.form
	return g.confirmLeave || f.open && f.dirty() && !g.submitting() && !g.writing(f.from)
}

// ask defers then, and asks about the form's unsaved changes, when leaving
// waits on them, and reports whether it did. A window close or a quit while
// the question is open waits on it, leaving its buttons and the action it
// defers as they are.
func (g *gui) ask(then func(), offerSave bool) bool {
	if !g.unsaved() {
		return false
	}
	if g.confirmLeave {
		return true
	}
	f := &g.form
	g.confirmLeave = true
	p, id := g.newLeavePrompt(offerSave), f.id
	g.message(p.alert, func(button int) {
		g.confirmLeave = false
		// While another alert was answered first, a delete may have closed
		// the form or a write started; the answer was to a question that no
		// longer holds, so the user asks again.
		if !g.form.open || g.form.id != id {
			return
		}
		switch button {
		case p.saveAt:
			if g.canSubmit() {
				g.submit(then)
			}
		case p.discardAt:
			g.closeForm()
			then()
		}
	})
	return true
}

// submit creates the form's entry, or gives the edited one the form's
// namespace, name, value and details in one write. Once the write succeeds,
// unless the user has since chosen another entry, namespace, form, or
// search, then runs, unless nil, or else the list follows the entry
// written. Closing the form is not a choice: then is what the user chose
// last.
func (g *gui) submit(then func()) {
	f := &g.form
	to := store.Ref{Namespace: f.namespace, Name: f.name}
	written := entryID{f.store, to}
	if err := store.ValidateNamespace(to.Namespace); err != nil {
		f.err = formError{"namespace", sentence(err)}
		g.namespaceField.Focus()
		return
	}
	if err := store.ValidateName(to.Name); err != nil {
		f.err = formError{"name", sentence(err)}
		g.nameField.Focus()
		return
	}
	from := f.from
	var value []byte // nil keeps the stored value
	if from == (entryID{}) || !f.keepValue && f.value != f.original {
		value = []byte(f.value)
		if err := store.ValidateValue(value); err != nil {
			f.err = formError{"value", sentence(err)}
			g.valueField.Focus()
			return
		}
	}
	details := store.Details{Kind: kindOf(f.kind), Notes: f.notes}
	// A write that gives an SSH Key entry its value or its kind stores only
	// a key mk run can lend, as mk set does; the window cannot remove a
	// passphrase.
	if details.Kind == store.ValueSSH && (value != nil || f.kind != f.kindFrom) {
		err := f.keptKey
		if !f.keepValue {
			key := value
			if key == nil {
				key = []byte(f.original)
			}
			_, _, err = sshagent.Key(key)
		}
		if err != nil {
			// A kept value has no field to paste into.
			replace := "Paste"
			if f.keepValue {
				replace = "In Terminal, use mk set --stdin to store"
			}
			f.err = formError{"value", "This is not an SSH private key. " + replace + " the private key itself, not its public key, or choose another kind."}
			switch {
			case errors.Is(err, sshagent.ErrPassphrase):
				f.err.message = "This SSH key is protected by a passphrase, which mk run does not ask for. In Terminal, mk set --stdin removes it from a key it stores in an SSH Key entry, such as a new one whose name ends in SSH_KEY."
			case errors.Is(err, sshagent.ErrPKCS8):
				f.err.message = "This SSH key is protected by a passphrase that Maskey cannot remove. In Terminal, pipe openssl pkey -in <file> to mk set --stdin, which stores it without the passphrase."
			case errors.Is(err, sshagent.ErrUnsupported):
				f.err.message = "Maskey cannot lend this SSH key. " + replace + " the whole key, of type Ed25519, RSA, or ECDSA P-256, P-384, or P-521, or choose another kind."
			}
			g.valueField.Focus()
			return
		}
	}
	var editedDetails *store.Details // nil keeps the stored details
	if f.detailsChanged() {
		editedDetails = &details
	}
	f.err = formError{}
	id := f.id
	verb, done, target := "Save", "Saved", from
	if from == (entryID{}) {
		verb, done, target = "Add", "Added", written
	}
	moves, opened := g.moves, g.open(f.store).store
	g.startWrite(&write{action: submitWrite, entry: target, form: id}, func() func() {
		var err error
		if from == (entryID{}) {
			err = opened.Create(g.ctx, to, value, details)
		} else {
			err = opened.Edit(g.ctx, from.ref, to, value, editedDetails)
		}
		return func() {
			sameForm := g.form.open && id == g.form.id
			if err != nil {
				// The form stays as it was, to correct and retry; the list
				// shows what the failed or uncertain write left behind.
				if sameForm {
					g.form.err = formError{message: sentence(err)}
					if store.IsKind(err, store.KindExists) {
						g.form.err = formError{"name", fmt.Sprintf("%s already has an entry named %s.", to.Namespace, to.Name)}
						g.nameField.Focus()
					}
				}
				g.writeFailed(sameForm, verb, done, target, err)
				return
			}
			if sameForm {
				g.closeForm()
			}
			switch {
			case g.moves != moves:
				g.refresh()
			case then != nil:
				then()
				g.refresh()
			default:
				g.follow(written)
			}
		}
	})
}

// askDelete deletes id once the user confirms. Nothing undoes a delete, so
// no button is the default: Return deletes nothing.
func (g *gui) askDelete(id entryID) {
	// The window shows the entry asked about, but for another row's Delete…
	// beside a form, which names it.
	message := "Delete this entry?"
	if g.form.open && g.form.from != id {
		message = fmt.Sprintf("Delete “%s” from %s?", id.ref.Name, id.ref.Namespace)
	}
	g.message(alert{message: message, detail: "You can’t undo this action.",
		buttons: []alertButton{{title: "Delete", destructive: true}, {title: "Cancel", isCancel: true}}}, func(button int) {
		// A store that left this Mac while the question stood takes no
		// delete.
		if button == 0 && g.listed[id.store] != nil {
			g.delete(id)
		}
	})
}

func (g *gui) delete(id entryID) {
	opened := g.open(id.store).store
	g.startWrite(&write{action: deleteWrite, entry: id}, func() func() {
		err := opened.Delete(g.ctx, id.ref)
		return func() {
			if err != nil {
				g.writeFailed(false, "Delete", "Deleted", id, err)
				return
			}
			if g.form.open && g.form.from == id {
				g.closeForm()
			}
			if g.entry == id {
				g.entry = entryID{}
				g.selectionChanged()
			}
			g.refresh()
		}
	})
}

// changeAccess grants or revokes id's program access once the owner
// approves. The switch shows the change while it runs and the stored state
// again if the owner declines or the write fails.
func (g *gui) changeAccess(id entryID, granted bool) {
	setAccess := g.open(id.store).setAccess
	g.startWrite(&write{action: accessWrite, entry: id, accessTo: granted}, func() func() {
		err := setAccess(g.ctx, id.ref, granted)
		return func() {
			switch {
			case err == nil:
				if s := g.listed[id.store]; s != nil {
					attributes := s.attributes[id.ref]
					attributes.ProgramAccess = granted
					s.attributes[id.ref] = attributes
				}
				g.refresh()
			case store.IsKind(err, store.KindCancelled):
			default:
				g.writeFailed(false, "Change Program Access for", "Changed", id, err)
			}
		}
	})
}

// writeFailed reports a write's failure in an alert when its form is no
// longer open. A failed write may have changed the store, so the lists
// reload either way.
func (g *gui) writeFailed(formOpen bool, verb, done string, id entryID, err error) {
	g.exit = noExit
	if !formOpen {
		title := fmt.Sprintf("Couldn’t %s “%s”", verb, id.ref.Name)
		if store.IsKind(err, store.KindUncertain) {
			title = fmt.Sprintf("Couldn’t Confirm That “%s” Was %s", id.ref.Name, done)
		}
		g.showAlert(title, err)
	}
	g.refresh()
}

// Report the outcome after installation; showMessage cannot update an open alert.
func (g *gui) installCLI() {
	if g.cliInstalling {
		return
	}
	g.cliInstalling = true
	g.start(func() func() {
		ctx, cancel := context.WithTimeout(g.ctx, time.Minute)
		defer cancel()
		executable, err := os.Executable()
		var home, link string
		if err == nil {
			home, err = os.UserHomeDir()
		}
		if err == nil {
			link, err = cli.InstallCLI(home, executable)
		}
		if err == nil {
			err = agent.Install(ctx, executable)
		}
		return func() {
			g.cliInstalling = false
			if err != nil {
				g.exit = noExit
				g.showAlert("Couldn’t Install the Command Line Tool", err)
				return
			}
			// The exit that goes ahead now ends the window before it could
			// show the message; one still held for a write, or for an
			// answer about unsaved changes, shows it.
			ends := g.exit != noExit && !g.busy() && !g.unsaved()
			g.resumeExit()
			if ends {
				return
			}
			dir := filepath.Dir(link)
			if rest, ok := strings.CutPrefix(dir, home); ok {
				dir = "~" + rest
			}
			g.message(alert{message: "Command Line Tool Installed",
				detail: fmt.Sprintf("mk is in %s. If your shell can’t find it, add that directory to PATH.", dir), buttons: []alertButton{okButton}}, nil)
		}
	})
}
