package bitwarden

import (
	"context"
	"net/http"
	"unicode/utf8"

	"github.com/Entwining/maskey/internal/store"
)

// Project is a project the machine account can see.
type Project struct {
	ID   string
	Name string
}

// Header is a secret without its value and note, as a listing returns it.
// Revision changes with every write and has no other meaning.
type Header struct {
	ID       string
	Project  string
	Key      string
	Revision string
}

// Secret is a whole secret, read in one request.
type Secret struct {
	Header
	Value []byte
	Note  string
}

type projectReply struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type secretReply struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Value        string `json:"value"`
	Note         string `json:"note"`
	RevisionDate string `json:"revisionDate"`
	Projects     []struct {
		ID string `json:"id"`
	} `json:"projects"`
}

type secretRequest struct {
	Key          string   `json:"key"`
	Value        string   `json:"value"`
	Note         string   `json:"note"`
	ProjectIDs   []string `json:"projectIds"`
	ValueChanged bool     `json:"valueChanged,omitempty"`
}

// Projects lists the projects the machine account can see.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	s, err := c.signedIn(ctx, nil)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data []projectReply `json:"data"`
	}
	if err := c.call(ctx, http.MethodGet, "/organizations/"+s.organization+"/projects", nil, &reply, false); err != nil {
		return nil, err
	}
	projects := make([]Project, len(reply.Data))
	for i, p := range reply.Data {
		name, err := c.open(p.Name)
		if err != nil {
			return nil, err
		}
		projects[i] = Project{ID: p.ID, Name: string(name)}
	}
	return projects, nil
}

// Project reads one project. Unlike an empty listing of its secrets, its
// failure shows that the account cannot read the project.
func (c *Client) Project(ctx context.Context, id string) (Project, error) {
	var p projectReply
	if err := c.call(ctx, http.MethodGet, "/projects/"+id, nil, &p, false); err != nil {
		return Project{}, err
	}
	name, err := c.open(p.Name)
	if err != nil {
		return Project{}, err
	}
	return Project{ID: p.ID, Name: string(name)}, nil
}

// Index lists a project's secrets without reading a value, which Bitwarden
// does not record as a retrieval.
func (c *Client) Index(ctx context.Context, project string) ([]Header, error) {
	var reply struct {
		Secrets []secretReply `json:"secrets"`
	}
	if err := c.call(ctx, http.MethodGet, "/projects/"+project+"/secrets", nil, &reply, false); err != nil {
		return nil, err
	}
	headers := make([]Header, len(reply.Secrets))
	for i, r := range reply.Secrets {
		header, err := c.header(r)
		if err != nil {
			return nil, err
		}
		headers[i] = header
	}
	return headers, nil
}

// Read reads whole secrets by ID, in the order of ids. Bitwarden answers
// not_found for all of them when one is missing or unreadable.
func (c *Client) Read(ctx context.Context, ids []string) ([]Secret, error) {
	var reply struct {
		Data []secretReply `json:"data"`
	}
	request := struct {
		IDs []string `json:"ids"`
	}{ids}
	if err := c.call(ctx, http.MethodPost, "/secrets/get-by-ids", request, &reply, false); err != nil {
		return nil, err
	}
	byID := make(map[string]Secret, len(reply.Data))
	for _, r := range reply.Data {
		secret, err := c.secret(r)
		if err != nil {
			return nil, err
		}
		byID[secret.ID] = secret
	}
	secrets := make([]Secret, len(ids))
	for i, id := range ids {
		secret, ok := byID[id]
		if !ok || len(byID) != len(ids) {
			return nil, remoteError("Bitwarden answered with other secrets than the ones asked for")
		}
		secrets[i] = secret
	}
	return secrets, nil
}

// Create adds a secret to project. Bitwarden does not keep keys unique: two
// secrets of one project can share a key.
func (c *Client) Create(ctx context.Context, project, name string, value []byte, note string) (Header, error) {
	s, request, err := c.seal(ctx, project, name, value, note)
	if err != nil {
		return Header{}, err
	}
	var reply secretReply
	if err := c.call(ctx, http.MethodPost, "/organizations/"+s.organization+"/secrets", request, &reply, true); err != nil {
		return Header{}, err
	}
	return c.written(reply)
}

// Update replaces a secret's key, value and note in one request. Bitwarden
// takes no precondition, so it overwrites whatever another writer stored
// since this one read the secret. valueChanged says whether value differs
// from the stored one, which Bitwarden records as a new version.
func (c *Client) Update(ctx context.Context, id, project, name string, value []byte, note string, valueChanged bool) (Header, error) {
	_, request, err := c.seal(ctx, project, name, value, note)
	if err != nil {
		return Header{}, err
	}
	request.ValueChanged = valueChanged
	var reply secretReply
	if err := c.call(ctx, http.MethodPut, "/secrets/"+id, request, &reply, true); err != nil {
		return Header{}, err
	}
	return c.written(reply)
}

// Delete moves a secret to Bitwarden's trash. Bitwarden deletes by ID alone:
// the secret's project is no part of the request.
func (c *Client) Delete(ctx context.Context, id string) error {
	var reply struct {
		Data []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"data"`
	}
	if err := c.call(ctx, http.MethodPost, "/secrets/delete", []string{id}, &reply, true); err != nil {
		return err
	}
	switch {
	case len(reply.Data) != 1 || reply.Data[0].ID != id:
		return uncertainError()
	case reply.Data[0].Error != "":
		return &store.Error{Kind: store.KindPermissionDenied, Message: "the machine account may not delete the secret"}
	}
	return nil
}

// seal encrypts a secret's fields for a write. Bitwarden's other clients
// read every field as text, so a value must be UTF-8.
func (c *Client) seal(ctx context.Context, project, name string, value []byte, note string) (*session, secretRequest, error) {
	if !utf8.Valid(value) {
		return nil, secretRequest{}, &store.Error{Kind: store.KindInvalid, Message: "a value stored in Bitwarden must be UTF-8 text"}
	}
	s, err := c.signedIn(ctx, nil)
	if err != nil {
		return nil, secretRequest{}, err
	}
	return s, secretRequest{
		Key:        s.key.encrypt([]byte(name)),
		Value:      s.key.encrypt(value),
		Note:       s.key.encrypt([]byte(note)),
		ProjectIDs: []string{project},
	}, nil
}

// written reads the answer to a write, which the server has performed: an
// answer that cannot be read leaves what it stored unknown.
func (c *Client) written(reply secretReply) (Header, error) {
	header, err := c.header(reply)
	if err != nil {
		return Header{}, uncertainError()
	}
	return header, nil
}

func (c *Client) header(r secretReply) (Header, error) {
	name, err := c.open(r.Key)
	if err != nil {
		return Header{}, err
	}
	header := Header{ID: r.ID, Key: string(name), Revision: r.RevisionDate}
	if len(r.Projects) > 0 {
		header.Project = r.Projects[0].ID
	}
	return header, nil
}

func (c *Client) secret(r secretReply) (Secret, error) {
	header, err := c.header(r)
	if err != nil {
		return Secret{}, err
	}
	value, err := c.open(r.Value)
	if err != nil {
		return Secret{}, err
	}
	note, err := c.open(r.Note)
	if err != nil {
		return Secret{}, err
	}
	return Secret{Header: header, Value: value, Note: string(note)}, nil
}

// open decrypts a field with the organization key of the session that is
// current, the one the field's request just used.
func (c *Client) open(text string) ([]byte, error) {
	c.mu.Lock()
	s := c.session
	c.mu.Unlock()
	if s == nil {
		return nil, remoteError("the Bitwarden session ended before its answer was read")
	}
	plaintext, err := s.key.decrypt(text)
	if err != nil {
		return nil, remoteError("a Bitwarden secret could not be decrypted: " + err.Error())
	}
	return plaintext, nil
}
