package bitwarden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// snapshot is a project's Maskey entries as this Mac last read them from
// Bitwarden, which lets it list and read them without the network.
type snapshot struct {
	Project   string    `json:"project"`
	Refreshed time.Time `json:"refreshed"`
	Secrets   []Secret  `json:"secrets"`
}

// snapshotFile is a snapshot on disk: sealed with the organization key,
// beside that key as Bitwarden answered it at sign-in, which only the access
// token opens. The file is ciphertext to whoever lacks the token.
type snapshotFile struct {
	Version int    `json:"v"`
	Payload string `json:"payload"`
	Sealed  string `json:"sealed"`
}

const snapshotVersion = 2

// writeSnapshot replaces the file at path with s in one rename, so a reader
// never sees part of it. It seals s with the session of the exchange s comes
// from, also once that session's bearer expired: signing in again would be a
// request to Bitwarden, which the backend never makes while it holds its
// state.
func (c *Client) writeSnapshot(path string, s *snapshot) error {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	plaintext, err := json.Marshal(s)
	if err != nil {
		return err
	}
	file, err := json.Marshal(snapshotFile{Version: snapshotVersion, Payload: session.payload, Sealed: session.key.encrypt(plaintext)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = temporary.Write(file)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), path)
	}
	if err != nil {
		os.Remove(temporary.Name())
	}
	return err
}

// readSnapshot opens the snapshot of project at path with the access token
// alone. A file that is missing, of another version, sealed for another
// token, or of another project is no snapshot.
func (c *Client) readSnapshot(path, project string) *snapshot {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file snapshotFile
	if json.Unmarshal(data, &file) != nil || file.Version != snapshotVersion {
		return nil
	}
	organizationKey, err := c.token.organizationKey(file.Payload)
	if err != nil {
		return nil
	}
	plaintext, err := organizationKey.decrypt(file.Sealed)
	if err != nil {
		return nil
	}
	var s snapshot
	if json.Unmarshal(plaintext, &s) != nil || s.Project != project {
		return nil
	}
	return &s
}
