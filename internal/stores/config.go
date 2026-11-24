// Package stores keeps this Mac's stores: iCloud Keychain, which is built
// in, and the connections to remote stores the owner added. A connection has
// a name the owner chose, which commands select it by, and a type, which
// says what it connects to.
package stores

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Entwining/maskey/internal/bitwarden"
	"github.com/Entwining/maskey/internal/store"
)

const (
	// ICloud is the name and the type of the built-in store.
	ICloud = "icloud"
	// Bitwarden is the type of a Bitwarden Secrets Manager project, and the
	// name its first connection is given unless the owner chooses another.
	Bitwarden = "bitwarden"
)

// idPattern leaves out ":", which separates a store from an entry where a
// reference names both.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Connection is one store. Region, Organization, and Project say what a
// Bitwarden connection reads and writes; ProjectName is the project's name
// when the connection was added, for people.
type Connection struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Region       string `json:"region,omitempty"`
	Organization string `json:"organization,omitempty"`
	Project      string `json:"project,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
}

// Config is this Mac's stores besides iCloud Keychain, and the store a
// command uses when it names none.
type Config struct {
	Default string       `json:"default"`
	Stores  []Connection `json:"stores"`
}

// Dir is where this Mac keeps its stores' configuration and snapshots.
func Dir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, store.Service), nil
}

// Target is what the connection reads and writes, whatever its name. A
// request for a store's entries carries the target its sender resolved the
// name to, so that it reaches no other connection given that name since.
func (c Connection) Target() string {
	return strings.Join([]string{c.Type, c.Region, c.Organization, c.Project}, "/")
}

// ErrDenied is why Load fails in a process that may not read the
// configuration, as inside a sandbox: the Maskey service, which writes the
// file, can read it for that process.
var ErrDenied error = &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}

// ErrNoStore distinguishes a missing local store from a missing remote project.
var ErrNoStore = &store.Error{Kind: store.KindNotFound, Message: "this Mac has no store with that name"}

func configPath(dir string) string { return filepath.Join(dir, "stores.json") }

// snapshotPath is the snapshot of the store id, in a directory of its own
// that removing the store removes.
func snapshotPath(dir, id string) string { return filepath.Join(dir, "stores", id, "snapshot") }

// Load reads the configuration in dir. Without one, iCloud Keychain is the
// only store.
func Load(dir string) (Config, error) {
	config := Config{Default: ICloud}
	data, err := os.ReadFile(configPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return config, nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return Config{Default: ICloud}, ErrDenied
	}
	if err == nil {
		err = json.Unmarshal(data, &config)
	}
	if err != nil {
		return Config{Default: ICloud}, &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}
	}
	if _, ok := config.Find(config.Default); !ok {
		config.Default = ICloud
	}
	return config, nil
}

// All lists every store by name, so that every listing has one order on
// every Mac, whatever order its stores were added in.
func (c Config) All() []Connection {
	return byID(append([]Connection{{ID: ICloud, Type: ICloud}}, c.Stores...))
}

// Remote lists the stores besides iCloud Keychain, in the order of All.
func (c Config) Remote() []Connection { return byID(slices.Clone(c.Stores)) }

func byID(connections []Connection) []Connection {
	slices.SortFunc(connections, func(a, b Connection) int { return strings.Compare(a.ID, b.ID) })
	return connections
}

// Find returns the store named id.
func (c Config) Find(id string) (Connection, bool) {
	all := c.All()
	i := slices.IndexFunc(all, func(connection Connection) bool { return connection.ID == id })
	if i < 0 {
		return Connection{}, false
	}
	return all[i], true
}

// Resolve returns the store a command means by id, the default for "".
func (c Config) Resolve(id string) (Connection, error) {
	if id == "" {
		id = c.Default
	}
	return c.Named(id)
}

// Named returns the store an operation on stores names, which unlike a
// command on entries has no default to mean.
func (c Config) Named(id string) (Connection, error) {
	if connection, ok := c.Find(id); ok && id != "" {
		return connection, nil
	}
	return Connection{}, ErrNoStore
}

// Connection validates an addition's local fields and fills in their defaults.
func (a Addition) Connection() (Connection, error) {
	connection := Connection{ID: a.ID, Type: a.Type, Region: a.Region}
	if connection.ID == "" {
		connection.ID = Bitwarden
	}
	if connection.Region == "" {
		connection.Region = "us"
	}
	switch {
	case connection.Type != Bitwarden:
		return Connection{}, &store.Error{Kind: store.KindInvalid, Message: "the only store type to add is bitwarden"}
	case !idPattern.MatchString(connection.ID):
		return Connection{}, &store.Error{Kind: store.KindInvalid, Message: "a store name starts with a lowercase ASCII letter or digit, followed by up to 31 lowercase letters, digits or '-'"}
	}
	if _, ok := bitwarden.Regions[connection.Region]; !ok {
		return Connection{}, &store.Error{Kind: store.KindInvalid, Message: "the region is us or eu"}
	}
	return connection, nil
}

func (c Config) CheckNewName(id string) error {
	if _, taken := c.Find(id); taken {
		return &store.Error{Kind: store.KindExists, Message: "a store with that name exists; choose another name"}
	}
	return nil
}

func CheckTokenStore(id string) error {
	if id == ICloud {
		return &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain has no access token"}
	}
	return nil
}

// Removable returns the local connection a removal would forget.
func (c Config) Removable(id string) (Connection, error) {
	if id == ICloud {
		return Connection{}, &store.Error{Kind: store.KindInvalid, Message: "iCloud Keychain is built in"}
	}
	return c.Named(id)
}

func (c Config) write(dir string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "stores.json.*")
	if err != nil {
		return err
	}
	_, err = temporary.Write(append(data, '\n'))
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), configPath(dir))
	}
	if err != nil {
		os.Remove(temporary.Name())
	}
	return err
}
