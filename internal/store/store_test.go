package store_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

var ctx = context.Background()

func ref(namespace, name string) store.Ref { return store.Ref{Namespace: namespace, Name: name} }

func TestIdentifierRules(t *testing.T) {
	for _, namespace := range []string{"github", "aws.prod", "team_1", "a-b", "0"} {
		if err := store.ValidateNamespace(namespace); err != nil {
			t.Errorf("namespace %q rejected: %v", namespace, err)
		}
	}
	for _, namespace := range []string{"", "GitHub", "a/b", "-a", ".a", "a b", "a\x00"} {
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
	s := store.New(storetest.New())
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
	s := store.New(storetest.New())
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
	s := store.New(backend)
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

func TestForeignAccountsIgnored(t *testing.T) {
	backend := storetest.New()
	backend.Put("not-maskey", []byte("x"))
	backend.Put("Upper/NAME", []byte("x"))
	backend.Put("ns/bad-name", []byte("x"))
	backend.Put("ns/GOOD", []byte("x"))
	namespaces, err := store.New(backend).Namespaces(ctx)
	if err != nil || !slices.Equal(namespaces, []store.Namespace{{"ns", 1}}) {
		t.Fatalf("namespaces %v, error %v", namespaces, err)
	}
}

func TestWriteOutcomes(t *testing.T) {
	s := store.New(storetest.New())
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
	s := store.New(backend)
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
	s := store.New(backend)
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
	s := store.New(backend)
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

func TestBackendErrorsNameTheEntry(t *testing.T) {
	backend := storetest.New()
	backend.Before = func(op, account string) error {
		return &store.Error{Kind: store.KindLocked, OSStatus: -25308, Message: "locked"}
	}
	r := ref("ns", "A")
	_, err := store.New(backend).Set(ctx, r, []byte("v"))
	e, ok := err.(*store.Error)
	if !ok || e.Kind != store.KindLocked || e.Ref == nil || *e.Ref != r {
		t.Fatalf("Set: %#v, want locked naming %v", err, r)
	}
}

func TestValuesSelection(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend)
	s.Set(ctx, ref("app", "TOKEN"), []byte("t"))
	s.Set(ctx, ref("app", "URL"), []byte("u"))
	s.Set(ctx, ref("db", "PASSWORD"), []byte("p"))
	s.Set(ctx, ref("other", "TOKEN"), []byte("o"))

	for _, test := range []struct {
		namespaces []string
		refs       []store.Ref
		want       []string
	}{
		{[]string{"app", "db", "app"}, nil, []string{"TOKEN=t", "URL=u", "PASSWORD=p"}},
		{nil, []store.Ref{ref("other", "TOKEN"), ref("db", "PASSWORD")}, []string{"TOKEN=o", "PASSWORD=p"}},
		// An entry of a selected namespace is selected once.
		{[]string{"app"}, []store.Ref{ref("app", "URL"), ref("db", "PASSWORD")}, []string{"TOKEN=t", "URL=u", "PASSWORD=p"}},
		// A named entry takes the place of a namespace's entry of that name.
		{[]string{"app"}, []store.Ref{ref("other", "TOKEN")}, []string{"URL=u", "TOKEN=o"}},
	} {
		entries, err := s.Values(ctx, test.namespaces, test.refs)
		if err != nil {
			t.Fatalf("Values(%q, %v): %v", test.namespaces, test.refs, err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.Name+"="+string(e.Value))
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("Values(%q, %v): %q, want %q", test.namespaces, test.refs, got, test.want)
		}
	}

	for _, test := range []struct {
		namespaces []string
		refs       []store.Ref
		want       store.Kind
	}{
		{[]string{"app", "other"}, nil, store.KindConflict},
		{nil, []store.Ref{ref("app", "TOKEN"), ref("other", "TOKEN")}, store.KindConflict},
		{[]string{"app", "missing"}, nil, store.KindNotFound},
		{[]string{"app"}, []store.Ref{ref("db", "MISSING")}, store.KindNotFound},
		{nil, nil, store.KindInvalid},
		{[]string{"Bad"}, nil, store.KindInvalid},
		{nil, []store.Ref{ref("db", "9")}, store.KindInvalid},
	} {
		backend.Reads = nil
		if _, err := s.Values(ctx, test.namespaces, test.refs); store.KindOf(err) != test.want {
			t.Errorf("Values(%q, %v): %v, want %s", test.namespaces, test.refs, err, test.want)
		}
		if len(backend.Reads) != 0 {
			t.Errorf("Values(%q, %v) read values before rejecting the selection", test.namespaces, test.refs)
		}
	}
}

func TestValuesFailsWhenEntryVanishes(t *testing.T) {
	backend := storetest.New()
	s := store.New(backend)
	s.Set(ctx, ref("app", "A"), []byte("a"))
	s.Set(ctx, ref("app", "B"), []byte("b"))
	backend.Before = func(op, account string) error {
		if op == "read" {
			backend.Before = nil
			return backend.Delete(ctx, "app/B")
		}
		return nil
	}
	_, err := s.Values(ctx, []string{"app"}, nil)
	e, ok := err.(*store.Error)
	if !ok || e.Kind != store.KindNotFound || e.Ref == nil || *e.Ref != ref("app", "B") {
		t.Fatalf("Values: %v, want not found naming app/B", err)
	}
}
