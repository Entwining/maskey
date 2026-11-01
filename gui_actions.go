package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/cli"
	"github.com/Entwining/maskey/internal/store"
)

// read loads ref's value and passes it to use unless another entry was
// chosen meanwhile.
func (g *gui) read(ref store.Ref, action string, use func(value []byte)) {
	token := g.selToken
	g.readAction = action
	g.start(func() func() {
		value, err := g.store.Value(activity.WithVia(g.ctx, action), ref)
		return func() {
			if token != g.selToken {
				return
			}
			g.readAction = ""
			if err != nil {
				g.showAlert(fmt.Sprintf("Couldn’t Read “%s”", ref.Name), err)
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

// copy puts ref's value on the clipboard; the chosen entry's card confirms
// it.
func (g *gui) copy(ref store.Ref) {
	services := g.services
	chosen := ref == g.entry
	g.copied = false
	g.read(ref, "copy", func(value []byte) {
		services.WriteClipboard(string(value))
		if chosen {
			g.copied, g.copiedUntil = true, time.Time{}
		}
	})
}

// openNew opens the form for a new entry in the namespace chosen, once the
// open form is left.
func (g *gui) openNew() {
	g.leave(func() {
		namespace := g.namespace
		if namespace == allNamespaces {
			namespace = ""
		}
		chosenBefore := g.entry
		g.choose(store.Ref{})
		g.selectionChanged()
		g.pick = store.Ref{}
		g.form = form{open: true, id: g.form.id + 1, chosenBefore: chosenBefore, namespace: namespace, kind: kindLabel(store.InferValueKind("")), inferredFrom: new(string)}
	})
}

// openEdit chooses ref and opens the form for it, once the open form is
// left, and loads the value it starts from.
func (g *gui) openEdit(ref store.Ref) {
	if g.canEdit(ref) {
		g.leave(func() { g.edit(ref) })
	}
}

func (g *gui) canEdit(ref store.Ref) bool {
	return ref != (store.Ref{}) && !g.writing(ref) && (!g.form.open || g.form.from != ref)
}

func (g *gui) edit(ref store.Ref) {
	g.choose(ref)
	g.selectionChanged()
	g.pick = store.Ref{}
	details := g.attributes[ref].Details
	kind := kindLabel(store.ValueKindOf(ref.Name, details.Kind))
	g.form = form{open: true, id: g.form.id + 1, from: ref, namespace: ref.Namespace, name: ref.Name,
		kind: kind, kindFrom: kind, notes: details.Notes, notesFrom: details.Notes, loading: true}
	id := g.form.id
	g.start(func() func() {
		value, err := g.store.Value(activity.WithVia(g.ctx, "edit"), ref)
		return func() {
			if !g.form.open || id != g.form.id {
				return
			}
			g.form.loading = false
			switch {
			case err != nil:
				g.form.err = formError{message: sentence(err)}
			case strings.ContainsRune(string(value), '\r'):
				g.form.keepValue = true
			default:
				g.form.value, g.form.original = string(value), string(value)
			}
		}
	})
}

// closeForm drops the form; a new entry's form gives the list back the entry
// chosen before it opened, unless the listing no longer has it.
func (g *gui) closeForm() {
	if g.form.open && g.form.from == (store.Ref{}) && slices.Contains(g.refs, g.form.chosenBefore) {
		g.entry = g.form.chosenBefore
	}
	g.form = form{id: g.form.id}
}

// submitting reports whether the open form's write is running.
func (g *gui) submitting() bool {
	return g.form.open && g.writeForm == g.form.id && g.writeAction == "submit"
}

// canSubmit reports whether the form's primary action is available: the
// form would write something, and no other write runs.
func (g *gui) canSubmit() bool { return g.form.ready() && g.writeAction == "" }

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

// ask defers then, and asks about the form's unsaved changes, when it has
// any, and reports whether it did. A write of the form already has its
// draft, or deletes its entry, so leaving then is what Close does. A window
// close or a quit while the question is open waits on it, leaving its
// buttons and the action it defers as they are.
func (g *gui) ask(then func(), offerSave bool) bool {
	if g.confirmLeave {
		return true
	}
	f := &g.form
	if !f.open || !f.dirty() || g.submitting() || g.writing(f.from) {
		return false
	}
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
	if err := store.ValidateName(to.Name); err != nil {
		f.err = formError{"name", sentence(err)}
		g.nameField.Focus()
		return
	}
	if err := store.ValidateNamespace(to.Namespace); err != nil {
		f.err = formError{"namespace", sentence(err)}
		g.namespaceField.Focus()
		return
	}
	from := f.from
	var value []byte // nil keeps the stored value
	if from == (store.Ref{}) || !f.keepValue && f.value != f.original {
		value = []byte(f.value)
		if err := store.ValidateValue(value); err != nil {
			f.err = formError{"value", sentence(err)}
			g.valueField.Focus()
			return
		}
	}
	details := store.Details{Kind: kindOf(f.kind), Notes: f.notes}
	var editedDetails *store.Details // nil keeps the stored details
	if f.detailsChanged() {
		editedDetails = &details
	}
	f.err = formError{}
	id := f.id
	verb, done, target := "Save", "Saved", from
	if from == (store.Ref{}) {
		verb, done, target = "Add", "Added", to
	}
	g.writeAction, g.writeRef, g.writeForm = "submit", target, id
	moves := g.moves
	g.start(func() func() {
		var err error
		if from == (store.Ref{}) {
			err = g.store.Create(g.ctx, to, value, details)
		} else {
			err = g.store.Edit(g.ctx, from, to, value, editedDetails)
		}
		return func() {
			g.writeAction = ""
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
				g.follow(to)
			}
		}
	})
}

// askDelete deletes ref once the user confirms. Nothing undoes a delete,
// so no button is the default: Return deletes nothing.
func (g *gui) askDelete(ref store.Ref) {
	// The window shows the entry asked about, but for another row's Delete…
	// beside a form, which names it.
	message := "Delete this entry?"
	if g.form.open && g.form.from != ref {
		message = fmt.Sprintf("Delete “%s” from %s?", ref.Name, ref.Namespace)
	}
	g.message(alert{message: message, detail: "You can’t undo this action.",
		buttons: []alertButton{{title: "Delete", destructive: true}, {title: "Cancel", isCancel: true}}}, func(button int) {
		if button == 0 {
			g.delete(ref)
		}
	})
}

func (g *gui) delete(ref store.Ref) {
	g.writeAction, g.writeRef = "delete", ref
	g.start(func() func() {
		err := g.store.Delete(g.ctx, ref)
		return func() {
			g.writeAction = ""
			if err != nil {
				g.writeFailed(false, "Delete", "Deleted", ref, err)
				return
			}
			if g.form.open && g.form.from == ref {
				g.closeForm()
			}
			if g.entry == ref {
				g.entry = store.Ref{}
				g.selectionChanged()
			}
			g.refresh()
		}
	})
}

// changeAccess grants or revokes ref's program access once the owner
// approves. The switch shows the change while it runs and the stored state
// again if the owner declines or the write fails.
func (g *gui) changeAccess(ref store.Ref, granted bool) {
	g.writeAction, g.writeRef, g.accessTo = "access", ref, granted
	g.start(func() func() {
		_, _, err := g.store.ChangeAccess(g.ctx, []store.Ref{ref}, granted, g.approve)
		return func() {
			g.writeAction = ""
			switch {
			case err == nil:
				attributes := g.attributes[ref]
				attributes.ProgramAccess = granted
				g.attributes[ref] = attributes
				g.refresh()
			case store.IsKind(err, store.KindCancelled):
			default:
				g.writeFailed(false, "Change Program Access for", "Changed", ref, err)
			}
		}
	})
}

// writeFailed reports a write's failure in an alert when its form is no
// longer open. A failed write may have changed the Keychain, so the lists
// reload either way.
func (g *gui) writeFailed(formOpen bool, verb, done string, ref store.Ref, err error) {
	if !formOpen {
		title := fmt.Sprintf("Couldn’t %s “%s”", verb, ref.Name)
		if store.IsKind(err, store.KindUncertain) {
			title = fmt.Sprintf("Couldn’t Confirm That “%s” Was %s", ref.Name, done)
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
				g.showAlert("Couldn’t Install the Command Line Tool", err)
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
