package stores

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Entwining/maskey/internal/bitwarden"
	"github.com/Entwining/maskey/internal/store"
)

// Local keeps items in this Mac's Keychain that do not sync: each remote
// store's access token and the program access of its entries.
type Local interface {
	LocalGet(ctx context.Context, account string) ([]byte, error)
	LocalSet(ctx context.Context, account string, value []byte) error
	LocalDelete(ctx context.Context, account string) error
}

// Addition is a connection to add. ID is its name, or "" for the name of
// its type; Project names the Bitwarden project by ID or by name, or is ""
// when the machine account can read exactly one. Token is a credential: it
// goes to the Keychain and into no error.
type Addition struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Region  string `json:"region,omitempty"`
	Project string `json:"project,omitempty"`
	Token   []byte `json:"token,omitempty"`
}

// Registry is the one writer of this Mac's store configuration and the one
// holder of its remote stores' sessions, which is why the Maskey service
// owns it.
type Registry struct {
	dir     string
	icloud  store.Backend
	local   Local
	http    *http.Client
	regions map[string]bitwarden.Region

	// mu guards backends and turns, and each change of the configuration
	// from its reading to its writing. No request to a remote and no
	// Keychain call runs under it, so that one store's slow exchange holds
	// back no other store.
	mu       sync.Mutex
	backends map[string]opened
	turns    map[string]*sync.Mutex
}

// opened is a connection's backend and the target it was opened for.
type opened struct {
	target  string
	backend *bitwarden.Backend
}

func NewRegistry(dir string, icloud store.Backend, local Local, client *http.Client) *Registry {
	return &Registry{dir: dir, icloud: icloud, local: local, http: client, regions: bitwarden.Regions, backends: map[string]opened{}, turns: map[string]*sync.Mutex{}}
}

// cleanupTimeout bounds the removal of a token whose store was not added.
const cleanupTimeout = 30 * time.Second

func tokenAccount(id string) string  { return "token/" + id }
func grantsAccount(id string) string { return "grants/" + id }

// turn waits until no other request opens or changes the connection named
// id, and returns what ends this one's turn. A turn lasts through the
// requests to the remote that adding a store or replacing its token makes,
// which is why each name has its own.
func (r *Registry) turn(id string) (end func()) {
	r.mu.Lock()
	turn := r.turns[id]
	if turn == nil {
		turn = new(sync.Mutex)
		r.turns[id] = turn
	}
	r.mu.Unlock()
	turn.Lock()
	return turn.Unlock
}

// update changes the configuration as it is now, so that a change made to
// another store since the caller read it stays.
func (r *Registry) update(change func(*Config) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	config, err := Load(r.dir)
	if err != nil {
		return err
	}
	if err := change(&config); err != nil {
		return err
	}
	if err := config.write(r.dir); err != nil {
		return &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration could not be written"}
	}
	return nil
}

// Config is this Mac's stores, for a client that may not read their file.
func (r *Registry) Config(context.Context) (Config, error) { return Load(r.dir) }

// Backend returns the backend of the store named id, iCloud Keychain's for
// "" as well: a request that names no store comes from a command that knows
// only that one. A remote store's is returned only while the name still
// means target, the connection the request was made for: the owner may have
// confirmed a write to that project, not to one connected under the name
// since.
func (r *Registry) Backend(ctx context.Context, id, target string) (store.Backend, error) {
	if id == "" || id == ICloud {
		return r.icloud, nil
	}
	found, err := r.remote(ctx, id)
	if err != nil {
		return nil, err
	}
	if found.target != target {
		return nil, &store.Error{Kind: store.KindConflict, Message: "the store was connected to another project since this began; nothing was done"}
	}
	return found.backend, nil
}

func (r *Registry) cached(id string) (opened, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	found, ok := r.backends[id]
	return found, ok
}

func (r *Registry) remote(ctx context.Context, id string) (opened, error) {
	if found, ok := r.cached(id); ok {
		return found, nil
	}
	defer r.turn(id)()
	// Another request may have opened the store while this one waited.
	if found, ok := r.cached(id); ok {
		return found, nil
	}
	config, err := Load(r.dir)
	if err != nil {
		return opened{}, err
	}
	connection, err := config.named(id)
	if err != nil {
		return opened{}, err
	}
	text, err := r.local.LocalGet(ctx, tokenAccount(id))
	if store.IsKind(err, store.KindNotFound) {
		return opened{}, &store.Error{Kind: store.KindUnauthenticated, Message: "this Mac's Keychain no longer holds the store's access token"}
	}
	if err != nil {
		return opened{}, err
	}
	token, err := bitwarden.ParseToken(string(text))
	if err != nil {
		return opened{}, err
	}
	client := bitwarden.New(r.regions[connection.Region], token, r.http)
	found := opened{connection.Target(), bitwarden.NewBackend(client, connection.Project, snapshotPath(r.dir, id), &grants{local: r.local, account: grantsAccount(id)})}
	r.mu.Lock()
	r.backends[id] = found
	r.mu.Unlock()
	return found, nil
}

// Sync reads the store named id from Bitwarden now, instead of when its
// snapshot is next too old.
func (r *Registry) Sync(ctx context.Context, id string) error {
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain syncs on its own"}
	}
	found, err := r.remote(ctx, id)
	if err != nil {
		return err
	}
	return found.backend.Refresh(ctx)
}

// Add connects a Bitwarden project. It signs in with the token before it
// keeps anything, so a connection that exists has worked once.
func (r *Registry) Add(ctx context.Context, addition Addition) (Connection, error) {
	invalid := func(message string) (Connection, error) {
		return Connection{}, &store.Error{Kind: store.KindInvalid, Message: message}
	}
	if addition.Type != Bitwarden {
		return invalid("the only store type to add is bitwarden")
	}
	connection := Connection{ID: addition.ID, Type: Bitwarden, Region: addition.Region}
	if connection.ID == "" {
		connection.ID = Bitwarden
	}
	if connection.Region == "" {
		connection.Region = "us"
	}
	region, ok := r.regions[connection.Region]
	switch {
	case !idPattern.MatchString(connection.ID):
		return invalid("a store name starts with a lowercase ASCII letter or digit, followed by up to 31 lowercase letters, digits or '-'")
	case !ok:
		return invalid("the region is us or eu")
	}
	// The name stays free through the turn, which no other addition of it
	// shares.
	defer r.turn(connection.ID)()
	config, err := Load(r.dir)
	if err != nil {
		return Connection{}, err
	}
	if _, taken := config.Find(connection.ID); taken {
		return Connection{}, &store.Error{Kind: store.KindExists, Message: "a store with that name exists; choose another name"}
	}
	token, err := bitwarden.ParseToken(string(addition.Token))
	if err != nil {
		return Connection{}, err
	}
	client := bitwarden.New(region, token, r.http)
	if connection.Organization, err = client.Organization(ctx); err != nil {
		return Connection{}, err
	}
	projects, err := client.Projects(ctx)
	if err != nil {
		return Connection{}, err
	}
	project, err := chooseProject(projects, addition.Project)
	if err != nil {
		return Connection{}, err
	}
	connection.Project, connection.ProjectName = project.ID, project.Name
	if err := r.local.LocalSet(ctx, tokenAccount(connection.ID), addition.Token); err != nil {
		// The token may be kept though its write did not answer, and no
		// store would name it. The request's own time can be what ended
		// the write.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_ = r.local.LocalDelete(cleanup, tokenAccount(connection.ID))
		return Connection{}, err
	}
	err = r.update(func(config *Config) error {
		config.Stores = append(config.Stores, connection)
		return nil
	})
	if err != nil {
		_ = r.local.LocalDelete(ctx, tokenAccount(connection.ID))
		return Connection{}, err
	}
	return connection, nil
}

// SetToken replaces the access token of the store named id, as when the
// old one expired, once the new one signs in and reaches the same project.
// The store keeps its name, the default, and its entries' program access.
func (r *Registry) SetToken(ctx context.Context, id string, text []byte) error {
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain has no access token"}
	}
	defer r.turn(id)()
	config, err := Load(r.dir)
	if err != nil {
		return err
	}
	connection, err := config.named(id)
	if err != nil {
		return err
	}
	token, err := bitwarden.ParseToken(string(text))
	if err != nil {
		return err
	}
	client := bitwarden.New(r.regions[connection.Region], token, r.http)
	organization, err := client.Organization(ctx)
	if err != nil {
		return err
	}
	projects, err := client.Projects(ctx)
	if err != nil {
		return err
	}
	if organization != connection.Organization || !slices.ContainsFunc(projects, func(p bitwarden.Project) bool { return p.ID == connection.Project }) {
		return &store.Error{Kind: store.KindPermissionDenied, Message: "the token cannot read the store's project; a token for another project is another store"}
	}
	r.close(id)
	if err := r.local.LocalSet(ctx, tokenAccount(id), text); err != nil {
		return err
	}
	// The snapshot's key opens only with the token that read it.
	_ = os.RemoveAll(filepath.Dir(snapshotPath(r.dir, id)))
	return nil
}

// chooseProject finds the project wanted names by ID or by name, or the
// only one when it names none. A store is one project, so a machine account
// that reads several needs the owner's choice.
func chooseProject(projects []bitwarden.Project, wanted string) (bitwarden.Project, error) {
	matches := projects
	if wanted != "" {
		matches = slices.DeleteFunc(slices.Clone(projects), func(p bitwarden.Project) bool { return p.ID != wanted && p.Name != wanted })
	}
	switch {
	case len(projects) == 0:
		return bitwarden.Project{}, &store.Error{Kind: store.KindPermissionDenied, Message: "the machine account can read no project; give it access to one in Bitwarden"}
	case len(matches) == 0:
		return bitwarden.Project{}, &store.Error{Kind: store.KindNotFound, Message: "the machine account can read no project with that ID or name"}
	case len(matches) > 1 && wanted == "":
		return bitwarden.Project{}, &store.Error{Kind: store.KindInvalid, Message: "the machine account can read several projects; name one with --project"}
	case len(matches) > 1:
		return bitwarden.Project{}, &store.Error{Kind: store.KindConflict, Message: "several projects have that name; pass the project's ID"}
	}
	return matches[0], nil
}

// Remove forgets the store named id on this Mac: its token, program access,
// and snapshot. What Bitwarden holds stays.
func (r *Registry) Remove(ctx context.Context, id string) error {
	defer r.turn(id)()
	config, err := Load(r.dir)
	if err != nil {
		return err
	}
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain is built in"}
	}
	if _, err := config.named(id); err != nil {
		return err
	}
	r.close(id)
	// The token goes first and the store's name last: a removal that stops
	// halfway leaves a store that cannot sign in and can be removed again,
	// not a credential or a copy of the project that no store names.
	if err := r.local.LocalDelete(ctx, tokenAccount(id)); err != nil && !store.IsKind(err, store.KindNotFound) {
		return err
	}
	if err := r.local.LocalDelete(ctx, grantsAccount(id)); err != nil && !store.IsKind(err, store.KindNotFound) {
		return unfinished(cmp.Or(store.KindOf(err), store.KindKeychain), "its entries' program access could not be removed")
	}
	if err := os.RemoveAll(filepath.Dir(snapshotPath(r.dir, id))); err != nil {
		return unfinished(store.KindPrerequisite, "the copy of its entries kept on this Mac could not be deleted")
	}
	err = r.update(func(config *Config) error {
		config.Stores = slices.DeleteFunc(config.Stores, func(c Connection) bool { return c.ID == id })
		if config.Default == id {
			config.Default = ICloud
		}
		return nil
	})
	if err != nil {
		return unfinished(store.KindPrerequisite, "this Mac's store configuration could not be changed")
	}
	return nil
}

// unfinished is the failure of a removal after the store's token went.
func unfinished(kind store.Kind, step string) error {
	return &store.Error{Kind: kind, Message: "the store's access token was removed, but " + step + "; remove the store again"}
}

// close ends the backend the store named id has, so that a request that
// already holds it, as one waiting for the owner's approval, changes
// nothing of the connection that comes after.
func (r *Registry) close(id string) {
	r.mu.Lock()
	found, ok := r.backends[id]
	delete(r.backends, id)
	r.mu.Unlock()
	if ok {
		// Closing waits for the exchange the backend is in.
		found.backend.Close()
	}
}

// SetDefault makes the store named id the one commands use when they name
// none.
func (r *Registry) SetDefault(_ context.Context, id string) error {
	return r.update(func(config *Config) error {
		if _, err := config.named(id); err != nil {
			return err
		}
		config.Default = id
		return nil
	})
}

// grants keeps a store's grants as one Keychain item, a JSON list of secret
// IDs, read once: the registry's process is its only writer.
type grants struct {
	local   Local
	account string

	mu     sync.Mutex
	loaded bool
	ids    []string
}

func (g *grants) load(ctx context.Context) error {
	if g.loaded {
		return nil
	}
	data, err := g.local.LocalGet(ctx, g.account)
	switch {
	case store.IsKind(err, store.KindNotFound):
	case err != nil:
		return err
	case json.Unmarshal(data, &g.ids) != nil:
		// A list that cannot be read grants nothing.
		g.ids = nil
	}
	g.loaded = true
	return nil
}

func (g *grants) Granted(ctx context.Context) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.load(ctx); err != nil {
		return nil, err
	}
	return slices.Clone(g.ids), nil
}

func (g *grants) Set(ctx context.Context, id string, granted bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.load(ctx); err != nil {
		return err
	}
	if slices.Contains(g.ids, id) == granted {
		return nil
	}
	ids := slices.DeleteFunc(slices.Clone(g.ids), func(other string) bool { return other == id })
	if granted {
		ids = append(ids, id)
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if err := g.local.LocalSet(ctx, g.account, data); err != nil {
		// The item may hold either list now, so the next use reads it.
		g.loaded, g.ids = false, nil
		return err
	}
	g.ids = ids
	return nil
}
