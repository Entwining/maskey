// Package store holds the credential operations the GUI and the CLI share.
//
// Each credential is one item in the backend, keyed by an account string
// that encodes its namespace and name. Namespaces exist only as the set of
// namespaces that currently have entries, so there is no index to keep in
// sync across devices.
package store

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Service is the service attribute (kSecAttrService) of every Maskey item and
// the app's identifier in mygo.json. Changing either orphans every stored
// credential on every synced Mac.
const Service = "io.github.entwining.maskey"

var (
	namespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	namePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Ref identifies one credential.
type Ref struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// account encodes r for the backend. Namespaces cannot contain "/", so the
// first "/" always separates the two parts and distinct refs never share an
// account.
func (r Ref) account() string { return r.Namespace + "/" + r.Name }

func (r Ref) String() string { return r.account() }

func parseAccount(account string) (Ref, bool) {
	namespace, name, ok := strings.Cut(account, "/")
	ref := Ref{Namespace: namespace, Name: name}
	return ref, ok && ValidateRef(ref) == nil
}

// ValidateNamespace reports whether s can name a namespace. Its error does
// not quote s, which may be a value put where a namespace belongs.
func ValidateNamespace(s string) error {
	if !namespacePattern.MatchString(s) {
		return &Error{Kind: KindInvalid, Message: "invalid namespace: use lowercase letters, digits, '.', '_' and '-', starting with a letter or digit"}
	}
	return nil
}

// ValidateName reports whether s can name a credential. Names become
// environment variable names, so they follow the shell identifier rules.
// Its error does not quote s, as ValidateNamespace's does not.
func ValidateName(s string) error {
	if !namePattern.MatchString(s) {
		return &Error{Kind: KindInvalid, Message: "invalid name: use letters, digits and '_', not starting with a digit"}
	}
	return nil
}

func ValidateRef(r Ref) error {
	if err := ValidateNamespace(r.Namespace); err != nil {
		return err
	}
	return ValidateName(r.Name)
}

// ValidateValue reports whether v can be stored. Values are kept byte for
// byte, including surrounding whitespace and newlines; an environment
// variable cannot hold an empty value distinctly from an unset one in every
// tool, nor a NUL byte at all.
func ValidateValue(v []byte) error {
	if len(v) == 0 {
		return &Error{Kind: KindInvalid, Message: "the value is empty"}
	}
	if bytes.IndexByte(v, 0) >= 0 {
		return &Error{Kind: KindInvalid, Message: "the value contains a NUL byte"}
	}
	return nil
}

// Details are what a person sets on an entry besides its value.
type Details struct {
	Kind  ValueKind `json:"kind,omitempty"`
	Notes string    `json:"notes,omitempty"`
}

// Attributes are what the backend keeps about an item besides its value,
// which a listing returns.
type Attributes struct {
	Modified time.Time `json:"modified"`
	Details
}

// Item is an item's account and attributes, without its value.
type Item struct {
	Account string `json:"account"`
	Attributes
}

// Backend stores values under account strings. Read omits accounts that do
// not exist from its result; Items never loads values. Add fails with
// KindExists, and Update and Delete with KindNotFound. Edit gives an item
// the account to, and the value and the details unless they are nil, in one
// write; it fails with KindNotFound when account does not exist and
// KindExists when to is another existing account.
type Backend interface {
	Items(ctx context.Context) ([]Item, error)
	Read(ctx context.Context, accounts []string) (map[string][]byte, error)
	Add(ctx context.Context, account string, value []byte, details Details) error
	Update(ctx context.Context, account string, value []byte) error
	Edit(ctx context.Context, account, to string, value []byte, details *Details) error
	Delete(ctx context.Context, account string) error
}

type Store struct {
	backend Backend
}

func New(backend Backend) *Store { return &Store{backend: backend} }

// Namespace describes a namespace that has entries.
type Namespace struct {
	Name    string `json:"name"`
	Entries int    `json:"entries"`
}

func (s *Store) list(ctx context.Context) ([]Ref, map[Ref]Attributes, error) {
	items, err := s.backend.Items(ctx)
	if err != nil {
		return nil, nil, err
	}
	refs := make([]Ref, 0, len(items))
	attributes := make(map[Ref]Attributes, len(items))
	for _, item := range items {
		// Another script hosted by the same interpreter can write to this
		// service; items outside the encoding are not Maskey's.
		if ref, ok := parseAccount(item.Account); ok {
			refs = append(refs, ref)
			attributes[ref] = item.Attributes
		}
	}
	slices.SortFunc(refs, func(a, b Ref) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Name, b.Name))
	})
	return refs, attributes, nil
}

// Namespaces lists the namespaces that have entries, sorted by name.
func (s *Store) Namespaces(ctx context.Context) ([]Namespace, error) {
	refs, _, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	return namespacesOf(refs), nil
}

// Listing returns what Namespaces returns, every entry's ref, sorted by
// namespace and name, and their attributes, from one listing of the
// backend.
func (s *Store) Listing(ctx context.Context) ([]Namespace, []Ref, map[Ref]Attributes, error) {
	refs, attributes, err := s.list(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	return namespacesOf(refs), refs, attributes, nil
}

func namespacesOf(refs []Ref) []Namespace {
	namespaces := []Namespace{}
	for _, ref := range refs {
		if n := len(namespaces); n > 0 && namespaces[n-1].Name == ref.Namespace {
			namespaces[n-1].Entries++
		} else {
			namespaces = append(namespaces, Namespace{Name: ref.Namespace, Entries: 1})
		}
	}
	return namespaces
}

// Create adds a credential with its details and fails with KindExists if it
// already exists.
func (s *Store) Create(ctx context.Context, ref Ref, value []byte, details Details) error {
	if err := validateEntry(ref, value); err != nil {
		return err
	}
	return withRef(s.backend.Add(ctx, ref.account(), value, details), ref)
}

// Edit gives the credential at from the ref to and, unless they are nil,
// that value and those details, in one write, so that a failure leaves it as
// it was. It fails with KindNotFound when from does not exist and
// KindExists when to is another existing credential.
func (s *Store) Edit(ctx context.Context, from, to Ref, value []byte, details *Details) error {
	if err := ValidateRef(from); err != nil {
		return err
	}
	if err := ValidateRef(to); err != nil {
		return err
	}
	if value != nil {
		if err := ValidateValue(value); err != nil {
			return err
		}
	}
	if from == to && value == nil && details == nil {
		return nil
	}
	err := s.backend.Edit(ctx, from.account(), to.account(), value, details)
	if IsKind(err, KindExists) {
		return withRef(err, to)
	}
	return withRef(err, from)
}

// Set creates the credential, of the kind its name suggests, or replaces
// its value, reporting whether it created it. When another writer deletes
// the credential between the two steps, Set fails with KindConflict
// instead of guessing which write should win.
func (s *Store) Set(ctx context.Context, ref Ref, value []byte) (created bool, err error) {
	if err := validateEntry(ref, value); err != nil {
		return false, err
	}
	err = s.backend.Add(ctx, ref.account(), value, Details{Kind: InferValueKind(ref.Name)})
	if !IsKind(err, KindExists) {
		return err == nil, withRef(err, ref)
	}
	err = s.backend.Update(ctx, ref.account(), value)
	if IsKind(err, KindNotFound) {
		return false, &Error{Kind: KindConflict, Ref: &ref, Message: "the entry was deleted while it was being replaced"}
	}
	return false, withRef(err, ref)
}

// Delete removes one credential and fails with KindNotFound if it does not
// exist.
func (s *Store) Delete(ctx context.Context, ref Ref) error {
	if err := ValidateRef(ref); err != nil {
		return err
	}
	return withRef(s.backend.Delete(ctx, ref.account()), ref)
}

// Entry is a credential with its value.
type Entry struct {
	Ref
	Value []byte
}

// Value reads the value of one credential.
func (s *Store) Value(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ValidateRef(ref); err != nil {
		return nil, err
	}
	entries, err := s.read(ctx, []Ref{ref})
	if err != nil {
		return nil, err
	}
	return entries[0].Value, nil
}

// Values reads every credential of the namespaces, and the credentials refs
// name. A namespace without entries or a missing entry is an error, and so is
// a name selected from more than one namespace: neither may silently change
// what a command receives.
func (s *Store) Values(ctx context.Context, namespaces []string, refs []Ref) ([]Entry, error) {
	if len(namespaces) == 0 && len(refs) == 0 {
		return nil, &Error{Kind: KindInvalid, Message: "no namespace or entry selected"}
	}
	for _, namespace := range namespaces {
		if err := ValidateNamespace(namespace); err != nil {
			return nil, err
		}
	}
	for _, ref := range refs {
		if err := ValidateRef(ref); err != nil {
			return nil, err
		}
	}
	all, _, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	var selected []Ref
	owner := map[string]string{}
	add := func(ref Ref) error {
		other, ok := owner[ref.Name]
		switch {
		case !ok:
			owner[ref.Name] = ref.Namespace
			selected = append(selected, ref)
		case other != ref.Namespace:
			return &Error{Kind: KindConflict, Message: fmt.Sprintf("%s is selected from both namespace %q and namespace %q", ref.Name, other, ref.Namespace)}
		}
		return nil
	}
	for _, namespace := range namespaces {
		found := false
		for _, ref := range all {
			if ref.Namespace != namespace {
				continue
			}
			found = true
			if err := add(ref); err != nil {
				return nil, err
			}
		}
		if !found {
			return nil, &Error{Kind: KindNotFound, Message: fmt.Sprintf("namespace %q has no entries", namespace)}
		}
	}
	for _, ref := range refs {
		if !slices.Contains(all, ref) {
			return nil, &Error{Kind: KindNotFound, Ref: &ref, Message: "the entry does not exist"}
		}
		if err := add(ref); err != nil {
			return nil, err
		}
	}
	return s.read(ctx, selected)
}

// read is the one path that loads values from the backend.
func (s *Store) read(ctx context.Context, refs []Ref) ([]Entry, error) {
	accounts := make([]string, len(refs))
	for i, ref := range refs {
		accounts[i] = ref.account()
	}
	values, err := s.backend.Read(ctx, accounts)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, len(refs))
	for i, ref := range refs {
		value, ok := values[ref.account()]
		if !ok {
			return nil, &Error{Kind: KindNotFound, Ref: &ref, Message: "the entry does not exist"}
		}
		entries[i] = Entry{Ref: ref, Value: value}
	}
	return entries, nil
}

// validateEntry checks a reference and a value as a write needs them.
func validateEntry(ref Ref, value []byte) error {
	if err := ValidateRef(ref); err != nil {
		return err
	}
	return ValidateValue(value)
}

// withRef attaches ref to a backend error that does not name its entry.
func withRef(err error, ref Ref) error {
	var e *Error
	if errors.As(err, &e) && e.Ref == nil {
		named := *e
		named.Ref = &ref
		return &named
	}
	return err
}
