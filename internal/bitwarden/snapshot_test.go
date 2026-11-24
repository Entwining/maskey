package bitwarden

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

func TestSnapshotContainsOnlyEntries(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	entry, err := s.client.Create(ctx, project, "a/B#token", []byte("v"), "note")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.Create(ctx, project, "ordinary", []byte("other"), "other note"); err != nil {
		t.Fatal(err)
	}
	if err := s.backend.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.fake.Fetched, []string{entry.ID}) {
		t.Errorf("get-by-ids requested %d secrets, want only the Maskey entry", len(s.fake.Fetched))
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	plain, err := s.fake.Open(file.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ Secrets []map[string]string }
	if err := json.Unmarshal([]byte(plain), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Secrets) != 1 {
		t.Fatalf("snapshot contains %d secrets, want only the Maskey entry", len(saved.Secrets))
	}
	want := map[string]string{"id": entry.ID, "project": project, "key": entry.Key, "revision": entry.Revision, "value": "dg==", "note": "note"}
	if !maps.Equal(saved.Secrets[0], want) {
		t.Errorf("saved entry has fields %v with unexpected names or contents", slices.Sorted(maps.Keys(saved.Secrets[0])))
	}
}

func TestSnapshotOmitsSecretRenamedDuringRead(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	entry, err := s.client.Create(ctx, project, "a/B#token", []byte("v"), "")
	if err != nil {
		t.Fatal(err)
	}
	s.fake.ReadIDs = func(ids []string) []string {
		renamed := s.fake.Secrets[entry.ID]
		renamed.Key = s.fake.Seal("ordinary")
		s.fake.Secrets[entry.ID] = renamed
		return ids
	}
	if err := s.backend.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	saved := s.client.readSnapshot(s.path, project)
	if saved == nil || len(saved.Secrets) != 0 {
		t.Fatal("the refresh did not save an empty snapshot after the rename")
	}
}

func TestEarlierSnapshot(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprintf("offline=%v", offline), func(t *testing.T) {
			s := newStack(t)
			s.add("a/B", "current", store.Details{})
			plain := fmt.Sprintf(`{"project":%q,"refreshed":%q,"secrets":[{"ID":%q,"Project":%q,"Key":"a/B#token","Revision":"old","Value":"b2xk","Note":""}]}`,
				project, s.now.Format(time.RFC3339Nano), s.fake.Order[0], project)
			data, err := json.Marshal(snapshotFile{Version: 1, Payload: s.backend.client.session.payload, Sealed: s.fake.Seal(plain)})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			backend := s.open(vectorTokenParsed(t))
			if offline {
				s.fake.Close()
				if _, err := backend.Items(store.WithSaved(t.Context())); !store.IsKind(err, store.KindNotFound) {
					t.Errorf("saved listing with an earlier copy: %v, want not_found", err)
				}
				if value, err := s.read(backend, "a/B"); value != "" || !store.IsKind(err, store.KindNetwork) {
					t.Errorf("offline read with an earlier copy: %v, want network and no value", err)
				}
				return
			}
			if value, err := s.read(backend, "a/B"); err != nil || value != "current" {
				t.Fatalf("online read did not refresh the earlier copy: %v", err)
			}
			s.fake.Close()
			if value, err := s.read(s.open(vectorTokenParsed(t)), "a/B"); err != nil || value != "current" {
				t.Errorf("the refreshed copy was not readable after restart: %v", err)
			}
		})
	}
}
