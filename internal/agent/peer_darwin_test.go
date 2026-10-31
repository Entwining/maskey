//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAuthenticateSameBinary(t *testing.T) {
	if len(os.Args) == 4 && os.Args[2] == "--" {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: os.Args[3], Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := authenticate(conn); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		var reply [1]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 1 {
			t.Fatalf("authenticated reply: %v", err)
		}
		return
	}

	listener := peerListener(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthenticateSameBinary$", "--", listener.Addr().String())
	// A coverage build of the test writes its data under TMPDIR.
	cmd.Env = []string{"PATH=" + servicePATH, "TMPDIR=" + os.TempDir()}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	// The kernel reports the peer, whatever it claims.
	if pid, err := authenticate(conn); err != nil || pid != cmd.Process.Pid {
		t.Fatalf("authenticated pid %d, %v; want %d", pid, err, cmd.Process.Pid)
	}
	var request [1]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 1 {
		t.Fatalf("authenticated request: %v", err)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("client: %v: %s", err, &output)
	}
}

func peerListener(t *testing.T) *net.UnixListener {
	t.Helper()
	dir, err := os.MkdirTemp("", "mk-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "peer.sock"), Net: "unix"})
	if errors.Is(err, syscall.EPERM) {
		t.Skip("native boundary test context unavailable: Unix socket bind denied by the operating system")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	listener.SetDeadline(time.Now().Add(10 * time.Second))
	return listener
}

// The service compares its own identity with its executable's on disk, which
// changes when an upgrade replaces the app and is unusable mid-copy.
func TestFileIdentity(t *testing.T) {
	self, err := selfIdentity()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if own, err := fileIdentity(executable); err != nil || !bytes.Equal(own, self) {
		t.Fatalf("own executable: %x %v, running %x", own, err, self)
	}
	if other, err := fileIdentity("/bin/ls"); err != nil || bytes.Equal(other, self) {
		t.Fatalf("another executable: %x %v", other, err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(t.TempDir(), "partial")
	if err := os.WriteFile(partial, data[:len(data)/2], 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := fileIdentity(partial); err == nil {
		t.Fatal("a partly copied executable has an identity")
	}
	if _, err := fileIdentity(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing executable has an identity")
	}
}
