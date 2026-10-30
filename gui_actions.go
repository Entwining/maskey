package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
)

// read loads the chosen entry's value and passes it to use unless another
// entry was chosen meanwhile.
func (g *gui) read(action string, use func(value []byte)) {
	ref := g.entry
	token := g.selToken
	g.readAction = action
	g.start(func() func() {
		value, err := g.store.Value(context.Background(), ref)
		return func() {
			if token != g.selToken {
				return
			}
			g.readAction = ""
			if err != nil {
				g.showAlert(fmt.Sprintf("Couldn’t read “%s”", ref.Name), err)
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
	g.read("reveal", func(value []byte) { g.revealed = string(value) })
}

func (g *gui) copy() {
	services := g.services
	g.copied = false
	g.read("copy", func(value []byte) {
		services.WriteClipboard(string(value))
		g.copied, g.copiedUntil = true, time.Time{}
	})
}

// openNew opens the form for a new entry in the namespace chosen.
func (g *gui) openNew() {
	if g.form.open {
		return
	}
	namespace := g.namespace
	if namespace == allNamespaces {
		namespace = ""
	}
	g.selectionChanged()
	g.form = form{open: true, id: g.form.id + 1, namespace: namespace, kind: kindLabel(store.InferValueKind("")), inferredFrom: new(string)}
}

// openEdit opens the form for the chosen entry and loads the value it
// starts from.
func (g *gui) openEdit() {
	if g.form.open || g.entry == (store.Ref{}) || g.writing(g.entry) {
		return
	}
	ref := g.entry
	g.selectionChanged()
	details := g.attributes[ref].Details
	kind := kindLabel(store.ValueKindOf(ref.Name, details.Kind))
	g.form = form{open: true, id: g.form.id + 1, from: ref, namespace: ref.Namespace, name: ref.Name,
		kind: kind, kindFrom: kind, notes: details.Notes, notesFrom: details.Notes, loading: true}
	id := g.form.id
	g.start(func() func() {
		value, err := g.store.Value(context.Background(), ref)
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

func (g *gui) closeForm() {
	g.form = form{id: g.form.id}
}

// submit creates the form's entry, or gives the edited one the form's
// namespace, name, value and details in one write.
func (g *gui) submit() {
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
	verb, done, target := "save", "saved", from
	if from == (store.Ref{}) {
		verb, done, target = "add", "added", to
	}
	g.writeAction, g.writeRef, g.writeForm = "submit", target, id
	g.start(func() func() {
		var err error
		if from == (store.Ref{}) {
			err = g.store.Create(context.Background(), to, value, details)
		} else {
			err = g.store.Edit(context.Background(), from, to, value, editedDetails)
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
			g.follow(to)
		}
	})
}

func (g *gui) askDelete(ref store.Ref) {
	g.confirmDelete, g.deleteRef = true, ref
}

func (g *gui) delete(ref store.Ref) {
	g.writeAction, g.writeRef = "delete", ref
	g.start(func() func() {
		err := g.store.Delete(context.Background(), ref)
		return func() {
			g.writeAction = ""
			if err != nil {
				g.writeFailed(false, "delete", "deleted", ref, err)
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

// writeFailed reports a write's failure in an alert when its form is no
// longer open. A failed write may have changed the Keychain, so the lists
// reload either way.
func (g *gui) writeFailed(formOpen bool, verb, done string, ref store.Ref, err error) {
	if !formOpen {
		title := fmt.Sprintf("Couldn’t %s “%s”", verb, ref.Name)
		if store.IsKind(err, store.KindUncertain) {
			title = fmt.Sprintf("Couldn’t confirm that “%s” was %s", ref.Name, done)
		}
		g.showAlert(title, err)
	}
	g.refresh()
}

func (g *gui) installCLI() {
	g.cliOpen = true
	if g.cliInstalling {
		return
	}
	g.cliInstalling = true
	g.cliTitle, g.cliMessage = "Installing the Command Line Tool…", "Linking mk and starting its Keychain service."
	g.start(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		executable, err := os.Executable()
		var home, link string
		if err == nil {
			home, err = os.UserHomeDir()
		}
		if err == nil {
			link, err = installCLI(home, executable)
		}
		if err == nil {
			err = agent.Install(ctx, executable)
		}
		return func() {
			g.cliInstalling, g.cliOpen = false, true
			if err != nil {
				g.cliTitle, g.cliMessage = "Couldn’t Install the Command Line Tool", sentence(err)
				return
			}
			dir := filepath.Dir(link)
			if rest, ok := strings.CutPrefix(dir, home); ok {
				dir = "~" + rest
			}
			g.cliTitle = "Command Line Tool Installed"
			g.cliMessage = fmt.Sprintf("mk is in %s and its Keychain service is running. If your shell can’t find mk, add %s to PATH.", dir, dir)
		}
	})
}

// installCLI links home/.local/bin/mk to the app's executable, so upgrading
// the app in place upgrades mk too. It replaces an earlier link but never a
// file.
func installCLI(home, executable string) (string, error) {
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(dir, "mk")
	info, err := os.Lstat(link)
	switch {
	case err == nil && info.Mode()&fs.ModeSymlink == 0:
		return "", fmt.Errorf("%s exists and is not a symbolic link", link)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	// Symlink fails rather than replace a file already at this name, so
	// only a link made here is ever removed.
	temporary := fmt.Sprintf("%s.maskey-new-%d", link, os.Getpid())
	if err := os.Symlink(target, temporary); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, link); err != nil {
		os.Remove(temporary)
		return "", err
	}
	return link, nil
}
