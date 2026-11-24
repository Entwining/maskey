package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Entwining/maskey/internal/store"
)

// form is the detail pane's editor, of a new entry or of the entry from.
type form struct {
	open bool
	id   int
	from entryID // zero for a new entry
	// store is the store the form writes to: from's, or the one chosen for
	// a new entry.
	store string
	// chosenBefore is the entry chosen before a new entry's form opened,
	// chosen again when it closes.
	chosenBefore entryID
	// namespaceFrom is the namespace a new entry's form opened with: the
	// one chosen in the sidebar, or none.
	namespaceFrom string
	namespace     string
	name          string
	value         string
	original      string // the stored value, once loaded
	kind          string // a label of kinds
	notes         string
	// kindFrom and notesFrom are what the form showed when it opened; the
	// form writes the details only when they differ.
	kindFrom  string
	notesFrom string
	// inferred is the kind the name gave, while the kind follows the name:
	// in a new entry's form, until the user picks another; "" otherwise.
	inferred string
	loading  bool
	// keepValue is set when the stored value holds carriage returns: the
	// text area shows them as loaded, but a line break typed or pasted in
	// it is LF, so an edit would mix line endings. The entry can move, but
	// its value stays.
	keepValue bool
	// keptKey is sshagent.Key's verdict on the value kept, which the form
	// does not hold, for when SSH Key is chosen for it.
	keptKey error
	err     formError
}

// ready reports whether submitting the form would write anything.
func (f *form) ready() bool {
	if f.from == (entryID{}) {
		return f.namespace != "" && f.name != "" && f.value != ""
	}
	if f.loading || f.original == "" && !f.keepValue {
		return false
	}
	return f.namespace != f.from.ref.Namespace || f.name != f.from.ref.Name || !f.keepValue && f.value != f.original || f.detailsChanged()
}

func (f *form) detailsChanged() bool { return f.kind != f.kindFrom || f.notes != f.notesFrom }

// dirty reports whether the form holds anything the user typed. A value
// still loading or kept as stored is not, and neither, in a new entry's
// form, are its kind and the namespace it opened with.
func (f *form) dirty() bool {
	if f.from == (entryID{}) {
		return f.namespace != f.namespaceFrom || f.name != "" || f.value != "" || f.notes != ""
	}
	return f.namespace != f.from.ref.Namespace || f.name != f.from.ref.Name || f.value != f.original || f.detailsChanged()
}

// followName infers the kind from the name, unless the user picked a kind
// other than the one inferred.
func (f *form) followName() {
	if f.inferred == "" || f.kind != f.inferred {
		f.inferred = ""
		return
	}
	f.kind = kindLabel(store.InferValueKind(f.name))
	f.inferred = f.kind
}

// terminalRef is the entry that a value given in Terminal goes to: the
// entry edited, or the new one as typed so far. A placeholder stands for
// what is not a valid namespace or name yet, and for a name the store may
// already hold, which mk set would replace where Add refuses: one its
// listing shows, or any before a listing of it has been read.
func (g *gui) terminalRef() store.Ref {
	f := &g.form
	if f.from != (entryID{}) {
		return f.from.ref
	}
	ref := store.Ref{Namespace: "<namespace>", Name: "<NAME>"}
	if store.ValidateNamespace(f.namespace) == nil {
		ref.Namespace = f.namespace
	}
	typed := store.Ref{Namespace: f.namespace, Name: f.name}
	if s := g.listed[f.store]; s != nil && s.read && store.ValidateName(f.name) == nil && !g.has(entryID{f.store, typed}) {
		ref.Name = f.name
	}
	return ref
}

// terminalTarget is the entry a command for Terminal names, and what it
// says after the command, so that mk set leaves an entry of the kind the
// form shows. mk set keeps an existing entry's kind and gives a new one the
// kind its name infers. Where that differs, an SSH key goes to a new entry
// whose name gives it its kind, since only there does mk set remove a
// passphrase and mk run lend the key instead of putting it in the
// environment; another kind is chosen in the window afterwards.
func (g *gui) terminalTarget() (store.Ref, string) {
	f := &g.form
	ref, kind := g.terminalRef(), kindOf(f.kind)
	switch {
	case store.ValueKindOf(ref.Name, g.attributes(f.from).Kind) == kind:
		return ref, ""
	case kind == store.ValueSSH:
		ref.Name = "<NAME>_SSH_KEY"
		return ref, ""
	}
	return ref, " Then set its Kind to " + f.kind + " in this window."
}

// linesHint says how Terminal stores a value whose line endings this
// editor would change. For an SSH Key entry mk set can remove a passphrase,
// so it promises the line endings, not every byte.
func (g *gui) linesHint() string {
	ref, then := g.terminalTarget()
	return "In Terminal, " + g.form.setCommand(ref) + " < <file> keeps the line endings of a value of several lines." + then
}

// setCommand is the command that gives ref, in the form's store, a value the
// window cannot store as given. Without --store it would write to the
// default store.
func (f *form) setCommand(ref store.Ref) string {
	return fmt.Sprintf("mk set --store %s --stdin %s", f.store, ref)
}

// caseNote says, under a new entry's Name, that the entry would stand beside
// entries of its store whose references differ from its own only in case,
// as mk set warns. It does not keep the entry from being added.
func (g *gui) caseNote() string {
	f := &g.form
	s := g.listed[f.store]
	ref := store.Ref{Namespace: f.namespace, Name: f.name}
	if f.from != (entryID{}) || s == nil || slices.Contains(s.refs, ref) {
		return ""
	}
	// The comparison mk set makes before it warns.
	var twins []string
	for _, twin := range store.CaseTwins(ref, s.refs) {
		twins = append(twins, twin.String())
	}
	if len(twins) == 0 {
		return ""
	}
	return "This creates a separate entry from " + strings.Join(twins, " and ") + "."
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
