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

	"github.com/Entwining/maskey/internal/activity"
)

// Service is the service attribute (kSecAttrService) of every Maskey item and
// the app's identifier in mygo.json. Changing either orphans every stored
// credential on every synced Mac.
const Service = "io.github.entwining.maskey"

var (
	namespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
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

// ParseAccount is the reference an item's account names.
func ParseAccount(account string) (Ref, error) {
	namespace, name, ok := strings.Cut(account, "/")
	if !ok {
		return Ref{}, &Error{Kind: KindInvalid, Message: "invalid entry reference"}
	}
	ref := Ref{Namespace: namespace, Name: name}
	return ref, ValidateRef(ref)
}

// ValidateNamespace reports whether s can name a namespace. Its error does
// not quote s, which may be a value put where a namespace belongs.
func ValidateNamespace(s string) error {
	if !namespacePattern.MatchString(s) {
		return &Error{Kind: KindInvalid, Message: "a namespace starts with an ASCII letter or digit, followed by ASCII letters, digits, '.', '_' or '-'"}
	}
	return nil
}

// ValidateName reports whether s can name a credential. Names become
// environment variable names, so they follow the shell identifier rules.
// Its error does not quote s, as ValidateNamespace's does not.
func ValidateName(s string) error {
	if !namePattern.MatchString(s) {
		return &Error{Kind: KindInvalid, Message: "NAME starts with an ASCII letter or '_', followed by ASCII letters, digits or '_'"}
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

// Attributes are what a listing returns about an item besides its value.
// ProgramAccess permits programmatic retrieval.
type Attributes struct {
	Modified      time.Time `json:"modified"`
	ProgramAccess bool      `json:"program_access"`
	Details
}

// Item is an item's account and attributes, without its value.
type Item struct {
	Account string `json:"account"`
	Attributes
}

// Backend stores values under account strings. Read omits accounts that do
// not exist from its result; Items never loads values. Get returns the
// value of an item that has program access, which it checks in the same
// lookup, and fails with KindNotGranted for another. Add fails with
// KindExists, and Get, Update, SetProgramAccess and Delete with
// KindNotFound. Edit gives an item the account to, and the value and the
// details unless they are nil, in one write; it fails with KindNotFound
// when account does not exist and KindExists when to is another existing
// account. No write but SetProgramAccess changes program access.
type Backend interface {
	Items(ctx context.Context) ([]Item, error)
	Read(ctx context.Context, accounts []string) (map[string][]byte, error)
	Get(ctx context.Context, account string) ([]byte, error)
	Add(ctx context.Context, account string, value []byte, details Details) error
	Update(ctx context.Context, account string, value []byte) error
	Edit(ctx context.Context, account, to string, value []byte, details *Details) error
	SetProgramAccess(ctx context.Context, account string, granted bool) error
	Delete(ctx context.Context, account string) error
}

type Store struct {
	backend Backend
	// recorder receives the records of refused approvals, which reach no
	// backend operation; the backend records its own operations.
	recorder activity.Recorder
}

func New(backend Backend, recorder activity.Recorder) *Store {
	return &Store{backend: backend, recorder: recorder}
}

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
		if ref, err := ParseAccount(item.Account); err == nil {
			refs = append(refs, ref)
			attributes[ref] = item.Attributes
		}
	}
	// Namespaces differ by case, so ignoring it keeps GitHub beside github
	// rather than every capitalized namespace first; their bytes break ties.
	slices.SortFunc(refs, func(a, b Ref) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Namespace), strings.ToLower(b.Namespace)),
			strings.Compare(a.Namespace, b.Namespace),
			strings.Compare(a.Name, b.Name))
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
	err := s.backend.Add(ctx, ref.account(), value, details)
	if IsKind(err, KindExists) {
		return existing(err, ref)
	}
	return err
}

// Edit gives the credential at from the ref to and, unless they are nil,
// that value and those details, in one write, so that a failure leaves it
// as it was. It fails with KindNotFound when from
// does not exist and KindExists when to is another existing credential.
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
		return existing(err, to)
	}
	return err
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
		return err == nil, err
	}
	err = s.backend.Update(ctx, ref.account(), value)
	if IsKind(err, KindNotFound) {
		return false, &Error{Kind: KindConflict, Ref: &ref, Message: "the entry was deleted while it was being replaced"}
	}
	return false, existing(err, ref)
}

// Delete removes one credential and fails with KindNotFound if it does not
// exist.
func (s *Store) Delete(ctx context.Context, ref Ref) error {
	if err := ValidateRef(ref); err != nil {
		return err
	}
	return s.backend.Delete(ctx, ref.account())
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

// GrantedValue reads the value of a credential the owner granted program
// access. The grant and the value come from one lookup, so a credential
// deleted and created again under the same reference cannot inherit an
// earlier grant.
func (s *Store) GrantedValue(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ValidateRef(ref); err != nil {
		return nil, err
	}
	value, err := s.backend.Get(ctx, ref.account())
	if IsKind(err, KindNotGranted) {
		return nil, existing(err, ref)
	}
	return value, err
}

// Approve asks the owner to approve a change to program access, giving
// reason, a verb phrase such as "grant program access to github/TOKEN". It
// returns how the owner approved it, which the activity record keeps.
type Approve func(ctx context.Context, reason string) (string, error)

// AccessChange is a credential a grant or revoke concerned and whether it
// changed; one already in the requested state did not.
type AccessChange struct {
	Ref
	Changed bool `json:"changed"`
}

// ChangeAccess validates all refs before requesting one approval for the
// changes needed. A batch already in the requested state asks nothing.
// Results follow input order and include unchanged entries. A write failure
// returns the completed prefix; validation or approval failure returns none,
// and a refused approval is recorded for each change it concerned.
func (s *Store) ChangeAccess(ctx context.Context, refs []Ref, granted bool, approve Approve) ([]AccessChange, string, error) {
	for _, ref := range refs {
		if err := ValidateRef(ref); err != nil {
			return nil, "", err
		}
	}
	_, attributes, err := s.list(ctx)
	if err != nil {
		return nil, "", err
	}
	var pending []Ref
	for i, ref := range refs {
		if _, ok := attributes[ref]; !ok {
			return nil, "", &Error{Kind: KindNotFound, Argument: i + 1, Message: "the entry does not exist"}
		}
		if attributes[ref].ProgramAccess != granted && !slices.Contains(pending, ref) {
			pending = append(pending, ref)
		}
	}
	var auth string
	if len(pending) > 0 {
		if auth, err = approve(ctx, accessReason(pending, granted)); err != nil {
			s.recordRefusal(ctx, pending, granted, err)
			return nil, "", err
		}
		ctx = activity.WithAuth(ctx, auth)
	}
	changes := make([]AccessChange, 0, len(refs))
	for _, ref := range refs {
		a := attributes[ref]
		changed := a.ProgramAccess != granted
		if changed {
			if err := s.backend.SetProgramAccess(ctx, ref.account(), granted); err != nil {
				return changes, auth, existing(err, ref)
			}
			a.ProgramAccess = granted
			attributes[ref] = a
		}
		changes = append(changes, AccessChange{Ref: ref, Changed: changed})
	}
	return changes, auth, nil
}

// recordRefusal records each change the owner did not approve, with how
// the approval ended.
func (s *Store) recordRefusal(ctx context.Context, refs []Ref, granted bool, err error) {
	op := "revoke"
	if granted {
		op = "grant"
	}
	records := make([]activity.Record, len(refs))
	for i, ref := range refs {
		records[i] = activity.Record{Op: op, Entry: ref.String(), Result: string(KindOf(err))}
	}
	s.recorder.Record(ctx, records...)
}

// Limit the entry list so approval prompts stay readable.
func accessReason(refs []Ref, granted bool) string {
	verb := "revoke"
	if granted {
		verb = "grant"
	}
	names := make([]string, 0, 2)
	for _, ref := range refs[:min(len(refs), 2)] {
		names = append(names, ref.String())
	}
	switch more := len(refs) - len(names); {
	case more > 0:
		return fmt.Sprintf("%s program access to %s and %d more", verb, strings.Join(names, ", "), more)
	case len(names) == 2:
		return fmt.Sprintf("%s program access to %s and %s", verb, names[0], names[1])
	default:
		return fmt.Sprintf("%s program access to %s", verb, names[0])
	}
}

// Values reads the credentials that selections name: each is an entry, or
// with an empty Name every entry of a namespace. An entry replaces the
// same-named entries of namespaces, as an explicit import shadows a glob
// import. A namespace without entries, a missing entry, and a name that two
// namespaces or two entries supply are errors: none may silently change what
// a command receives. A missing selection is reported by its position.
func (s *Store) Values(ctx context.Context, selections []Ref) ([]Entry, error) {
	all, _, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i, selection := range selections {
		switch {
		case selection.Name == "" && !slices.ContainsFunc(all, func(ref Ref) bool { return ref.Namespace == selection.Namespace }):
			return nil, &Error{Kind: KindNotFound, Argument: i + 1, Message: "the namespace has no entries"}
		case selection.Name != "" && !slices.Contains(all, selection):
			return nil, &Error{Kind: KindNotFound, Argument: i + 1, Message: "the entry does not exist"}
		}
	}
	var selected []Ref
	explicit := map[string]Ref{}
	for _, ref := range selections {
		if ref.Name == "" {
			continue
		}
		other, ok := explicit[ref.Name]
		switch {
		case !ok:
			explicit[ref.Name] = ref
			selected = append(selected, ref)
		case other != ref:
			return nil, &Error{Kind: KindConflict, Message: fmt.Sprintf("%s and %s both set %s", other, ref, ref.Name)}
		}
	}
	supplier := map[string]string{}
	for _, ref := range all {
		if !slices.Contains(selections, Ref{Namespace: ref.Namespace}) {
			continue
		}
		if _, ok := explicit[ref.Name]; ok {
			continue
		}
		if other, ok := supplier[ref.Name]; ok {
			return nil, &Error{Kind: KindConflict, Message: fmt.Sprintf("%s and %s both supply %s", other, ref.Namespace, ref.Name)}
		}
		supplier[ref.Name] = ref.Namespace
		selected = append(selected, ref)
	}
	return s.read(ctx, selected)
}

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

// existing attaches ref to a backend error about a credential the backend
// has shown to exist. Errors name no other reference: one that was never
// stored may be a value typed in the wrong place.
func existing(err error, ref Ref) error {
	var e *Error
	if errors.As(err, &e) && e.Ref == nil {
		named := *e
		named.Ref = &ref
		return &named
	}
	return err
}
