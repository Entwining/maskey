// Package sshagent lends SSH keys to one command through the SSH agent
// protocol: the command can list the keys and sign with them, but the
// protocol gives it no request that returns a private key, and the agent
// ends when the command does.
package sshagent

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh/agent"
)

// Agent serves its keys on a Unix socket in a directory of its own, which
// only this user can enter.
type Agent struct {
	dir      string
	listener net.Listener
	keyring  agent.Agent
	served   sync.WaitGroup

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

// Start serves keys until Close. The socket is in os.TempDir, which a
// command's sandbox lets it reach where it lets it keep temporary files.
func Start(keys []agent.AddedKey) (*Agent, error) {
	keyring := agent.NewKeyring()
	for _, key := range keys {
		if err := keyring.Add(key); err != nil {
			return nil, err
		}
	}
	// x/crypto reports each request it cannot answer, such as a signature
	// with a key the agent does not hold, through the standard logger, which
	// would write to mk's own stderr.
	log.SetOutput(io.Discard)
	dir, err := os.MkdirTemp("", "mk-ssh-")
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "agent.sock"))
	if err != nil {
		os.Remove(dir)
		return nil, err
	}
	a := &Agent{dir: dir, listener: listener, keyring: keyring, conns: map[net.Conn]struct{}{}}
	a.served.Go(a.accept)
	return a, nil
}

// Socket is the path SSH_AUTH_SOCK names.
func (a *Agent) Socket() string { return a.listener.Addr().String() }

func (a *Agent) accept() {
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			conn.Close()
			return
		}
		a.conns[conn] = struct{}{}
		a.mu.Unlock()
		a.served.Go(func() {
			_ = agent.ServeAgent(a.keyring, conn)
			a.mu.Lock()
			delete(a.conns, conn)
			a.mu.Unlock()
			conn.Close()
		})
	}
}

// Close stops the agent and removes its directory. It also ends the
// connections still open, such as one a process the command left behind
// holds, so that no signature follows the command's end. Closing it again,
// even concurrently, is harmless.
func (a *Agent) Close() error {
	a.mu.Lock()
	a.closed = true
	for conn := range a.conns {
		conn.Close()
	}
	a.mu.Unlock()
	a.listener.Close()
	a.served.Wait()
	return os.RemoveAll(a.dir)
}
