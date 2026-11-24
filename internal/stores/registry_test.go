package stores

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/bitwarden"
	"github.com/Entwining/maskey/internal/bitwarden/bitwardentest"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

// memoryLocal stands in for the Keychain items that stay on this Mac.
type memoryLocal map[string][]byte

func (m memoryLocal) LocalGet(_ context.Context, account string) ([]byte, error) {
	value, ok := m[account]
	if !ok {
		return nil, &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	return value, nil
}

func (m memoryLocal) LocalSet(_ context.Context, account string, value []byte) error {
	m[account] = slices.Clone(value)
	return nil
}

func (m memoryLocal) LocalDelete(_ context.Context, account string) error {
	if _, ok := m[account]; !ok {
		return &store.Error{Kind: store.KindNotFound, Message: "the entry does not exist"}
	}
	delete(m, account)
	return nil
}

type fixture struct {
	fake   *bitwardentest.Server
	local  memoryLocal
	icloud *storetest.Backend
	dir    string
}

func newFixture(t *testing.T) *fixture {
	return &fixture{fake: bitwardentest.New(t), local: memoryLocal{}, icloud: storetest.New(), dir: t.TempDir()}
}

// registry is the registry as a newly started service has it.
func (f *fixture) registry() *Registry {
	r := NewRegistry(f.dir, f.icloud, f.local, f.fake.Client())
	r.regions = map[string]bitwarden.Region{"us": {API: f.fake.URL, Identity: f.fake.URL}}
	return r
}

func bitwardenToken() Addition {
	return Addition{Type: Bitwarden, Token: []byte(bitwardentest.Token)}
}

func TestAddConnectsAProjectUnderAUniqueName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	added, err := r.Add(ctx, bitwardenToken())
	want := Connection{ID: "bitwarden", Type: Bitwarden, Region: "us", Organization: bitwardentest.Organization, Project: bitwardentest.Project, ProjectName: "Maskey"}
	if err != nil || added != want {
		t.Fatalf("added %+v, %v", added, err)
	}
	if string(f.local["token/bitwarden"]) != bitwardentest.Token {
		t.Error("the token is not in the Keychain item of the store")
	}
	data, err := os.ReadFile(configPath(f.dir))
	if err != nil || strings.Contains(string(data), "C2Ig") {
		t.Fatalf("the configuration file: %v, holding the token: %v", err, err == nil)
	}
	if info, _ := os.Stat(configPath(f.dir)); info.Mode().Perm() != 0o600 {
		t.Errorf("the configuration file has mode %v", info.Mode().Perm())
	}

	// A second connection of the type needs a name of its own.
	if _, err := r.Add(ctx, bitwardenToken()); !store.IsKind(err, store.KindExists) {
		t.Errorf("a second connection without a name: %v, want exists", err)
	}
	work := bitwardenToken()
	work.ID = "work"
	if _, err := r.Add(ctx, work); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]store.Kind{"icloud": store.KindExists, "work": store.KindExists, "Work": store.KindInvalid, "a:b": store.KindInvalid, "-a": store.KindInvalid} {
		addition := bitwardenToken()
		addition.ID = name
		if _, err := r.Add(ctx, addition); !store.IsKind(err, want) {
			t.Errorf("the name %q: %v, want %s", name, err, want)
		}
	}
	config, err := Load(f.dir)
	var names []string
	for _, connection := range config.All() {
		names = append(names, connection.ID)
	}
	// Stores are listed by name, not in the order they were added.
	if err != nil || config.Default != ICloud || !slices.Equal(names, []string{"bitwarden", ICloud, "work"}) {
		t.Errorf("config %+v, %v", config, err)
	}
}

func TestAddKeepsNothingOfARefusedToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	refused := Addition{Type: Bitwarden, Token: []byte(strings.Replace(bitwardentest.Token, "C2Ig", "XXXX", 1))}
	if _, err := f.registry().Add(ctx, refused); !store.IsKind(err, store.KindUnauthenticated) {
		t.Errorf("a refused token: %v, want unauthenticated", err)
	}
	_, err := f.registry().Add(ctx, Addition{Type: Bitwarden, Token: []byte("not-a-token-0123456789")})
	if !store.IsKind(err, store.KindInvalid) || strings.Contains(err.Error(), "0123456789") {
		t.Errorf("a malformed token: %v", err)
	}
	for _, addition := range []Addition{{Type: "vault", Token: []byte(bitwardentest.Token)}, {Type: Bitwarden, Region: "moon", Token: []byte(bitwardentest.Token)}} {
		if _, err := f.registry().Add(ctx, addition); !store.IsKind(err, store.KindInvalid) {
			t.Errorf("%+v: %v, want invalid", addition.Type+addition.Region, err)
		}
	}
	if _, err := os.Stat(configPath(f.dir)); err == nil || len(f.local) != 0 {
		t.Errorf("a failed connection left a configuration (%v) or %d Keychain items", err, len(f.local))
	}
}

func TestAddNeedsTheProjectWhenTheAccountReadsSeveral(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const second = "44444444-4444-4444-8444-444444444444"
	f.fake.Projects = append(f.fake.Projects, bitwardentest.ProjectInfo{ID: second, Name: "Other"}, bitwardentest.ProjectInfo{ID: "55555555-5555-4555-8555-555555555555", Name: "Other"})
	for project, want := range map[string]store.Kind{"": store.KindInvalid, "Missing": store.KindNotFound, "Other": store.KindConflict} {
		addition := bitwardenToken()
		addition.Project = project
		if _, err := f.registry().Add(ctx, addition); !store.IsKind(err, want) {
			t.Errorf("the project %q: %v, want %s", project, err, want)
		}
	}
	byName, byID := bitwardenToken(), bitwardenToken()
	byName.Project, byID.Project, byID.ID = "Maskey", second, "other"
	if added, err := f.registry().Add(ctx, byName); err != nil || added.Project != bitwardentest.Project {
		t.Errorf("by name: %+v, %v", added, err)
	}
	if added, err := f.registry().Add(ctx, byID); err != nil || added.Project != second || added.ProjectName != "Other" {
		t.Errorf("by ID: %+v, %v", added, err)
	}
	f.fake.Projects = nil
	none := bitwardenToken()
	none.ID = "none"
	if _, err := f.registry().Add(ctx, none); !store.IsKind(err, store.KindPermissionDenied) {
		t.Errorf("an account without a project: %v, want permission_denied", err)
	}
}

func TestBackendOfAStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	if _, err := r.Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ICloud} {
		if backend, err := r.Backend(ctx, id); err != nil || backend != store.Backend(f.icloud) {
			t.Errorf("the store %q is not iCloud Keychain: %v", id, err)
		}
	}
	if _, err := r.Backend(ctx, "missing"); !store.IsKind(err, store.KindNotFound) {
		t.Errorf("a store that is not there: %v, want not_found", err)
	}
	backend, err := r.Backend(ctx, "bitwarden")
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Add(ctx, "github/GITHUB_TOKEN", []byte("value"), store.Details{}); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetProgramAccess(ctx, "github/GITHUB_TOKEN", true); err != nil {
		t.Fatal(err)
	}
	if items, _ := f.icloud.Items(ctx); len(items) != 0 || len(f.fake.Secrets) != 1 {
		t.Errorf("the entry went to iCloud Keychain (%d items) or not to Bitwarden (%d secrets)", len(items), len(f.fake.Secrets))
	}

	// The grant and the snapshot outlast the process, and need no network.
	f.fake.Close()
	later, err := f.registry().Backend(ctx, "bitwarden")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := later.Get(ctx, "github/GITHUB_TOKEN"); string(value) != "value" || err != nil {
		t.Errorf("a later process, offline, got %q, %v", value, err)
	}
	if err := later.SetProgramAccess(ctx, "github/GITHUB_TOKEN", false); err != nil || string(f.local["grants/bitwarden"]) != "[]" {
		t.Errorf("revoke: %v, leaving the grants %s", err, f.local["grants/bitwarden"])
	}

	delete(f.local, "token/bitwarden")
	if _, err := f.registry().Backend(ctx, "bitwarden"); !store.IsKind(err, store.KindUnauthenticated) {
		t.Errorf("a store whose token is gone: %v, want unauthenticated", err)
	}
}

func TestRemoveForgetsTheStoreOnThisMacOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	if _, err := r.Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	backend, _ := r.Backend(ctx, "bitwarden")
	if err := backend.Add(ctx, "a/B", []byte("value"), store.Details{}); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetProgramAccess(ctx, "a/B", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetDefault(ctx, "bitwarden"); err != nil {
		t.Fatal(err)
	}
	if config, _ := Load(f.dir); config.Default != "bitwarden" {
		t.Fatalf("the default is %s", config.Default)
	}
	for id, want := range map[string]store.Kind{ICloud: store.KindInvalid, "missing": store.KindNotFound, "": store.KindNotFound} {
		if err := r.Remove(ctx, id); !store.IsKind(err, want) {
			t.Errorf("removing %s: %v, want %s", id, err, want)
		}
	}
	for _, id := range []string{"missing", ""} {
		if err := r.SetDefault(ctx, id); !store.IsKind(err, store.KindNotFound) {
			t.Errorf("the default %q: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir, "stores", "bitwarden")); err != nil {
		t.Error("a removal that names no store removed a snapshot")
	}

	if err := r.Remove(ctx, "bitwarden"); err != nil {
		t.Fatal(err)
	}
	config, err := Load(f.dir)
	if err != nil || config.Default != ICloud || len(config.Stores) != 0 || len(f.local) != 0 {
		t.Errorf("after the removal: config %+v, %v, %d Keychain items", config, err, len(f.local))
	}
	if _, err := os.Stat(filepath.Join(f.dir, "stores", "bitwarden")); err == nil {
		t.Error("the snapshot stayed")
	}
	if _, err := r.Backend(ctx, "bitwarden"); !store.IsKind(err, store.KindNotFound) {
		t.Errorf("the removed store still answers: %v", err)
	}
	if len(f.fake.Secrets) != 1 {
		t.Error("removing the store deleted what Bitwarden holds")
	}
}

func TestSyncReadsBitwardenNow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	if _, err := r.Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	backend, _ := r.Backend(ctx, "bitwarden")
	if items, err := backend.Items(ctx); err != nil || len(items) != 0 {
		t.Fatalf("items %v, %v", items, err)
	}
	// Another Mac adds an entry.
	f.fake.Secrets["33333333-3333-4333-8333-999999999999"] = bitwardentest.Secret{Key: f.fake.Seal("a/B#token"), Value: f.fake.Seal("value"), Note: f.fake.Seal(""), ProjectIDs: []string{bitwardentest.Project}}
	f.fake.Order = append(f.fake.Order, "33333333-3333-4333-8333-999999999999")
	if items, _ := backend.Items(ctx); len(items) != 0 {
		t.Fatal("a fresh snapshot already shows the entry")
	}
	if err := r.Sync(ctx, "bitwarden"); err != nil {
		t.Fatal(err)
	}
	if items, _ := backend.Items(ctx); len(items) != 1 {
		t.Errorf("after a sync the store lists %d entries", len(items))
	}
	if err := r.Sync(ctx, ICloud); !store.IsKind(err, store.KindInvalid) {
		t.Errorf("syncing iCloud Keychain: %v", err)
	}
}

func TestLoadWithoutAConfiguration(t *testing.T) {
	dir := t.TempDir()
	config, err := Load(dir)
	if err != nil || config.Default != ICloud || len(config.All()) != 1 {
		t.Fatalf("config %+v, %v", config, err)
	}
	if c, err := config.Resolve(""); err != nil || c.ID != ICloud {
		t.Errorf("the default store is %+v, %v", c, err)
	}
	// A default naming a store that is gone falls back to the built-in one.
	if err := os.WriteFile(configPath(dir), []byte(`{"default":"gone","stores":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if config, err := Load(dir); err != nil || config.Default != ICloud {
		t.Errorf("config %+v, %v", config, err)
	}
	if err := os.WriteFile(configPath(dir), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !store.IsKind(err, store.KindPrerequisite) {
		t.Errorf("a broken configuration: %v", err)
	}
	// One that cannot be read at all fails the same way.
	if err := os.Remove(configPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(configPath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !store.IsKind(err, store.KindPrerequisite) {
		t.Errorf("an unreadable configuration: %v", err)
	}
}

// A new token replaces the old one only once it signs in and reads the
// store's project, and the store keeps its entries' program access.
func TestSetTokenKeepsTheStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	if _, err := r.Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	backend, _ := r.Backend(ctx, "bitwarden")
	if err := backend.Add(ctx, "a/B", []byte("value"), store.Details{}); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetProgramAccess(ctx, "a/B", true); err != nil {
		t.Fatal(err)
	}
	refused := []byte(strings.Replace(bitwardentest.Token, "C2Ig", "XXXX", 1))
	if err := r.SetToken(ctx, "bitwarden", refused); !store.IsKind(err, store.KindUnauthenticated) {
		t.Errorf("a refused token: %v", err)
	}
	for id, want := range map[string]store.Kind{ICloud: store.KindInvalid, "missing": store.KindNotFound, "": store.KindNotFound} {
		if err := r.SetToken(ctx, id, []byte(bitwardentest.Token)); !store.IsKind(err, want) {
			t.Errorf("a token for %s: %v, want %s", id, err, want)
		}
	}
	projects := f.fake.Projects
	f.fake.Projects = []bitwardentest.ProjectInfo{{ID: "44444444-4444-4444-8444-444444444444", Name: "Other"}}
	if err := r.SetToken(ctx, "bitwarden", []byte(bitwardentest.Token)); !store.IsKind(err, store.KindPermissionDenied) {
		t.Errorf("a token for another project: %v", err)
	}
	f.fake.Projects = projects
	if string(f.local["token/bitwarden"]) != bitwardentest.Token {
		t.Fatal("a token that was not accepted replaced the store's")
	}

	delete(f.local, "token/bitwarden")
	if err := r.SetToken(ctx, "bitwarden", []byte(bitwardentest.Token)); err != nil {
		t.Fatal(err)
	}
	backend, err := r.Backend(ctx, "bitwarden")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := backend.Get(ctx, "a/B"); err != nil || string(value) != "value" {
		t.Errorf("after the new token: %q, %v", value, err)
	}
}

// lostAnswer writes the item and then fails, as a write whose answer never
// arrived does.
type lostAnswer struct {
	memoryLocal
	// ended, when set, ends the request's context as the write is lost.
	ended context.CancelFunc
}

func (l lostAnswer) LocalSet(ctx context.Context, account string, value []byte) error {
	_ = l.memoryLocal.LocalSet(ctx, account, value)
	if l.ended != nil {
		l.ended()
	}
	return &store.Error{Kind: store.KindUncertain, Message: "the write may have happened"}
}

// LocalDelete fails, as the Keychain helper does, once ctx has ended.
func (l lostAnswer) LocalDelete(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	return l.memoryLocal.LocalDelete(ctx, account)
}

// After a write of program access that may have happened, the next use
// reads the Keychain item instead of answering from what was known before.
func TestGrantsReadAgainAfterAnUncertainWrite(t *testing.T) {
	ctx := context.Background()
	local := memoryLocal{"grants/work": []byte(`["id-1"]`)}
	g := &grants{local: lostAnswer{memoryLocal: local}, account: "grants/work"}
	if ids, err := g.Granted(ctx); err != nil || !slices.Equal(ids, []string{"id-1"}) {
		t.Fatalf("granted %v, %v", ids, err)
	}
	if err := g.Set(ctx, "id-1", false); !store.IsKind(err, store.KindUncertain) {
		t.Fatalf("revoke: %v", err)
	}
	if ids, err := g.Granted(ctx); err != nil || len(ids) != 0 {
		t.Errorf("granted %v, %v after a revoke that reached the Keychain", ids, err)
	}
}

// A request that took the store's backend before the store was removed, as
// one waiting for the owner's approval, leaves no program access for a
// store connected under the name again.
func TestRemoveEndsTheBackendARequestHolds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.registry()
	if _, err := r.Add(ctx, bitwardenToken()); err != nil {
		t.Fatal(err)
	}
	held, err := r.Backend(ctx, "bitwarden")
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Add(ctx, "a/B", []byte("value"), store.Details{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove(ctx, "bitwarden"); err != nil {
		t.Fatal(err)
	}
	if err := held.SetProgramAccess(ctx, "a/B", true); !store.IsKind(err, store.KindConflict) {
		t.Errorf("a grant after the removal: %v", err)
	}
	if _, kept := f.local["grants/bitwarden"]; kept {
		t.Error("the removed store has program access again")
	}
}

// A token whose write may have reached the Keychain is not left there for a
// store that was not added.
func TestAddLeavesNoTokenWithoutAStore(t *testing.T) {
	f := newFixture(t)
	r := f.registry()
	// The request's time ends while the token is written.
	ctx, cancel := context.WithCancel(context.Background())
	r.local = lostAnswer{f.local, cancel}
	if _, err := r.Add(ctx, bitwardenToken()); !store.IsKind(err, store.KindUncertain) {
		t.Fatalf("got %v, want uncertain", err)
	}
	if config, err := Load(f.dir); err != nil || len(config.Stores) != 0 || len(f.local) != 0 {
		t.Errorf("config %+v, %v, Keychain items %d", config, err, len(f.local))
	}
}
