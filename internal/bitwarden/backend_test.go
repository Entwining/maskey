package bitwarden

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

const indexRoute = "GET /projects/" + project + "/secrets"

type memoryGrants struct{ ids []string }

func (g *memoryGrants) Granted(context.Context) ([]string, error) { return slices.Clone(g.ids), nil }

func (g *memoryGrants) Set(_ context.Context, id string, granted bool) error {
	g.ids = slices.DeleteFunc(g.ids, func(other string) bool { return other == id })
	if granted {
		g.ids = append(g.ids, id)
	}
	return nil
}

// stack is a backend over the fake server, with a clock the test moves.
type stack struct {
	t       *testing.T
	fake    *fakeServer
	client  *Client
	backend *Backend
	grants  *memoryGrants
	path    string
	now     time.Time
}

func newStack(t *testing.T) *stack {
	t.Helper()
	fake, client := newFake(t)
	s := &stack{
		t: t, fake: fake, client: client, grants: &memoryGrants{},
		path: filepath.Join(t.TempDir(), "stores", "bitwarden", "snapshot"),
		now:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	s.backend = s.open(vectorTokenParsed(t))
	return s
}

// open returns a backend as another process would start it: with a client
// that has not signed in, over the same snapshot file and grants.
func (s *stack) open(token *Token) *Backend {
	backend := NewBackend(clientOf(s.fake, token), project, s.path, s.grants)
	backend.now = func() time.Time { return s.now }
	return backend
}

func (s *stack) add(account, value string, details store.Details) {
	s.t.Helper()
	if err := s.backend.Add(context.Background(), account, []byte(value), details); err != nil {
		s.t.Fatalf("adding %s: %v", account, err)
	}
}

// read is the value of account as the backend reads it, or "" without one.
func (s *stack) read(backend *Backend, account string) (string, error) {
	values, err := backend.Read(context.Background(), []string{account})
	return string(values[account].Value), err
}

// keys are the keys of the secrets the server holds, by ID.
func (s *stack) keys() map[string]string {
	s.t.Helper()
	keys := map[string]string{}
	for id, secret := range s.fake.Secrets {
		name, err := s.fake.Open(secret.Key)
		if err != nil {
			s.t.Fatal(err)
		}
		keys[id] = name
	}
	return keys
}

func TestBackendStoresEntriesAsSecrets(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("github/GITHUB_TOKEN", "one", store.Details{Kind: store.ValuePassword, Notes: "from the settings page"})
	s.add("deploy/SSH_KEY", "key", store.Details{})

	keys := s.keys()
	id := s.fake.Order[0]
	if keys[id] != "github/GITHUB_TOKEN#password" || keys[s.fake.Order[1]] != "deploy/SSH_KEY#ssh" {
		t.Fatalf("the server holds the keys %v", keys)
	}
	_, refs, attributes, err := store.New(s.backend, nil).Listing(ctx)
	if err != nil || len(refs) != 2 {
		t.Fatalf("listing %v, %v", refs, err)
	}
	token := attributes[store.Ref{Namespace: "github", Name: "GITHUB_TOKEN"}]
	if token.Kind != store.ValuePassword || token.Notes != "from the settings page" || token.ProgramAccess {
		t.Errorf("listed %+v", token)
	}
	values, err := s.backend.Read(ctx, []string{"github/GITHUB_TOKEN", "deploy/SSH_KEY", "deploy/MISSING"})
	if err != nil || len(values) != 2 || string(values["github/GITHUB_TOKEN"].Value) != "one" || values["deploy/SSH_KEY"].Kind != store.ValueSSH {
		t.Fatalf("read %v, %v", values, err)
	}

	if err := s.backend.Update(ctx, "github/GITHUB_TOKEN", []byte("two")); err != nil {
		t.Fatal(err)
	}
	stored := s.fake.Secrets[id]
	if value, _ := s.fake.Open(stored.Value); value != "two" || !stored.ValueChanged {
		t.Errorf("after an update the server holds %q, value changed %v", value, stored.ValueChanged)
	}
	if note, _ := s.fake.Open(stored.Note); note != "from the settings page" {
		t.Errorf("an update of the value changed the notes to %q", note)
	}

	// A rename with new details and no value is one write to the same secret.
	if err := s.backend.Edit(ctx, "github/GITHUB_TOKEN", "github/GH_PAT", nil, &store.Details{Kind: store.ValueToken}); err != nil {
		t.Fatal(err)
	}
	stored = s.fake.Secrets[id]
	if s.keys()[id] != "github/GH_PAT#token" || stored.ValueChanged || len(s.fake.Order) != 2 {
		t.Errorf("after a rename the server holds %v, value changed %v", s.keys(), stored.ValueChanged)
	}
	if value, _ := s.read(s.backend, "github/GH_PAT"); value != "two" {
		t.Errorf("the renamed entry reads %q", value)
	}

	for name, err := range map[string]error{
		"adding an entry that exists":   s.backend.Add(ctx, "deploy/SSH_KEY", []byte("v"), store.Details{}),
		"renaming onto another entry":   s.backend.Edit(ctx, "github/GH_PAT", "deploy/SSH_KEY", nil, nil),
		"updating an entry that is not": s.backend.Update(ctx, "github/GITHUB_TOKEN", []byte("v")),
		"deleting an entry that is not": s.backend.Delete(ctx, "github/GITHUB_TOKEN"),
	} {
		want := store.KindNotFound
		if strings.Contains(name, "exists") || strings.Contains(name, "another") {
			want = store.KindExists
		}
		if !store.IsKind(err, want) {
			t.Errorf("%s: %v, want %s", name, err, want)
		}
	}
	if err := s.backend.Delete(ctx, "github/GH_PAT"); err != nil || len(s.fake.Secrets) != 1 {
		t.Errorf("delete: %v, leaving %d secrets", err, len(s.fake.Secrets))
	}
	if value, err := s.read(s.backend, "github/GH_PAT"); value != "" || err != nil {
		t.Errorf("a deleted entry reads %q, %v", value, err)
	}
}

func TestBackendReadsOfflineFromTheSnapshot(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("github/GITHUB_TOKEN", "the-value", store.Details{Notes: "the-notes"})
	file, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"the-value", "the-notes", "GITHUB_TOKEN", project} {
		if bytes.Contains(file, []byte(plain)) {
			t.Errorf("the snapshot file holds %q in the clear", plain)
		}
	}
	if err := s.backend.SetProgramAccess(ctx, "github/GITHUB_TOKEN", true); err != nil {
		t.Fatal(err)
	}

	s.fake.Close()
	s.now = s.now.Add(30 * 24 * time.Hour)
	offline := s.open(vectorTokenParsed(s.t))
	if value, err := s.read(offline, "github/GITHUB_TOKEN"); value != "the-value" || err != nil {
		t.Fatalf("offline read %q, %v", value, err)
	}
	if value, err := offline.Get(ctx, "github/GITHUB_TOKEN"); string(value) != "the-value" || err != nil {
		t.Errorf("offline get %q, %v", value, err)
	}
	if items, err := offline.Items(ctx); err != nil || len(items) != 1 || items[0].Notes != "the-notes" {
		t.Errorf("offline items %+v, %v", items, err)
	}
	if err := offline.SetProgramAccess(ctx, "github/GITHUB_TOKEN", false); err != nil || len(s.grants.ids) != 0 {
		t.Errorf("offline revoke: %v, grants %v", err, s.grants.ids)
	}
	for name, err := range map[string]error{
		"add":    offline.Add(ctx, "github/OTHER", []byte("v"), store.Details{}),
		"update": offline.Update(ctx, "github/GITHUB_TOKEN", []byte("v")),
		"edit":   offline.Edit(ctx, "github/GITHUB_TOKEN", "github/OTHER", nil, nil),
		"delete": offline.Delete(ctx, "github/GITHUB_TOKEN"),
	} {
		if !store.IsKind(err, store.KindNetwork) {
			t.Errorf("offline %s: %v, want network", name, err)
		}
	}
	if value, _ := s.read(offline, "github/GITHUB_TOKEN"); value != "the-value" {
		t.Errorf("after refused offline writes the entry reads %q", value)
	}

	// The snapshot opens only with the token it was written under.
	other, err := ParseToken(strings.Replace(vectorToken, "X8vb", "Y8vb", 1))
	if err != nil {
		t.Fatal(err)
	}
	if value, err := s.read(s.open(other), "github/GITHUB_TOKEN"); value != "" || !store.IsKind(err, store.KindNetwork) {
		t.Errorf("another token read %q, %v", value, err)
	}
}

func TestBackendRefreshesAnOldSnapshot(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/KEPT", "kept", store.Details{})
	s.add("a/CHANGED", "before", store.Details{})
	s.add("a/DELETED", "gone", store.Details{})
	kept, changed, deleted := s.fake.Order[0], s.fake.Order[1], s.fake.Order[2]

	// Another Mac changes the project.
	if _, err := s.client.Update(ctx, changed, project, "a/CHANGED#token", []byte("after"), "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.client.Delete(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	created, err := s.client.Create(ctx, project, "a/CREATED#token", []byte("new"), "")
	if err != nil {
		t.Fatal(err)
	}

	requests := len(s.fake.Requests)
	s.now = s.now.Add(snapshotFresh - time.Second)
	if value, _ := s.read(s.backend, "a/CHANGED"); value != "before" || len(s.fake.Requests) != requests {
		t.Fatalf("a fresh snapshot read %q after %d requests", value, len(s.fake.Requests)-requests)
	}
	s.fake.Fetched = nil
	s.now = s.now.Add(time.Second)
	values, err := s.backend.Read(ctx, []string{"a/KEPT", "a/CHANGED", "a/DELETED", "a/CREATED"})
	if err != nil || len(values) != 3 || string(values["a/CHANGED"].Value) != "after" || string(values["a/CREATED"].Value) != "new" || string(values["a/KEPT"].Value) != "kept" {
		t.Fatalf("an old snapshot read %v, %v", values, err)
	}
	if slices.Sort(s.fake.Fetched); !slices.Equal(s.fake.Fetched, []string{changed, created.ID}) || slices.Contains(s.fake.Fetched, kept) {
		t.Errorf("the refresh fetched %v, want only the changed and the created secret", s.fake.Fetched)
	}
	// What that refresh fetched is not fetched again while it stays as it is.
	s.fake.Fetched = nil
	s.now = s.now.Add(snapshotFresh)
	if _, err := s.backend.Items(ctx); err != nil || len(s.fake.Fetched) != 0 {
		t.Errorf("the next refresh fetched %v, %v", s.fake.Fetched, err)
	}
	// A project the account can read but that is empty is an empty store.
	for _, account := range []string{"a/KEPT", "a/CHANGED", "a/CREATED"} {
		if err := s.backend.Delete(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	s.now = s.now.Add(snapshotFresh)
	if items, err := s.backend.Items(ctx); err != nil || len(items) != 0 {
		t.Errorf("an empty project lists %v, %v", items, err)
	}
}

func TestBackendKeepsTheSnapshotOnlyWhileBitwardenAllows(t *testing.T) {
	for name, test := range map[string]struct {
		refuse func(*fakeServer)
		want   store.Kind
		kept   bool
	}{
		"the token is revoked": {func(f *fakeServer) {
			f.Expired = f.Sessions
			f.Fail["POST /connect/token"] = 400
		}, store.KindUnauthenticated, false},
		"the project is no longer readable": {func(f *fakeServer) { f.Lost = true }, store.KindNotFound, false},
		"access is denied":                  {func(f *fakeServer) { f.Fail[indexRoute] = 403 }, store.KindPermissionDenied, false},
		"the server fails":                  {func(f *fakeServer) { f.Fail[indexRoute] = 503 }, "", true},
		"requests are limited":              {func(f *fakeServer) { f.Fail[indexRoute] = 429 }, "", true},
	} {
		s := newStack(t)
		s.add("a/B", "value", store.Details{})
		s.now = s.now.Add(snapshotFresh)
		test.refuse(s.fake)
		value, err := s.read(s.backend, "a/B")
		if store.KindOf(err) != test.want || (value == "value") != test.kept {
			t.Errorf("%s: read %q, %v", name, value, err)
		}
		_, statErr := os.Stat(s.path)
		if (statErr == nil) != test.kept {
			t.Errorf("%s: the snapshot file: %v", name, statErr)
		}
		// Without Bitwarden, another process has only the file to go by.
		s.fake.Close()
		value, err = s.read(s.open(vectorTokenParsed(s.t)), "a/B")
		if (value == "value") != test.kept || (err == nil) != test.kept {
			t.Errorf("%s: later and offline, read %q, %v", name, value, err)
		}
	}
}

func TestBackendPausesAfterAFailedRefresh(t *testing.T) {
	s := newStack(t)
	s.add("a/B", "value", store.Details{})
	s.now = s.now.Add(snapshotFresh)
	s.fake.Fail[indexRoute] = 503
	for range 3 {
		if value, err := s.read(s.backend, "a/B"); value != "value" || err != nil {
			t.Fatalf("read %q, %v", value, err)
		}
	}
	if got := s.fake.Count(indexRoute); got != 2 {
		t.Errorf("%d listings after one failed refresh, want the add's and the failed one", got)
	}
	s.now = s.now.Add(refreshPause)
	if _, err := s.read(s.backend, "a/B"); err != nil || s.fake.Count(indexRoute) != 3 {
		t.Errorf("after the pause: %v, %d listings", err, s.fake.Count(indexRoute))
	}
}

func TestBackendAsksAgainAfterAnUncertainWrite(t *testing.T) {
	s := newStack(t)
	s.add("a/B", "value", store.Details{})
	id := s.fake.Order[0]
	s.fake.Fail["PUT /secrets/"+id] = 502
	if err := s.backend.Update(context.Background(), "a/B", []byte("next")); !store.IsKind(err, store.KindUncertain) {
		t.Fatalf("got %v, want uncertain", err)
	}
	listings := s.fake.Count(indexRoute)
	if value, err := s.read(s.open(vectorTokenParsed(s.t)), "a/B"); value != "value" || err != nil || s.fake.Count(indexRoute) != listings+1 {
		t.Errorf("read %q, %v after %d more listings, want Bitwarden's answer", value, err, s.fake.Count(indexRoute)-listings)
	}

	// The pause after a refresh that failed does not put that read off.
	s.now = s.now.Add(snapshotFresh)
	s.fake.Fail[indexRoute] = 503
	if _, err := s.read(s.backend, "a/B"); err != nil {
		t.Fatal(err)
	}
	s.fake.Fail["PUT /secrets/"+id] = 502
	if err := s.backend.Update(context.Background(), "a/B", []byte("next")); !store.IsKind(err, store.KindUncertain) {
		t.Fatalf("got %v, want uncertain", err)
	}
	listings = s.fake.Count(indexRoute)
	if _, err := s.read(s.backend, "a/B"); err != nil || s.fake.Count(indexRoute) != listings+1 {
		t.Errorf("within the pause: %v after %d more listings", err, s.fake.Count(indexRoute)-listings)
	}
}

// A secret moved to another project between the listing and its read is not
// this store's entry, though the token reads it.
func TestBackendLeavesASecretMovedToAnotherProject(t *testing.T) {
	s := newStack(t)
	s.add("a/B", "value", store.Details{})
	id := s.fake.Order[0]
	moved := s.fake.Secrets[id]
	moved.Value, moved.ProjectIDs = s.fake.Seal("another project's"), []string{"44444444-4444-4444-8444-444444444444"}
	s.fake.Secrets[id] = moved
	s.now = s.now.Add(snapshotFresh)
	if value, err := s.read(s.backend, "a/B"); value != "value" || err != nil {
		t.Errorf("read %q, %v, want the value this project held", value, err)
	}
	if err := s.backend.Refresh(context.Background()); !store.IsKind(err, store.KindRemote) {
		t.Errorf("sync: %v, want remote", err)
	}
}

// A backend this Mac closed, as with its store removed, neither reads the
// project nor changes its grants for a request that took it before.
func TestBackendClosed(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	s.backend.Close()
	if err := s.backend.SetProgramAccess(ctx, "a/B", true); !store.IsKind(err, store.KindConflict) || len(s.grants.ids) != 0 {
		t.Errorf("grant: %v, grants %v", err, s.grants.ids)
	}
	if _, err := s.read(s.backend, "a/B"); !store.IsKind(err, store.KindConflict) {
		t.Errorf("read: %v", err)
	}
}

func TestBackendRefusesAmbiguousAndForeignKeys(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	for _, name := range []string{"plain", "github/NO_KIND", "github/UNKNOWN#certificate", "bad name/X#token", "github/1DIGIT#token", "github/TOKEN#token"} {
		if _, err := s.client.Create(ctx, project, name, []byte("v"), ""); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.backend.Items(ctx)
	if err != nil || len(items) != 1 || items[0].Account != "github/TOKEN" {
		t.Fatalf("items %+v, %v", items, err)
	}
	if err := s.backend.SetProgramAccess(ctx, "github/TOKEN", true); err != nil {
		t.Fatal(err)
	}
	if values, err := s.backend.Read(ctx, []string{"github/NO_KIND", "github/UNKNOWN"}); err != nil || len(values) != 0 {
		t.Errorf("read secrets that are not entries: %v, %v", values, err)
	}

	if _, err := s.client.Create(ctx, project, "github/TOKEN#password", []byte("other"), ""); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(snapshotFresh)
	_, err = s.backend.Read(ctx, []string{"github/TOKEN"})
	var conflict *store.Error
	if !store.IsKind(err, store.KindConflict) || !errors.As(err, &conflict) || conflict.Ref == nil || conflict.Ref.String() != "github/TOKEN" {
		t.Errorf("two secrets with one name: %v", err)
	}
	if err := s.backend.Update(ctx, "github/TOKEN", []byte("next")); !store.IsKind(err, store.KindConflict) {
		t.Errorf("update of an entry two secrets name: %v", err)
	}
	if err := s.backend.Edit(ctx, "github/TOKEN", "github/MOVED", nil, nil); !store.IsKind(err, store.KindConflict) || slices.Contains(slices.Collect(maps.Values(s.keys())), "github/MOVED#token") {
		t.Errorf("rename of an entry two secrets name: %v, keys %v", err, s.keys())
	}
	// The grant one of the secrets has still shows, and a revoke fails
	// rather than leave it for when the name is one secret's again.
	if items, err := s.backend.Items(ctx); err != nil || len(items) != 1 || !items[0].ProgramAccess {
		t.Errorf("items %+v, %v", items, err)
	}
	if err := s.backend.SetProgramAccess(ctx, "github/TOKEN", false); !store.IsKind(err, store.KindConflict) || len(s.grants.ids) != 1 {
		t.Errorf("revoke of an entry two secrets name: %v, grants %v", err, s.grants.ids)
	}
	// The store's other entries stay usable, and the listing still shows
	// the entry that needs repair.
	s.add("deploy/KEY", "value", store.Details{})
	if got, err := s.read(s.backend, "deploy/KEY"); err != nil || got != "value" {
		t.Errorf("another entry read %q, %v", got, err)
	}
	if items, err := s.backend.Items(ctx); err != nil || len(items) != 2 {
		t.Errorf("items %+v, %v", items, err)
	}
}

func TestBackendGrantsBelongToTheSecret(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.add("a/B", "value", store.Details{})
	if _, err := s.backend.Get(ctx, "a/B"); !store.IsKind(err, store.KindNotGranted) {
		t.Fatalf("get without a grant: %v", err)
	}
	if _, err := s.backend.Get(ctx, "a/MISSING"); !store.IsKind(err, store.KindNotFound) {
		t.Errorf("get of a missing entry: %v", err)
	}
	if err := s.backend.SetProgramAccess(ctx, "a/B", true); err != nil {
		t.Fatal(err)
	}
	if value, err := s.backend.Get(ctx, "a/B"); string(value) != "value" || err != nil {
		t.Errorf("get with a grant: %q, %v", value, err)
	}
	if items, _ := s.backend.Items(ctx); len(items) != 1 || !items[0].ProgramAccess {
		t.Errorf("items %+v", items)
	}
	// A rename keeps the secret and so its grant.
	if err := s.backend.Edit(ctx, "a/B", "a/C", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.backend.Get(ctx, "a/C"); err != nil {
		t.Errorf("get after a rename: %v", err)
	}
	// An entry created again under the name is another secret.
	if err := s.backend.Delete(ctx, "a/C"); err != nil {
		t.Fatal(err)
	}
	s.add("a/C", "again", store.Details{})
	if _, err := s.backend.Get(ctx, "a/C"); !store.IsKind(err, store.KindNotGranted) || len(s.grants.ids) != 0 {
		t.Errorf("get of a recreated entry: %v, grants %v", err, s.grants.ids)
	}
}

// A write that finds the token refused discards the snapshot as a refused
// read does; one that is only not permitted leaves it.
func TestBackendDiscardsTheSnapshotWhenAWriteFindsTheTokenRefused(t *testing.T) {
	for status, kept := range map[int]bool{401: false, 403: true} {
		s := newStack(t)
		s.add("a/B", "value", store.Details{})
		s.fake.Fail["PUT /secrets/"+s.fake.Order[0]] = status
		s.fake.Fail["POST /connect/token"] = 400
		err := s.backend.Update(context.Background(), "a/B", []byte("next"))
		want := store.KindPermissionDenied
		if !kept {
			want = store.KindUnauthenticated
		}
		if !store.IsKind(err, want) {
			t.Fatalf("status %d: %v, want %s", status, err, want)
		}
		if _, statErr := os.Stat(s.path); (statErr == nil) != kept {
			t.Errorf("status %d: the snapshot file: %v", status, statErr)
		}
	}
}

// A listing marked for the saved copy never waits for Bitwarden: it answers
// from a copy however old, and says so when this Mac has none.
func TestBackendListsTheSavedCopyWithoutAsking(t *testing.T) {
	s := newStack(t)
	saved := store.WithSaved(context.Background())
	if _, err := s.open(vectorTokenParsed(t)).Items(saved); !store.IsKind(err, store.KindNotFound) || len(s.fake.Requests) != 0 {
		t.Fatalf("without a copy: %v after %d requests", err, len(s.fake.Requests))
	}
	s.add("a/SAVED", "value", store.Details{})
	if _, err := s.client.Create(context.Background(), project, "a/ELSEWHERE#token", []byte("new"), ""); err != nil {
		t.Fatal(err)
	}
	s.now = s.now.Add(30 * 24 * time.Hour)
	requests := len(s.fake.Requests)
	items, err := s.open(vectorTokenParsed(t)).Items(saved)
	if err != nil || len(items) != 1 || items[0].Account != "a/SAVED" || len(s.fake.Requests) != requests {
		t.Fatalf("an old copy lists %v, %v after %d requests", items, err, len(s.fake.Requests)-requests)
	}
}
