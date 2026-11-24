package bitwarden

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/bitwarden/bitwardentest"
	"github.com/Entwining/maskey/internal/store"
)

const (
	organization = bitwardentest.Organization
	project      = bitwardentest.Project
)

type fakeServer = bitwardentest.Server

func newFake(t *testing.T) (*fakeServer, *Client) {
	t.Helper()
	fake := bitwardentest.New(t)
	return fake, clientOf(fake, vectorTokenParsed(t))
}

func vectorTokenParsed(t *testing.T) *Token {
	t.Helper()
	token, err := ParseToken(vectorToken)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func clientOf(fake *fakeServer, token *Token) *Client {
	return New(Region{API: fake.URL, Identity: fake.URL}, token, fake.Client())
}

func TestSecretsRoundTrip(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	value := []byte("line one\r\nline two\n")

	created, err := client.Create(ctx, project, "github/GITHUB_TOKEN#token", value, " a note ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Create(ctx, project, "github/OTHER#token", []byte("other"), ""); err != nil {
		t.Fatal(err)
	}
	for _, stored := range fake.Secrets {
		for _, field := range []string{stored.Key, stored.Value, stored.Note} {
			if !strings.HasPrefix(field, "2.") || strings.Contains(field, "GITHUB") {
				t.Fatalf("the server holds a field that is not ciphertext: %s", field)
			}
		}
	}

	index, err := client.Index(ctx, project)
	if err != nil || len(index) != 2 || index[0] != created {
		t.Fatalf("index %+v, %v; created %+v", index, err, created)
	}
	read, err := client.Read(ctx, []string{index[1].ID, index[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if read[1].Header != created || string(read[1].Value) != string(value) || read[1].Note != " a note " || read[0].Key != "github/OTHER#token" {
		t.Errorf("read %+v", read)
	}

	updated, err := client.Update(ctx, created.ID, project, "github/GITHUB_TOKEN#password", []byte("new"), "", true)
	if err != nil || updated.ID != created.ID || updated.Key != "github/GITHUB_TOKEN#password" || updated.Revision == created.Revision {
		t.Fatalf("updated %+v, %v", updated, err)
	}
	if !fake.Secrets[created.ID].ValueChanged {
		t.Error("the update did not tell Bitwarden that the value changed")
	}
	if err := client.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(ctx, []string{created.ID}); !store.IsKind(err, store.KindNotFound) {
		t.Errorf("reading a deleted secret: %v, want not_found", err)
	}
	if got := fake.Count("POST /connect/token"); got != 1 {
		t.Errorf("signed in %d times, want once", got)
	}
}

func TestProjects(t *testing.T) {
	_, client := newFake(t)
	want := Project{ID: project, Name: "Maskey"}
	projects, err := client.Projects(context.Background())
	if err != nil || len(projects) != 1 || projects[0] != want {
		t.Fatalf("projects %+v, %v", projects, err)
	}
	if one, err := client.Project(context.Background(), project); err != nil || one != want {
		t.Errorf("project %+v, %v", one, err)
	}
	if id, err := client.Organization(context.Background()); err != nil || id != organization {
		t.Errorf("organization %s, %v", id, err)
	}
}

func TestSignsInAgainWhenTheSessionEnds(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, project, "a/B#token", []byte("v"), ""); err != nil {
		t.Fatal(err)
	}
	// The API refuses the session, as after a revoked or ended one.
	fake.Expired = 1
	if _, err := client.Create(ctx, project, "a/C#token", []byte("v"), ""); err != nil {
		t.Fatalf("a refused session was not replaced: %v", err)
	}
	if len(fake.Order) != 2 {
		t.Errorf("%d secrets after a write repeated on a new session, want 2", len(fake.Order))
	}
	// The client's own clock ends a session before the server refuses it.
	client.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := client.Index(ctx, project); err != nil {
		t.Fatal(err)
	}
	if got := fake.Count("POST /connect/token"); got != 3 {
		t.Errorf("signed in %d times, want 3", got)
	}
	// A session refused right after sign-in is not retried forever.
	fake.Expired = 1 << 30
	if _, err := client.Index(ctx, project); !store.IsKind(err, store.KindUnauthenticated) {
		t.Errorf("got %v, want unauthenticated", err)
	}
}

func TestRefusedTokenIsUnauthenticated(t *testing.T) {
	fake, client := newFake(t)
	other, _ := ParseToken(strings.Replace(vectorToken, "C2Ig", "XXXX", 1))
	client.token = other
	if _, err := client.Projects(context.Background()); !store.IsKind(err, store.KindUnauthenticated) {
		t.Errorf("got %v, want unauthenticated", err)
	}
	if got := fake.Count("POST /connect/token"); got != 1 {
		t.Errorf("tried a refused token %d times, want once", got)
	}
}

func TestTokenWithAnotherKeyIsInvalid(t *testing.T) {
	_, client := newFake(t)
	head, _, _ := strings.Cut(vectorToken, ":")
	other, err := ParseToken(head + ":AAAAAAAAAAAAAAAAAAAAAA==")
	if err != nil {
		t.Fatal(err)
	}
	client.token = other
	// Bitwarden accepts the client secret, so only the answer shows the
	// token's other half is wrong.
	if _, err := client.Projects(context.Background()); !store.IsKind(err, store.KindInvalid) {
		t.Errorf("got %v, want invalid", err)
	}
}

func TestStatusKinds(t *testing.T) {
	const read, write = "GET /projects/" + project + "/secrets", "POST /organizations/" + organization + "/secrets"
	for _, test := range []struct {
		route  string
		status int
		want   store.Kind
	}{
		{read, http.StatusForbidden, store.KindPermissionDenied},
		{read, http.StatusNotFound, store.KindNotFound},
		{read, http.StatusTooManyRequests, store.KindRateLimited},
		{read, http.StatusBadGateway, store.KindRemote},
		{write, http.StatusForbidden, store.KindPermissionDenied},
		{write, http.StatusBadRequest, store.KindInvalid},
		// A failing server may have stored the secret before it failed.
		{write, http.StatusBadGateway, store.KindUncertain},
		{"POST /connect/token", http.StatusTooManyRequests, store.KindRateLimited},
		{"POST /connect/token", http.StatusBadGateway, store.KindRemote},
	} {
		fake, client := newFake(t)
		fake.Fail[test.route] = test.status
		var err error
		if test.route == write {
			_, err = client.Create(context.Background(), project, "a/B#token", []byte("v"), "")
		} else {
			_, err = client.Index(context.Background(), project)
		}
		if store.KindOf(err) != test.want {
			t.Errorf("%s answering %d: got %v, want %s", test.route, test.status, err, test.want)
		}
		if test.route == write && fake.Count(write) != 1 {
			t.Errorf("a write answered %d was sent %d times", test.status, fake.Count(write))
		}
	}
}

// dropAfterSignIn serves sign-in and then closes every connection without an
// answer, as a network that fails after the request left.
func dropAfterSignIn(t *testing.T) *Client {
	t.Helper()
	fake, _ := newFake(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connect/token" {
			fake.ServeHTTP(w, r)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(server.Close)
	return New(Region{API: server.URL, Identity: server.URL}, vectorTokenParsed(t), server.Client())
}

func TestLostAnswerIsUncertainOnlyForAWrite(t *testing.T) {
	client := dropAfterSignIn(t)
	ctx := context.Background()
	if _, err := client.Create(ctx, project, "a/B#token", []byte("v"), ""); !store.IsKind(err, store.KindUncertain) {
		t.Errorf("a write without an answer: %v, want uncertain", err)
	}
	if err := client.Delete(ctx, "33333333-3333-4333-8333-100000000000"); !store.IsKind(err, store.KindUncertain) {
		t.Errorf("a delete without an answer: %v, want uncertain", err)
	}
	if _, err := client.Index(ctx, project); !store.IsKind(err, store.KindNetwork) {
		t.Errorf("a read without an answer: %v, want network", err)
	}
}

func TestUnreachableServerIsNetworkForAWriteToo(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	if _, err := client.Organization(ctx); err != nil {
		t.Fatal(err)
	}
	// A listener that is closed refuses the connection, so nothing was sent.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	client.region.API = "http://" + listener.Addr().String()
	if _, err := client.Create(ctx, project, "a/B#token", []byte("v"), ""); !store.IsKind(err, store.KindNetwork) {
		t.Errorf("a write that could not connect: %v, want network", err)
	}
	if len(fake.Order) != 0 {
		t.Error("the write reached the server")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Index(cancelled, project); !store.IsKind(err, store.KindCancelled) {
		t.Errorf("a cancelled read: %v, want cancelled", err)
	}
}

func TestValueMustBeText(t *testing.T) {
	fake, client := newFake(t)
	_, err := client.Create(context.Background(), project, "a/B#token", []byte{0xff, 0xfe}, "")
	if !store.IsKind(err, store.KindInvalid) || len(fake.Requests) != 0 {
		t.Errorf("got %v after %d requests, want invalid before any request", err, len(fake.Requests))
	}
}

func TestUnreadableAnswers(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	created, err := client.Create(ctx, project, "a/B#token", []byte("v"), "")
	if err != nil {
		t.Fatal(err)
	}
	// A field under another key, or of a cipher type this client lacks.
	broken := fake.Secrets[created.ID]
	broken.Value = "7.AAAA"
	fake.Secrets[created.ID] = broken
	if _, err := client.Read(ctx, []string{created.ID}); !store.IsKind(err, store.KindRemote) {
		t.Errorf("an unsupported cipher type: %v, want remote_error", err)
	}
	if _, err := client.Index(ctx, project); err != nil {
		t.Errorf("the index read the value: %v", err)
	}
}

func TestReadRejectsAnIncompleteAnswer(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	var ids []string
	for _, key := range []string{"a/B#token", "a/C#token"} {
		h, err := client.Create(ctx, project, key, []byte("v"), "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, h.ID)
	}
	fake.ReadIDs = func(requested []string) []string { return requested[:1] }
	if _, err := client.Read(ctx, ids); !store.IsKind(err, store.KindRemote) {
		t.Errorf("an answer missing a secret: %v, want remote_error", err)
	}
}
