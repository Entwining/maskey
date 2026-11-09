package sshagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/agent"
)

// A connection still open when the agent closes, as one a process the
// command left behind holds, gets no more answers.
func TestCloseEndsOpenConnections(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Start([]agent.AddedKey{{PrivateKey: key, Comment: "ns/SSH_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", a.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := agent.NewClient(conn)
	if keys, err := client.List(); err != nil || len(keys) != 1 || keys[0].Comment != "ns/SSH_KEY" {
		t.Fatalf("List: %v, %v", keys, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(); err == nil {
		t.Error("the agent answered after Close")
	}
	if _, err := os.Stat(filepath.Dir(a.Socket())); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the agent's directory remains: %v", err)
	}
}
