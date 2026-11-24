package stores

import (
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

	mu       sync.Mutex
	backends map[string]*bitwarden.Backend
}

func NewRegistry(dir string, icloud store.Backend, local Local, client *http.Client) *Registry {
	return &Registry{dir: dir, icloud: icloud, local: local, http: client, regions: bitwarden.Regions, backends: map[string]*bitwarden.Backend{}}
}

// cleanupTimeout bounds the removal of a token whose store was not added.
const cleanupTimeout = 30 * time.Second

func tokenAccount(id string) string  { return "token/" + id }
func grantsAccount(id string) string { return "grants/" + id }

// Backend returns the backend of the store named id, iCloud Keychain's for
// "" as well: a request that names no store comes from a command that knows
// only that one.
func (r *Registry) Backend(ctx context.Context, id string) (store.Backend, error) {
	if id == "" || id == ICloud {
		return r.icloud, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remote(ctx, id)
}

func (r *Registry) remote(ctx context.Context, id string) (*bitwarden.Backend, error) {
	if backend, ok := r.backends[id]; ok {
		return backend, nil
	}
	config, err := Load(r.dir)
	if err != nil {
		return nil, err
	}
	connection, err := config.named(id)
	if err != nil {
		return nil, err
	}
	text, err := r.local.LocalGet(ctx, tokenAccount(id))
	if store.IsKind(err, store.KindNotFound) {
		return nil, &store.Error{Kind: store.KindUnauthenticated, Message: "this Mac's Keychain no longer holds the store's access token"}
	}
	if err != nil {
		return nil, err
	}
	token, err := bitwarden.ParseToken(string(text))
	if err != nil {
		return nil, err
	}
	client := bitwarden.New(r.regions[connection.Region], token, r.http)
	backend := bitwarden.NewBackend(client, connection.Project, snapshotPath(r.dir, id), &grants{local: r.local, account: grantsAccount(id)})
	r.backends[id] = backend
	return backend, nil
}

// Sync reads the store named id from Bitwarden now, instead of when its
// snapshot is next too old.
func (r *Registry) Sync(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain syncs on its own"}
	}
	backend, err := r.remote(ctx, id)
	if err != nil {
		return err
	}
	return backend.Refresh(ctx)
}

// Add connects a Bitwarden project. It signs in with the token before it
// keeps anything, so a connection that exists has worked once.
func (r *Registry) Add(ctx context.Context, addition Addition) (Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	config.Stores = append(config.Stores, connection)
	if err := config.write(r.dir); err != nil {
		_ = r.local.LocalDelete(ctx, tokenAccount(connection.ID))
		return Connection{}, &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration could not be written"}
	}
	return connection, nil
}

// SetToken replaces the access token of the store named id, as when the
// old one expired, once the new one signs in and reaches the same project.
// The store keeps its name, the default, and its entries' program access.
func (r *Registry) SetToken(ctx context.Context, id string, text []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain has no access token"}
	}
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
	// The token goes first: a removal that stops halfway leaves a store
	// that cannot sign in, not a credential without a store.
	for _, account := range []string{tokenAccount(id), grantsAccount(id)} {
		if err := r.local.LocalDelete(ctx, account); err != nil && !store.IsKind(err, store.KindNotFound) {
			return err
		}
	}
	config.Stores = slices.DeleteFunc(config.Stores, func(c Connection) bool { return c.ID == id })
	if config.Default == id {
		config.Default = ICloud
	}
	if err := config.write(r.dir); err != nil {
		return &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration could not be written"}
	}
	_ = os.RemoveAll(filepath.Dir(snapshotPath(r.dir, id)))
	return nil
}

// close ends the backend the store named id has, so that a request that
// already holds it, as one waiting for the owner's approval, changes
// nothing of the connection that comes after.
func (r *Registry) close(id string) {
	if backend, ok := r.backends[id]; ok {
		backend.Close()
		delete(r.backends, id)
	}
}

// SetDefault makes the store named id the one commands use when they name
// none.
func (r *Registry) SetDefault(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	config, err := Load(r.dir)
	if err != nil {
		return err
	}
	if _, err := config.named(id); err != nil {
		return err
	}
	config.Default = id
	if err := config.write(r.dir); err != nil {
		return &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration could not be written"}
	}
	return nil
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
