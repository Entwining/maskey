package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func recordsOf(recorder *storetest.Recorder) []string {
	var lines []string
	for _, r := range recorder.Records() {
		lines = append(lines, strings.TrimSpace(r.Op+" "+r.Entry+" "+r.To+" "+r.Result))
	}
	return lines
}

// A recorded backend enters each operation that reads or changes an entry,
// once, with how it ended.
func TestRecordedBackend(t *testing.T) {
	recorder := new(storetest.Recorder)
	backend := store.Recorded(storetest.New(), recorder)
	_ = backend.Add(ctx, "a/B", []byte("v"), store.Details{})
	_ = backend.Add(ctx, "a/B", []byte("v"), store.Details{})
	_, _ = backend.Items(ctx)
	_, _ = backend.Read(ctx, []string{"a/B", "a/MISSING"})
	_, _ = backend.Get(ctx, "a/B")
	_ = backend.SetProgramAccess(ctx, "a/B", true)
	_ = backend.Update(ctx, "a/B", []byte("w"))
	_ = backend.Edit(ctx, "a/B", "a/C", nil, nil)
	_ = backend.SetProgramAccess(ctx, "a/C", false)
	_ = backend.Delete(ctx, "a/C")
	want := []string{
		"add a/B  ok", "add a/B  exists", "read a/B  ok", "read a/MISSING  not_found", "get a/B  not_granted",
		"grant a/B  ok", "update a/B  ok", "edit a/B a/C ok", "revoke a/C  ok", "delete a/C  ok",
	}
	if got := recordsOf(recorder); !slices.Equal(got, want) {
		t.Errorf("recorded\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// An operation that ends unavailable is recorded where it ends, and not
// where another process performs it afterwards.
func TestRecordedBackendUnavailable(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"final", ctx, []string{"get a/B  unavailable"}},
		{"performed elsewhere", store.WithFallback(ctx), nil},
	} {
		recorder := new(storetest.Recorder)
		inner := storetest.New()
		inner.Before = func(string, string) error {
			return &store.Error{Kind: store.KindUnavailable, Message: "the Keychain is not reachable"}
		}
		_, _ = store.Recorded(inner, recorder).Get(test.ctx, "a/B")
		if got := recordsOf(recorder); !slices.Equal(got, test.want) {
			t.Errorf("%s: recorded %v, want %v", test.name, got, test.want)
		}
	}
}

// Replacing a value is one record, the update; the add that found the entry
// is not a failure of its own.
func TestSetRecordsAReplacementOnce(t *testing.T) {
	recorder := new(storetest.Recorder)
	s := store.New(store.Recorded(storetest.New(), recorder), recorder)
	for range 2 {
		if _, err := s.Set(ctx, ref("a", "B"), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := recordsOf(recorder), []string{"add a/B  ok", "update a/B  ok"}; !slices.Equal(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
}

// A change to program access that ends because the store cannot be listed
// reaches no entry, and is recorded for each entry it named.
func TestChangeAccessRecordsAStoreItCannotList(t *testing.T) {
	recorder := new(storetest.Recorder)
	backend := storetest.New()
	backend.Before = func(string, string) error {
		return &store.Error{Kind: store.KindNetwork, Message: "the store cannot be reached"}
	}
	refs := []store.Ref{{Namespace: "a", Name: "B"}, {Namespace: "a", Name: "C"}}
	approve := func(context.Context, string) (string, error) { return "", nil }
	if _, _, err := store.New(store.Recorded(backend, recorder), recorder).ChangeAccess(ctx, refs, false, approve); !store.IsKind(err, store.KindNetwork) {
		t.Fatal(err)
	}
	if got := recordsOf(recorder); !slices.Equal(got, []string{"revoke a/B  network", "revoke a/C  network"}) {
		t.Errorf("recorded %v", got)
	}
}
