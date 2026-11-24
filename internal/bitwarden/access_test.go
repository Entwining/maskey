package bitwarden

import (
	"context"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// The owner approves a change to the secret the listing showed. A secret
// that takes its name while the owner answers gets no grant, and a revoke
// that would leave the secret approved its grant does not report success.
func TestChangeAccessAppliesOnlyToTheSecretListed(t *testing.T) {
	ref := store.Ref{Namespace: "a", Name: "B"}
	ctx := context.Background()

	t.Run("grant", func(t *testing.T) {
		s := newStack(t)
		s.add("a/B", "value", store.Details{})
		recorder := new(storetest.Recorder)
		recreate := func(context.Context, string) (string, error) {
			if err := s.backend.Delete(ctx, "a/B"); err != nil {
				t.Fatal(err)
			}
			s.add("a/B", "another", store.Details{})
			return "dialog", nil
		}
		changes, _, err := store.New(store.Recorded(s.backend, recorder), recorder).ChangeAccess(ctx, []store.Ref{ref}, true, recreate)
		if !store.IsKind(err, store.KindConflict) || len(changes) != 0 || len(s.grants.ids) != 0 {
			t.Fatalf("a grant to a secret created meanwhile: %v, changes %v, grants %v", err, changes, s.grants.ids)
		}
		if _, err := s.backend.Get(ctx, "a/B"); !store.IsKind(err, store.KindNotGranted) {
			t.Errorf("the secret created meanwhile: %v, want not_granted", err)
		}
		if records := recorder.Records(); len(records) != 1 || records[0].Op != "grant" || records[0].Result != string(store.KindConflict) {
			t.Errorf("recorded %+v", records)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		s := newStack(t)
		s.add("a/B", "value", store.Details{})
		if err := storetest.SetAccess(ctx, s.backend, "a/B", true); err != nil {
			t.Fatal(err)
		}
		granted := s.grants.ids[0]
		rename := func(context.Context, string) (string, error) {
			if err := s.backend.Edit(ctx, "a/B", "a/KEPT", nil, nil); err != nil {
				t.Fatal(err)
			}
			s.add("a/B", "another", store.Details{})
			return "dialog", nil
		}
		_, _, err := store.New(s.backend, new(storetest.Recorder)).ChangeAccess(ctx, []store.Ref{ref}, false, rename)
		if !store.IsKind(err, store.KindConflict) {
			t.Fatalf("a revoke that left the renamed secret its grant: %v, want conflict", err)
		}
		if len(s.grants.ids) != 1 || s.grants.ids[0] != granted {
			t.Errorf("grants %v", s.grants.ids)
		}
	})

	// An entry that is still the secret listed changes as approved.
	s := newStack(t)
	s.add("a/B", "value", store.Details{})
	approve := func(context.Context, string) (string, error) { return "dialog", nil }
	if _, _, err := store.New(s.backend, new(storetest.Recorder)).ChangeAccess(ctx, []store.Ref{ref}, true, approve); err != nil || len(s.grants.ids) != 1 {
		t.Fatalf("an unchanged entry: %v, grants %v", err, s.grants.ids)
	}
}
