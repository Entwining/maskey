package store_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

var ctx = context.Background()

func ref(namespace, name string) store.Ref { return store.Ref{Namespace: namespace, Name: name} }

func TestIdentifierRules(t *testing.T) {
	for _, namespace := range []string{"github", "GitHub", "aws.prod", "team_1", "a-b", "0"} {
		if err := store.ValidateNamespace(namespace); err != nil {
			t.Errorf("namespace %q rejected: %v", namespace, err)
		}
	}
	for _, namespace := range []string{"", "a/b", "-a", ".a", "a b", "a\x00"} {
		if store.KindOf(store.ValidateNamespace(namespace)) != store.KindInvalid {
			t.Errorf("namespace %q accepted", namespace)
		}
	}
	for _, name := range []string{"GH_TOKEN", "_x", "a1"} {
		if err := store.ValidateName(name); err != nil {
			t.Errorf("name %q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "1A", "A-B", "A=B", "A/B", "A B"} {
		if store.KindOf(store.ValidateName(name)) != store.KindInvalid {
			t.Errorf("name %q accepted", name)
		}
	}
	for _, value := range []string{"", "a\x00b"} {
		if store.KindOf(store.ValidateValue([]byte(value))) != store.KindInvalid {
			t.Errorf("value %q accepted", value)
		}
	}
	// A value put where a namespace or name belongs stays out of the error.
	misplaced := "synthetic-value=7f3a"
	for _, err := range []error{store.ValidateNamespace(misplaced), store.ValidateName("TOKEN=" + misplaced)} {
		if err == nil || strings.Contains(err.Error(), misplaced) {
			t.Errorf("error %v quotes the rejected input", err)
		}
	}
}

// Similar refs must stay distinct entries that list under their own
// namespace and keep their own values.
func TestSimilarRefsDoNotCollide(t *testing.T) {
	s := store.New(storetest.New(), nil)
	refs := []store.Ref{
		ref("a", "b_c"), ref("a.b", "c"), ref("a_b", "c"), ref("a", "B_C"),
		ref("ab", "c"), ref("a", "bc"), ref("a-b", "c"),
	}
	for i, r := range refs {
		if _, err := s.Set(ctx, r, []byte{byte('A' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range refs {
		value, err := s.Value(ctx, r)
		if err != nil || string(value) != string(rune('A'+i)) {
			t.Errorf("%v: value %q, error %v", r, value, err)
		}
	}
	_, listed, _, err := s.Listing(ctx)
	if err != nil || !slices.Equal(listed[:3], []store.Ref{ref("a", "B_C"), ref("a", "b_c"), ref("a", "bc")}) {
		t.Errorf("listed %v, error %v", listed, err)
	}
	namespaces, err := s.Namespaces(ctx)
	want := []store.Namespace{{"a", 3}, {"a-b", 1}, {"a.b", 1}, {"a_b", 1}, {"ab", 1}}
	if err != nil || !slices.Equal(namespaces, want) {
		t.Errorf("namespaces %v, error %v", namespaces, err)
	}
}

func TestValuesKeptByteForByte(t *testing.T) {
	s := store.New(storetest.New(), nil)
	for _, value := range []string{" ", "\n", " padded\t", "line1\nline2\n", "\xff\xfe"} {
		r := ref("ns", "V")
		if _, err := s.Set(ctx, r, []byte(value)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Value(ctx, r)
		if err != nil || string(got) != value {
			t.Errorf("stored %q, read %q, error %v", value, got, err)
		}
	}
}

func TestListingReadsNoValues(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	s.Set(ctx, ref("ns", "A"), []byte("v"))
	if _, err := s.Namespaces(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.Listing(ctx); err != nil {
		t.Fatal(err)
	}
	if len(backend.Reads) != 0 {
		t.Fatalf("listing read values of %q", backend.Reads)
	}
}

// Namespaces sort ignoring case, with their bytes breaking ties before
// names do; names sort by their bytes.
func TestListingOrder(t *testing.T) {
	backend := storetest.New()
	for _, account := range []string{"Zeta/A", "github/A", "app/a", "Work/A", "GitHub/b", "app/B"} {
		backend.Put(account, []byte("v"))
	}
	_, refs, _, err := store.New(backend, nil).Listing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Ref{ref("app", "B"), ref("app", "a"), ref("GitHub", "b"), ref("github", "A"), ref("Work", "A"), ref("Zeta", "A")}
	if !slices.Equal(refs, want) {
		t.Fatalf("refs %v, want %v", refs, want)
	}
}

func TestForeignAccountsIgnored(t *testing.T) {
	backend := storetest.New()
	backend.Put("not-maskey", []byte("x"))
	backend.Put("-ns/NAME", []byte("x"))
	backend.Put("ns/bad-name", []byte("x"))
	backend.Put("ns/GOOD", []byte("x"))
	namespaces, err := store.New(backend, nil).Namespaces(ctx)
	if err != nil || !slices.Equal(namespaces, []store.Namespace{{"ns", 1}}) {
		t.Fatalf("namespaces %v, error %v", namespaces, err)
	}
}

func TestWriteOutcomes(t *testing.T) {
	s := store.New(storetest.New(), nil)
	r := ref("ns", "A")
	if created, err := s.Set(ctx, r, []byte("1")); err != nil || !created {
		t.Fatalf("first Set: created %v, error %v", created, err)
	}
	if created, err := s.Set(ctx, r, []byte("2")); err != nil || created {
		t.Fatalf("second Set: created %v, error %v", created, err)
	}
	err := s.Create(ctx, r, []byte("3"), store.Details{})
	if e, ok := err.(*store.Error); !ok || e.Kind != store.KindExists || e.Ref == nil || *e.Ref != r {
		t.Fatalf("Create over an entry: %v, want exists naming %v", err, r)
	}
	if err := s.Delete(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, r); store.KindOf(err) != store.KindNotFound {
		t.Errorf("Delete of a deleted entry: %v, want not found", err)
	}
	if _, err := s.Value(ctx, r); store.KindOf(err) != store.KindNotFound {
		t.Errorf("Value of a deleted entry: %v, want not found", err)
	}
}

// Edit renames an entry, moves it to another namespace, changes its value,
// or does both at once; an entry in the way fails it naming the target.
func TestEdit(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	a, b, c := ref("ns", "A"), ref("ns", "B"), ref("other", "B")
	s.Set(ctx, a, []byte("1"))
	s.Set(ctx, c, []byte("in the way"))
	steps := []struct {
		from, to store.Ref
		value    []byte
		want     string
	}{
		{a, b, nil, "1"},
		{b, b, []byte("2"), "2"},
		{b, a, []byte("3"), "3"},
	}
	for _, step := range steps {
		if err := s.Edit(ctx, step.from, step.to, step.value, nil); err != nil {
			t.Fatalf("Edit %v to %v: %v", step.from, step.to, err)
		}
		if v, err := s.Value(ctx, step.to); err != nil || string(v) != step.want {
			t.Fatalf("after editing %v to %v: %q, %v", step.from, step.to, v, err)
		}
		if _, err := s.Value(ctx, step.from); step.from != step.to && store.KindOf(err) != store.KindNotFound {
			t.Fatalf("%v remains after the move: %v", step.from, err)
		}
	}
	err := s.Edit(ctx, a, c, nil, nil)
	if e, ok := err.(*store.Error); !ok || e.Kind != store.KindExists || e.Ref == nil || *e.Ref != c {
		t.Fatalf("Edit onto an entry: %v, want exists naming %v", err, c)
	}
	if v, _ := s.Value(ctx, c); string(v) != "in the way" {
		t.Fatalf("the entry in the way holds %q", v)
	}
	if err := s.Edit(ctx, b, c, nil, nil); store.KindOf(err) != store.KindNotFound {
		t.Fatalf("Edit of a missing entry: %v, want not found", err)
	}
}

// Details are created with an entry, change with Edit alone, survive a
// rename and a replaced value, and come back with the listing.
func TestDetails(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	a, b := ref("ns", "A"), ref("ns", "B")
	details := func(r store.Ref) store.Details {
		t.Helper()
		_, refs, attributes, err := s.Listing(ctx)
		if err != nil || !slices.Contains(refs, r) {
			t.Fatalf("listing %v lacks %v: %v", refs, r, err)
		}
		return attributes[r].Details
	}
	first := store.Details{Kind: store.ValuePEM, Notes: "first"}
	if err := s.Create(ctx, a, []byte("1"), first); err != nil {
		t.Fatal(err)
	}
	if got := details(a); got != first {
		t.Fatalf("created with details %+v", got)
	}
	second := store.Details{Kind: store.ValueSetting, Notes: "second"}
	if err := s.Edit(ctx, a, a, nil, &second); err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(ctx, a, b, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, b, []byte("2")); err != nil {
		t.Fatal(err)
	}
	if got := details(b); got != second {
		t.Fatalf("details %+v after the rename and Set, want %+v", got, second)
	}
	if v, _ := s.Value(ctx, b); string(v) != "2" {
		t.Fatalf("value %q", v)
	}
	// Set creates an entry of the kind its name suggests.
	c := ref("ns", "GITHUB_TOKEN")
	if _, err := s.Set(ctx, c, []byte("3")); err != nil {
		t.Fatal(err)
	}
	if got := details(c); got != (store.Details{Kind: store.ValueToken}) {
		t.Fatalf("Set created details %+v", got)
	}
	// An edit that changes nothing writes nothing.
	backend.Before = func(op, _ string) error { return fmt.Errorf("unexpected %s", op) }
	if err := s.Edit(ctx, b, b, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInferValueKind(t *testing.T) {
	for name, want := range map[string]store.ValueKind{
		"GITHUB_TOKEN":          store.ValueToken,
		"OPENAI_API_KEY":        store.ValueToken,
		"DB_PASSWORD":           store.ValuePassword,
		"CLIENT_SECRET":         store.ValuePassword,
		"AWS_SECRET_ACCESS_KEY": store.ValueToken,
		"DEPLOY_PRIVATE_KEY":    store.ValuePEM,
		"TLS_CERT":              store.ValueSetting, // a certificate is public
		"DATABASE_URL":          store.ValueURL,
		"SENTRY_DSN":            store.ValueURL,
		"AWS_ACCESS_KEY_ID":     store.ValueIdentifier,
		"smtp_user":             store.ValueIdentifier,
		"AWS_REGION":            store.ValueSetting,
		"TOKEN":                 store.ValueToken,
	} {
		if got := store.InferValueKind(name); got != want {
			t.Errorf("InferValueKind(%q) = %q, want %q", name, got, want)
		}
	}
	// A stored kind wins; none, or one this version does not know, falls
	// back to the name.
	for _, test := range []struct {
		stored, want store.ValueKind
	}{
		{store.ValueSetting, store.ValueSetting},
		{"", store.ValueToken},
		{"future-kind", store.ValueToken},
	} {
		if got := store.ValueKindOf("GITHUB_TOKEN", test.stored); got != test.want {
			t.Errorf("ValueKindOf with %q stored = %q, want %q", test.stored, got, test.want)
		}
	}
}

// Another writer deletes the entry after Set finds it exists and before it
// replaces the value. Set must report the conflict, not recreate the entry.
func TestSetConflictsWithConcurrentDelete(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	r := ref("ns", "A")
	s.Set(ctx, r, []byte("1"))
	backend.Before = func(op, account string) error {
		if op == "update" {
			backend.Before = nil
			return backend.Delete(ctx, account)
		}
		return nil
	}
	if _, err := s.Set(ctx, r, []byte("2")); store.KindOf(err) != store.KindConflict {
		t.Fatalf("Set: %v, want a conflict", err)
	}
	if namespaces, _ := s.Namespaces(ctx); len(namespaces) != 0 {
		t.Fatalf("Set recreated the entry: %v", namespaces)
	}
}

// An error names an entry only once the backend has shown that it exists.
func TestErrorsNameOnlyExistingEntries(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	stored, missing := ref("ns", "A"), ref("ns", "B")
	s.Set(ctx, stored, []byte("v"))
	backend.Before = func(op, account string) error {
		if op == "add" {
			return nil
		}
		return &store.Error{Kind: store.KindLocked, OSStatus: -25308, Message: "locked"}
	}
	_, err := s.Set(ctx, stored, []byte("w"))
	if e, ok := err.(*store.Error); !ok || e.Kind != store.KindLocked || e.Ref == nil || *e.Ref != stored {
		t.Fatalf("replacing Set: %#v, want locked naming %v", err, stored)
	}
	backend.Before = func(string, string) error { return &store.Error{Kind: store.KindLocked, Message: "locked"} }
	_, err = s.Set(ctx, missing, []byte("v"))
	errDelete := s.Delete(ctx, missing)
	errEdit := s.Edit(ctx, missing, ref("ns", "C"), nil, nil)
	errCreate := s.Create(ctx, missing, []byte("v"), store.Details{})
	for _, err := range []error{err, errDelete, errEdit, errCreate} {
		if e, ok := err.(*store.Error); !ok || e.Kind != store.KindLocked || e.Ref != nil {
			t.Errorf("write of an unconfirmed entry: %#v, want locked naming none", err)
		}
	}
}

// Only program access lets GrantedValue return a value. The grant survives
// a replaced value, a rename, and edited details, as the Keychain keeps an
// attribute that a write leaves out.
func TestGrantedValue(t *testing.T) {
	s := store.New(storetest.New(), nil)
	a, b := ref("ns", "A"), ref("ns", "B")
	s.Set(ctx, a, []byte("1"))
	_, err := s.GrantedValue(ctx, a)
	if e, ok := err.(*store.Error); !ok || e.Kind != store.KindNotGranted || e.Ref == nil || *e.Ref != a {
		t.Fatalf("not granted: %#v, want not_granted naming %v", err, a)
	}
	if _, _, err := s.ChangeAccess(ctx, []store.Ref{a}, true, approveAll); err != nil {
		t.Fatal(err)
	}
	s.Set(ctx, a, []byte("2"))
	s.Edit(ctx, a, b, nil, &store.Details{Notes: "edited"})
	if value, err := s.GrantedValue(ctx, b); err != nil || string(value) != "2" {
		t.Fatalf("granted: %q, %v", value, err)
	}
	if _, err := s.GrantedValue(ctx, a); store.KindOf(err) != store.KindNotFound {
		t.Fatalf("missing: %v, want not found", err)
	}
}

func approveAll(context.Context, string) (string, error) { return "dialog", nil }

type recordingBackend struct {
	*storetest.Backend
	auths []string
}

func (b *recordingBackend) SetProgramAccess(ctx context.Context, account string, granted bool) error {
	b.auths = append(b.auths, activity.AuthOf(ctx))
	return b.Backend.SetProgramAccess(ctx, account, granted)
}

// One approval covers every entry a change concerns, and entries already in
// the requested state need none: a change that writes nothing asks nothing.
func TestChangeAccess(t *testing.T) {
	backend := &recordingBackend{Backend: storetest.New()}
	recorder := new(storetest.Recorder)
	s := store.New(backend, recorder)
	a, b, c := ref("ns", "A"), ref("ns", "B"), ref("other", "C")
	for _, r := range []store.Ref{a, b, c} {
		s.Set(ctx, r, []byte("v"))
	}
	var reasons []string
	approve := func(_ context.Context, reason string) (string, error) {
		reasons = append(reasons, reason)
		return "reused", nil
	}
	changes, auth, err := s.ChangeAccess(ctx, []store.Ref{a, b, a, c}, true, approve)
	want := []store.AccessChange{{Ref: a, Changed: true}, {Ref: b, Changed: true}, {Ref: a}, {Ref: c, Changed: true}}
	if err != nil || !slices.Equal(changes, want) || auth != "reused" {
		t.Fatalf("grant: %v, %q, %v; want %v", changes, auth, err, want)
	}
	if want := []string{"grant program access to ns/A, ns/B and 1 more"}; !slices.Equal(reasons, want) {
		t.Fatalf("reasons %q, want %q", reasons, want)
	}
	if want := []string{"reused", "reused", "reused"}; !slices.Equal(backend.auths, want) {
		t.Fatalf("writes under %q, want %q", backend.auths, want)
	}
	if records := recorder.Records(); len(records) != 0 {
		t.Fatalf("an approved change recorded %v beside the backend's records", records)
	}
	changes, auth, err = s.ChangeAccess(ctx, []store.Ref{b}, true, approve)
	if err != nil || !slices.Equal(changes, []store.AccessChange{{Ref: b}}) || auth != "" || len(reasons) != 1 {
		t.Fatalf("unchanged grant: %v, %q, %v after %d approvals", changes, auth, err, len(reasons))
	}
	if _, _, err = s.ChangeAccess(ctx, []store.Ref{b, ref("ns", "MISSING")}, false, approve); store.KindOf(err) != store.KindNotFound || err.(*store.Error).Argument != 2 || len(reasons) != 1 {
		t.Fatalf("missing entry: %v after %d approvals, want not found at argument 2 before any", err, len(reasons))
	}
	refuse := func(context.Context, string) (string, error) {
		return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	// A refusal reaches no backend operation, so the store records each
	// change the owner was asked for.
	caller := activity.Caller{Exe: "/bin/zsh", Chain: []string{}}
	if _, _, err = s.ChangeAccess(activity.WithCaller(ctx, caller), []store.Ref{c, b, a, b}, false, refuse); store.KindOf(err) != store.KindCancelled || len(backend.auths) != 3 {
		t.Fatalf("refused: %v after %d writes, want cancelled after 3", err, len(backend.auths))
	}
	var refused []activity.Record
	for _, r := range []store.Ref{c, b, a} {
		refused = append(refused, activity.Record{Op: "revoke", Entry: r.String(), Result: "cancelled", Caller: caller})
	}
	if got := recorder.Records(); !reflect.DeepEqual(got, refused) {
		t.Fatalf("refusal recorded %+v, want %+v", got, refused)
	}
	backend.Before = func(op, account string) error {
		if account == "other/C" {
			return &store.Error{Kind: store.KindUncertain, Message: "uncertain"}
		}
		return nil
	}
	changes, _, err = s.ChangeAccess(ctx, []store.Ref{a, c, b}, false, approve)
	if e, ok := err.(*store.Error); !ok || e.Kind != store.KindUncertain || e.Ref == nil || *e.Ref != c || !slices.Equal(changes, []store.AccessChange{{Ref: a, Changed: true}}) {
		t.Fatalf("failed revoke: %v, %#v; want ns/A revoked, then uncertain naming %v", changes, err, c)
	}
	if want := "revoke program access to ns/A, other/C and 1 more"; reasons[1] != want {
		t.Fatalf("reason %q, want %q", reasons[1], want)
	}
}

func TestValuesSelection(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	s.Set(ctx, ref("app", "TOKEN"), []byte("t"))
	s.Set(ctx, ref("app", "URL"), []byte("u"))
	s.Set(ctx, ref("db", "PASSWORD"), []byte("p"))
	s.Set(ctx, ref("other", "TOKEN"), []byte("o"))
	namespace := func(name string) store.Ref { return store.Ref{Namespace: name} }

	for _, test := range []struct {
		selections []store.Ref
		want       []string
	}{
		{[]store.Ref{namespace("app"), namespace("db"), namespace("app")}, []string{"TOKEN=t", "URL=u", "PASSWORD=p"}},
		{[]store.Ref{ref("other", "TOKEN"), ref("db", "PASSWORD"), ref("other", "TOKEN")}, []string{"TOKEN=o", "PASSWORD=p"}},
		// An entry of a selected namespace is selected once.
		{[]store.Ref{namespace("app"), ref("app", "URL"), ref("db", "PASSWORD")}, []string{"URL=u", "PASSWORD=p", "TOKEN=t"}},
		// An entry takes the place of every namespace's entry of that name.
		{[]store.Ref{namespace("app"), namespace("other"), ref("other", "TOKEN")}, []string{"TOKEN=o", "URL=u"}},
	} {
		entries, err := s.Values(ctx, test.selections)
		if err != nil {
			t.Fatalf("Values(%v): %v", test.selections, err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.Name+"="+string(e.Value))
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("Values(%v): %q, want %q", test.selections, got, test.want)
		}
	}

	for _, test := range []struct {
		selections []store.Ref
		want       string
	}{
		{[]store.Ref{namespace("app"), namespace("other")}, "app and other both supply TOKEN"},
		{[]store.Ref{ref("app", "TOKEN"), ref("other", "TOKEN")}, "app/TOKEN and other/TOKEN both set TOKEN"},
		// A missing selection is named by position, and found before a
		// conflict that would name it.
		{[]store.Ref{namespace("app"), namespace("missing")}, "argument 2: the namespace has no entries"},
		{[]store.Ref{ref("app", "TOKEN"), ref("db", "TOKEN")}, "argument 2: the entry does not exist"},
	} {
		backend.Reads = nil
		if _, err := s.Values(ctx, test.selections); err == nil || err.Error() != test.want {
			t.Errorf("Values(%v): %v, want %q", test.selections, err, test.want)
		}
		if len(backend.Reads) != 0 {
			t.Errorf("Values(%v) read values before rejecting the selection", test.selections)
		}
	}
}

func TestValuesFailsWhenEntryVanishes(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend, nil)
	s.Set(ctx, ref("app", "A"), []byte("a"))
	s.Set(ctx, ref("app", "B"), []byte("b"))
	backend.Before = func(op, account string) error {
		if op == "read" {
			backend.Before = nil
			return backend.Delete(ctx, "app/B")
		}
		return nil
	}
	_, err := s.Values(ctx, []store.Ref{{Namespace: "app"}})
	e, ok := err.(*store.Error)
	if !ok || e.Kind != store.KindNotFound || e.Ref == nil || *e.Ref != ref("app", "B") {
		t.Fatalf("Values: %v, want not found naming app/B", err)
	}
}
