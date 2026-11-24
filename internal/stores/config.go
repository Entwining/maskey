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
	connection, ok := c.Find(id)
	if !ok {
		return Connection{}, &store.Error{Kind: store.KindNotFound, Message: "this Mac has no store with that name"}
	}
	return connection, nil
}

// named returns the store an operation on stores names, which unlike a
// command on entries has no default to mean.
func (c Config) named(id string) (Connection, error) {
	if id == "" {
		return Connection{}, &store.Error{Kind: store.KindNotFound, Message: "this Mac has no store with that name"}
	}
	return c.Resolve(id)
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
