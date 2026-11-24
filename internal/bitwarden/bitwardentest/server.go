// Package bitwardentest provides Bitwarden's identity service and Secrets
// Manager API in one test server, holding ciphertext as the real one does.
// It has its own cipher, so a client that reads what it stores agrees with
// a second implementation, not only with itself. Only test code imports it.
package bitwardentest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	Organization = "11111111-1111-4111-8111-111111111111"
	Project      = "22222222-2222-4222-8222-222222222222"
	// Token is the sample access token of Bitwarden's SDK tests, which no
	// server issued.
	Token = "0.ec2c1d46-6a4b-4751-a310-af9601317f2d.C2IgxjjLF7qSshsbwe8JGcbM075YXw:X8vbvA0bduihIDe/qrzIQQ=="
)

// Secret is a secret as the API takes and stores it, its fields encrypted.
type Secret struct {
	Key          string   `json:"key"`
	Value        string   `json:"value"`
	Note         string   `json:"note"`
	ProjectIDs   []string `json:"projectIds"`
	ValueChanged bool     `json:"valueChanged,omitempty"`
}

// ProjectInfo is a project the machine account can read.
type ProjectInfo struct{ ID, Name string }

// Server answers for one organization and the machine account of Token.
// A test sets its fields before the requests they concern.
type Server struct {
	*httptest.Server
	clientID, clientSecret string
	tokenKey, key          []byte

	mu       sync.Mutex
	Secrets  map[string]Secret // by ID
	Order    []string
	Projects []ProjectInfo
	writes   map[string]int // by ID, the source of revisions and of new IDs
	Sessions int
	Requests []string
	// Fail answers the next request to a route with a status, once.
	Fail map[string]int
	// Expired makes the API refuse every session issued so far.
	Expired int
	// Fetched lists the IDs whose values were asked for.
	Fetched []string
	// ReadIDs can change which secrets a get-by-ids request finds.
	ReadIDs func([]string) []string
	// Lost makes Project one the account cannot read.
	Lost bool
}

func New(t testing.TB) *Server {
	t.Helper()
	head, seed, _ := strings.Cut(Token, ":")
	parts := strings.Split(head, ".")
	secret, err := base64.StdEncoding.DecodeString(seed)
	if err != nil {
		t.Fatal(err)
	}
	prk := hmac.New(sha256.New, []byte("bitwarden-accesstoken"))
	prk.Write(secret)
	tokenKey, err := hkdf.Expand(sha256.New, prk.Sum(nil), "sm-access-token", 64)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 64)
	for i := range key {
		key[i] = byte(i)
	}
	s := &Server{
		clientID: parts[1], clientSecret: parts[2], tokenKey: tokenKey, key: key,
		Secrets: map[string]Secret{}, writes: map[string]int{}, Fail: map[string]int{},
		Projects: []ProjectInfo{{ID: Project, Name: "Maskey"}},
	}
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

func seal(key, plaintext []byte) string {
	block, _ := aes.NewCipher(key[:32])
	padded := append(bytes.Clone(plaintext), bytes.Repeat([]byte{byte(16 - len(plaintext)%16)}, 16-len(plaintext)%16)...)
	iv := make([]byte, 16)
	rand.Read(iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
	mac := hmac.New(sha256.New, key[32:])
	mac.Write(iv)
	mac.Write(padded)
	encode := base64.StdEncoding.EncodeToString
	return "2." + encode(iv) + "|" + encode(padded) + "|" + encode(mac.Sum(nil))
}

// Seal encrypts plaintext with the organization key.
func (s *Server) Seal(plaintext string) string { return seal(s.key, []byte(plaintext)) }

// Open decrypts a field the server holds.
func (s *Server) Open(text string) (string, error) {
	fields := strings.Split(strings.TrimPrefix(text, "2."), "|")
	if !strings.HasPrefix(text, "2.") || len(fields) != 3 {
		return "", errors.New("not a type 2 cipher string")
	}
	var raw [3][]byte
	for i, field := range fields {
		var err error
		if raw[i], err = base64.StdEncoding.DecodeString(field); err != nil {
			return "", err
		}
	}
	mac := hmac.New(sha256.New, s.key[32:])
	mac.Write(raw[0])
	mac.Write(raw[1])
	if !hmac.Equal(mac.Sum(nil), raw[2]) || len(raw[1]) == 0 || len(raw[1])%16 != 0 {
		return "", errors.New("the cipher string fails its check")
	}
	block, _ := aes.NewCipher(s.key[:32])
	cipher.NewCBCDecrypter(block, raw[0]).CryptBlocks(raw[1], raw[1])
	return string(raw[1][:len(raw[1])-int(raw[1][len(raw[1])-1])]), nil
}

// Count is how many requests reached route, such as "GET /projects/<id>".
func (s *Server) Count(route string) (n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Requests {
		if r == route {
			n++
		}
	}
	return n
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	route := r.Method + " " + r.URL.Path
	s.Requests = append(s.Requests, route)
	if status, ok := s.Fail[route]; ok {
		delete(s.Fail, route)
		w.WriteHeader(status)
		return
	}
	if route == "POST /connect/token" {
		s.signIn(w, r)
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if issued, err := strconv.Atoi(strings.SplitN(bearer, ".", 3)[0]); err != nil || issued <= s.Expired {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	project := func(p ProjectInfo) map[string]any {
		return map[string]any{"id": p.ID, "name": s.Seal(p.Name), "read": true, "write": true}
	}
	switch {
	case route == "GET /organizations/"+Organization+"/projects":
		data := []any{}
		for _, p := range s.Projects {
			if !s.Lost || p.ID != Project {
				data = append(data, project(p))
			}
		}
		reply(map[string]any{"data": data})
	case route == "GET /projects/"+Project && !s.Lost:
		reply(project(s.Projects[0]))
	case route == "GET /projects/"+Project+"/secrets":
		listed := []any{}
		for _, id := range s.Order {
			if s.Lost {
				break
			}
			secret := s.secret(id)
			delete(secret, "value")
			delete(secret, "note")
			listed = append(listed, secret)
		}
		reply(map[string]any{"secrets": listed})
	case route == "POST /secrets/get-by-ids":
		var request struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&request)
		s.Fetched = append(s.Fetched, request.IDs...)
		if s.ReadIDs != nil {
			request.IDs = s.ReadIDs(request.IDs)
		}
		var data []any
		for _, id := range request.IDs {
			if _, ok := s.Secrets[id]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// Bitwarden leaves out of this answer what the token may do
			// with a secret.
			secret := s.secret(id)
			delete(secret, "read")
			delete(secret, "write")
			data = append(data, secret)
		}
		reply(map[string]any{"data": data})
	case route == "POST /organizations/"+Organization+"/secrets":
		id := "33333333-3333-4333-8333-" + strconv.Itoa(100000000000+len(s.writes))
		s.Order = append(s.Order, id)
		s.store(w, r, id)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/secrets/"):
		s.store(w, r, strings.TrimPrefix(r.URL.Path, "/secrets/"))
	case route == "POST /secrets/delete":
		var ids []string
		_ = json.NewDecoder(r.Body).Decode(&ids)
		var data []any
		for _, id := range ids {
			delete(s.Secrets, id)
			s.Order = slices.DeleteFunc(s.Order, func(other string) bool { return other == id })
			data = append(data, map[string]any{"id": id, "error": nil})
		}
		reply(map[string]any{"data": data})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil || r.Form.Get("client_id") != s.clientID || r.Form.Get("client_secret") != s.clientSecret ||
		r.Form.Get("scope") != "api.secrets" || r.Form.Get("grant_type") != "client_credentials" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.Sessions++
	claims, _ := json.Marshal(map[string]string{"organization": Organization})
	payload, _ := json.Marshal(map[string][]byte{"encryptionKey": s.key})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":      strconv.Itoa(s.Sessions) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".signature",
		"expires_in":        3600,
		"token_type":        "Bearer",
		"encrypted_payload": seal(s.tokenKey, payload),
	})
}

func (s *Server) secret(id string) map[string]any {
	stored := s.Secrets[id]
	return map[string]any{
		"id": id, "key": stored.Key, "value": stored.Value, "note": stored.Note, "revisionDate": "rev-" + strconv.Itoa(s.writes[id]),
		"projects": []any{map[string]any{"id": stored.ProjectIDs[0]}}, "read": true, "write": true,
	}
}

func (s *Server) store(w http.ResponseWriter, r *http.Request, id string) {
	var request Secret
	if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.ProjectIDs) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.Secrets[id] = request
	s.writes[id]++
	_ = json.NewEncoder(w).Encode(s.secret(id))
}
