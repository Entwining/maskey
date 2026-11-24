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
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestAuthenticateSameBinary(t *testing.T) { authenticateSameBinary(t, nil) }

// A command sandbox lets its commands ask about no process outside it, the
// service included, and they must still tell whose socket they reached.
func TestAuthenticateFromASandbox(t *testing.T) {
	sandbox := []string{"/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)(deny process-info*)(allow process-info* (target self))"}
	if err := exec.Command(sandbox[0], append(sandbox[1:], "/usr/bin/true")...).Run(); err != nil {
		t.Skip("native boundary test context unavailable: cannot start a sandbox here:", err)
	}
	authenticateSameBinary(t, sandbox)
}

// authenticateSameBinary has this test binary authenticate a copy of itself
// started through wrap, and that copy authenticate it.
func authenticateSameBinary(t *testing.T, wrap []string) {
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
	argv := slices.Concat(wrap, []string{os.Args[0], "-test.run=^" + t.Name() + "$", "--", listener.Addr().String()})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
	// The kernel reports the peer, whatever it claims; sandbox-exec replaces
	// itself with the command, so the pid is still the command's.
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

func TestAuthenticateAnotherBinary(t *testing.T) {
	listener := peerListener(t)
	cmd := exec.Command("/usr/bin/nc", "-U", listener.Addr().String())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := authenticate(conn); !errors.Is(err, errPeerMismatch) {
		t.Fatalf("another signed executable: %v, want a mismatch", err)
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
