package bitwarden

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// Region is one Bitwarden cloud: the API and the identity service that signs
// machine accounts in. A token is only ever sent to its region's pair.
type Region struct {
	API      string
	Identity string
}

// Regions are Bitwarden's clouds by the name its documentation gives them.
var Regions = map[string]Region{
	"us": {API: "https://api.bitwarden.com", Identity: "https://identity.bitwarden.com"},
	"eu": {API: "https://api.bitwarden.eu", Identity: "https://identity.bitwarden.eu"},
}

// responseLimit bounds a response body. A project of thousands of secrets at
// Bitwarden's size limits stays far below it.
const responseLimit = 64 << 20

// Client talks to one region as one machine account. It signs in when first
// used and again when the session ends; the session and the organization key
// stay in memory.
type Client struct {
	region Region
	token  *Token
	http   *http.Client
	now    func() time.Time

	mu      sync.Mutex
	session *session
}

type session struct {
	bearer       string
	expires      time.Time
	organization string
	key          key
	// payload is the organization key as the sign-in answered it, which
	// only the access token opens.
	payload string
}

func New(region Region, token *Token, client *http.Client) *Client {
	return &Client{region: region, token: token, http: client, now: time.Now}
}

// Organization is the ID of the organization the machine account belongs to.
func (c *Client) Organization(ctx context.Context) (string, error) {
	s, err := c.signedIn(ctx, nil)
	if err != nil {
		return "", err
	}
	return s.organization, nil
}

// signedIn returns the current session, signing in when there is none, it
// has expired, or it is stale, the session a request was just refused with.
func (c *Client) signedIn(ctx context.Context, stale *session) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil && c.session != stale && c.now().Before(c.session.expires) {
		return c.session, nil
	}
	c.session = nil
	s, err := c.signIn(ctx)
	if err != nil {
		return nil, err
	}
	c.session = s
	return s, nil
}

func (c *Client) signIn(ctx context.Context) (*session, error) {
	form := url.Values{
		"scope":         {"api.secrets"},
		"client_id":     {c.token.id},
		"client_secret": {c.token.secret},
		"grant_type":    {"client_credentials"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.region.Identity+"/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	status, body, err := c.exchange(request)
	switch {
	case err != nil:
		return nil, networkError(ctx, err)
	case status == http.StatusBadRequest || status == http.StatusUnauthorized:
		// Identity answers invalid_client with 400.
		return nil, &store.Error{Kind: store.KindUnauthenticated, Message: "Bitwarden refused the access token"}
	case status != http.StatusOK:
		return nil, statusError(status)
	}
	var reply struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		EncryptedPayload string `json:"encrypted_payload"`
	}
	if err := json.Unmarshal(body, &reply); err != nil || reply.AccessToken == "" {
		return nil, remoteError("Bitwarden's sign-in answer could not be read")
	}
	organizationKey, err := c.token.organizationKey(reply.EncryptedPayload)
	if err != nil {
		return nil, err
	}
	organization, err := organizationClaim(reply.AccessToken)
	if err != nil {
		return nil, err
	}
	// A minute's margin keeps a request from leaving with a session that
	// ends before it arrives.
	expires := c.now().Add(time.Duration(reply.ExpiresIn)*time.Second - time.Minute)
	return &session{bearer: reply.AccessToken, expires: expires, organization: organization, key: organizationKey, payload: reply.EncryptedPayload}, nil
}

// organizationKey opens the payload of a sign-in answer, which holds the
// key every field of the organization's secrets is encrypted with.
func (t *Token) organizationKey(payload string) (key, error) {
	opened, err := t.key.decrypt(payload)
	switch {
	case errors.Is(err, errCipherMAC):
		// Bitwarden checks only the client secret, so a token whose key half
		// is wrong signs in and fails here.
		return key{}, &store.Error{Kind: store.KindInvalid, Message: "the access token has an encryption key that does not open Bitwarden's sign-in answer"}
	case err != nil:
		return key{}, remoteError("Bitwarden's sign-in answer could not be decrypted: " + err.Error())
	}
	var keys struct {
		EncryptionKey []byte `json:"encryptionKey"`
	}
	if err := json.Unmarshal(opened, &keys); err != nil || len(keys.EncryptionKey) != len(key{}) {
		return key{}, remoteError("Bitwarden's sign-in answer holds no usable organization key")
	}
	return key(keys.EncryptionKey), nil
}

// organizationClaim reads the organization ID from the session token. The
// token came over TLS from the identity service, so its signature is the
// API's to check, not this client's.
func organizationClaim(bearer string) (string, error) {
	parts := strings.Split(bearer, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Organization string `json:"organization"`
			}
			if json.Unmarshal(payload, &claims) == nil && uuidPattern.MatchString(claims.Organization) {
				return strings.ToLower(claims.Organization), nil
			}
		}
	}
	return "", remoteError("Bitwarden's session names no organization")
}

// call sends one API request and decodes its answer into out. A write is a
// request that changes a secret: once it may have left, only an answer that
// refuses it shows it did not happen, so a lost or failed answer is
// uncertain rather than a reason to repeat it.
func (c *Client) call(ctx context.Context, method, path string, in, out any, write bool) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	var refused *session
	for {
		s, err := c.signedIn(ctx, refused)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, method, c.region.API+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+s.bearer)
		request.Header.Set("Accept", "application/json")
		if in != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		status, body, err := c.exchange(request)
		switch {
		case err != nil && write && !neverSent(err):
			return uncertainError()
		case err != nil:
			return networkError(ctx, err)
		case status == http.StatusUnauthorized && refused == nil:
			// The API refused the session before acting, so signing in
			// again and repeating is safe for a write too.
			refused = s
			continue
		case status == http.StatusUnauthorized:
			return &store.Error{Kind: store.KindUnauthenticated, Message: "Bitwarden refused the session it just issued"}
		case status >= 500 && write:
			return uncertainError()
		case status < 200 || status > 299:
			return statusError(status)
		}
		if err := json.Unmarshal(body, out); err != nil {
			if write {
				return uncertainError()
			}
			return remoteError("Bitwarden's answer could not be read")
		}
		return nil
	}
}

// exchange performs request and reads its whole body. The body is never part
// of an error: an answer can quote the request.
func (c *Client) exchange(request *http.Request) (int, []byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	switch {
	case err != nil:
		return 0, nil, err
	case len(body) > responseLimit:
		return 0, nil, errors.New("the answer is too large")
	}
	return response.StatusCode, body, nil
}

// neverSent reports whether err shows that no connection was made, so the
// request cannot have reached the server.
func neverSent(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	return errors.As(err, &dns) || errors.As(err, &op) && op.Op == "dial"
}

func networkError(ctx context.Context, err error) error {
	// A deadline that passes is the network's failure, not the caller's
	// choice.
	if errors.Is(ctx.Err(), context.Canceled) {
		return &store.Error{Kind: store.KindCancelled, Message: "the request to Bitwarden was cancelled"}
	}
	return &store.Error{Kind: store.KindNetwork, Message: "Bitwarden could not be reached"}
}

func uncertainError() error {
	return &store.Error{Kind: store.KindUncertain, Message: "Bitwarden did not confirm the write"}
}

func remoteError(message string) error {
	return &store.Error{Kind: store.KindRemote, Message: message}
}

func statusError(status int) error {
	switch status {
	case http.StatusBadRequest:
		return &store.Error{Kind: store.KindInvalid, Message: "Bitwarden rejected the request"}
	case http.StatusForbidden:
		return &store.Error{Kind: store.KindPermissionDenied, Message: "the machine account may not do this"}
	case http.StatusNotFound:
		// Bitwarden answers 404 for what the account may not see as well.
		return &store.Error{Kind: store.KindNotFound, Message: "the secret or project does not exist, or the machine account cannot see it"}
	case http.StatusTooManyRequests:
		return &store.Error{Kind: store.KindRateLimited, Message: "Bitwarden is limiting requests"}
	}
	return remoteError("Bitwarden answered HTTP " + strconv.Itoa(status))
}
