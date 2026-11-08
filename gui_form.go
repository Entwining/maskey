package main

import "github.com/Entwining/maskey/internal/store"

// form is the detail pane's editor, of a new entry or of the entry from.
type form struct {
	open bool
	id   int
	from store.Ref // zero for a new entry
	// chosenBefore is the entry chosen before a new entry's form opened,
	// chosen again when it closes.
	chosenBefore store.Ref
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
	// inferredFrom is the name the kind was inferred from, while the kind
	// follows the name: in a new entry's form, until the user picks one.
	inferredFrom *string
	loading      bool
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
// form, are its kind and the namespace it opened with.
func (f *form) dirty() bool {
	if f.from == (store.Ref{}) {
		return f.namespace != f.namespaceFrom || f.name != "" || f.value != "" || f.notes != ""
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
