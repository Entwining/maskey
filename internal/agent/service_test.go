//go:build darwin && arm64

package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/owner"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func TestServiceMetadata(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "service")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory permissions: %v %v", info, err)
	}
	path := filepath.Join(dir, "keychain.lock")
	lock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	info, err = lock.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions: %v %v", info, err)
	}
	lock.Close()
	next, err := lockFile(path)
	if err != nil {
		t.Fatalf("restart lock: %v", err)
	}
	defer next.Close()
	if current, err := next.Stat(); err != nil || !os.SameFile(info, current) {
		t.Fatal("restart replaced the lock inode")
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if linked, err := lockFile(alias); !errors.Is(err, errServiceFiles) {
		if linked != nil {
			linked.Close()
		}
		t.Fatalf("symlink lock: %v", err)
	}
}

func TestRemoveStaleSocket(t *testing.T) {
	dir := serviceTempDir(t)
	path := filepath.Join(dir, "keychain.sock")
	if err := os.WriteFile(path, []byte("ordinary file"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(context.Background(), path); !errors.Is(err, errServiceFiles) {
		t.Fatalf("ordinary file: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "ordinary file" {
		t.Fatal("ordinary file was removed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	t.Cleanup(func() { listener.Close() })
	if err := removeStaleSocket(context.Background(), path); !errors.Is(err, errServiceRunning) {
		t.Fatalf("live listener: %v", err)
	}
	listener.Close()
	if err := removeStaleSocket(context.Background(), path); err != nil {
		t.Fatalf("stale listener: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket remains: %v", err)
	}
}

func TestServeLimitsAndDrainsWrites(t *testing.T) {
	backend := storetest.New()
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	backend.Before = func(op, _ string) error {
		if op == "add" {
			entered <- struct{}{}
			<-release
		}
		return nil
	}
	path, cancel, finished := startTestService(t, backend, nil, func(context.Context) error { return nil }, nil, notReplaced)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	if err := serve(context.Background(), path, backend, nil, func(context.Context) error { return nil }, nil, notReplaced); !errors.Is(err, errServiceRunning) {
		t.Fatalf("second service instance: %v", err)
	}
	results := make(chan error, 4)
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	for _, name := range []string{"ONE", "TWO", "THREE", "FOUR"} {
		go func() {
			_, err := (&Client{path: path}).remote(ctx, request{Op: "add", Account: "fixture/" + name, Value: []byte("synthetic")})
			results <- err
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("four requests did not enter the backend")
		}
	}
	fifth, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer fifth.Close()
	fifth.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var greeting [1]byte
	if _, err := fifth.Read(greeting[:]); err == nil {
		t.Fatal("fifth connection was served while four requests were in progress")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("fifth connection failed instead of waiting: %v", err)
	}
	cancel()
	select {
	case <-finished:
		t.Fatal("service exited before accepted writes finished")
	case <-time.After(25 * time.Millisecond):
	}
	unblock()
	for range 4 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("accepted write was interrupted: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("accepted write did not finish")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("service did not drain")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service socket remains after shutdown: %v", err)
	}
	items, err := backend.Items(context.Background())
	if err != nil || len(items) != 4 {
		t.Fatalf("accepted write results: %v %v", items, err)
	}
}

// Grants waiting for the owner share one dialog and hold no worker slot, so
// that reads go on while the dialog is up.
func TestServeWaitsForOwnerWithoutSlots(t *testing.T) {
	backend := storetest.New()
	names := []string{"ONE", "TWO", "THREE", "FOUR"}
	for _, name := range names {
		backend.Put("fixture/"+name, []byte("synthetic"))
	}
	var listings atomic.Int32
	backend.Before = func(op, _ string) error {
		if op == "items" {
			listings.Add(1)
		}
		return nil
	}
	var dialogs atomic.Int32
	answer := make(chan struct{})
	approval := owner.NewApproval(func(ctx context.Context, _ string) error {
		dialogs.Add(1)
		<-answer
		return nil
	}, func() (time.Duration, bool) { return 0, true }, "")
	path, _, _ := startTestService(t, backend, nil, func(context.Context) error { return nil }, approval.Approve, notReplaced)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auths := make(chan string, len(names))
	for _, name := range names {
		go func() {
			r, err := (&Client{path: path}).remote(ctx, request{Op: "grant", Accounts: []string{"fixture/" + name}})
			if err != nil {
				t.Error(err)
			}
			auths <- r.Auth
		}()
	}
	for listings.Load() < int32(len(names)) {
		time.Sleep(10 * time.Millisecond)
	}
	for range names {
		if _, err := (&Client{path: path}).remote(ctx, request{Op: "read", Accounts: []string{"fixture/ONE"}}); err != nil {
			t.Fatalf("read while the owner is asked: %v", err)
		}
	}
	close(answer)
	var got []string
	for range names {
		got = append(got, <-auths)
	}
	slices.Sort(got)
	if want := []string{"dialog", "reused", "reused", "reused"}; !slices.Equal(got, want) || dialogs.Load() != 1 {
		t.Fatalf("approvals %q after %d dialogs, want %q after 1", got, dialogs.Load(), want)
	}
}

// Shutdown withdraws a change the owner has not approved, which has written
// nothing, rather than wait for the dialog past the job's ExitTimeOut.
func TestShutdownWithdrawsPendingChange(t *testing.T) {
	backend := storetest.New()
	backend.Put("fixture/NAME", []byte("synthetic"))
	asked := make(chan struct{})
	approve := func(ctx context.Context, _ string) (string, error) {
		close(asked)
		<-ctx.Done()
		return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	recorder := new(storetest.Recorder)
	path, stop, finished := startTestService(t, backend, recorder, func(context.Context) error { return nil }, approve, notReplaced)
	changed := make(chan error, 1)
	go func() {
		_, err := (&Client{path: path}).remote(context.Background(), request{Op: "grant", Accounts: []string{"fixture/NAME"}})
		changed <- err
	}()
	<-asked
	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the service waited for the owner")
	}
	if err := <-changed; !store.IsKind(err, store.KindCancelled) {
		t.Fatalf("withdrawn change: %v, want cancelled", err)
	}
	if items, _ := backend.Items(context.Background()); items[0].ProgramAccess {
		t.Fatal("a withdrawn change was written")
	}
	if records := recorder.Records(); len(records) != 1 || records[0].Result != "cancelled" {
		t.Fatalf("records %+v, want the withdrawn grant", records)
	}
}

// Shutdown lets a change the owner approved finish and be answered.
func TestShutdownFinishesApprovedChange(t *testing.T) {
	reached, release := make(chan struct{}), make(chan struct{})
	backend := accessHook{storetest.New(), func(context.Context) {
		close(reached)
		<-release
	}}
	backend.Put("fixture/NAME", []byte("synthetic"))
	approve := func(context.Context, string) (string, error) { return "dialog", nil }
	path, stop, finished := startTestService(t, backend, nil, func(context.Context) error { return nil }, approve, notReplaced)
	changed := make(chan response, 1)
	go func() {
		r, err := (&Client{path: path}).remote(context.Background(), request{Op: "grant", Accounts: []string{"fixture/NAME"}})
		if err != nil {
			t.Error(err)
		}
		changed <- r
	}()
	<-reached
	stop()
	select {
	case <-finished:
		t.Fatal("the service stopped during an approved write")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if r := <-changed; len(r.Changes) != 1 || !r.Changes[0].Changed {
		t.Fatalf("answer: %+v", r)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not stop after the write")
	}
}

// recordingReads records each read in log, as the Keychain does.
type recordingReads struct {
	*storetest.Backend
	log *activity.Log
}

func (b recordingReads) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	values, err := b.Backend.Read(ctx, accounts)
	b.log.Record(ctx, activity.Record{Op: "read", Entry: accounts[0], Result: "ok"})
	return values, err
}

// The service's failure to record reaches the client, whose own warning
// reports it, and leaves the result alone.
func TestServiceFailureToRecordReachesClient(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var serviceWarnings atomic.Int32
	log := activity.New(filepath.Join(blocker, "Logs"), func(error) { serviceWarnings.Add(1) })
	backend := recordingReads{storetest.New(), log}
	backend.Put("fixture/NAME", []byte("synthetic"))
	path, _, _ := startTestService(t, backend, nil, func(context.Context) error { return nil }, nil, notReplaced)
	var warnings []error
	client := &Client{path: path, failed: func(err error) { warnings = append(warnings, err) }}
	r, err := client.remote(context.Background(), request{Op: "read", Accounts: []string{"fixture/NAME"}})
	if err != nil || string(r.Values["fixture/NAME"]) != "synthetic" {
		t.Fatalf("read: %+v, %v", r, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), "not a directory") || serviceWarnings.Load() != 0 {
		t.Fatalf("client warnings %v, service warnings %d", warnings, serviceWarnings.Load())
	}
}

// callerCheck fails a listing whose context names no caller.
type callerCheck struct{ *storetest.Backend }

func (b callerCheck) Items(ctx context.Context) ([]store.Item, error) {
	if activity.CallerOf(ctx).Exe == "" {
		return nil, errors.New("no caller before the operation")
	}
	return b.Backend.Items(ctx)
}

// TestServiceClientProcess is the client of TestServiceRecordsClientsCaller,
// in a process of its own.
func TestServiceClientProcess(t *testing.T) {
	path := os.Getenv("MASKEY_TEST_SERVICE_SOCKET")
	if path == "" {
		t.Skip("a client for TestServiceRecordsClientsCaller")
	}
	(&Client{path: path}).remote(context.Background(), request{Op: "grant", Accounts: []string{"fixture/NAME"}})
}

// The service describes its client before the operation, as the program
// that started the client, and records the client's withdrawn grant under
// it.
func TestServiceRecordsClientsCaller(t *testing.T) {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		t.Fatal(err)
	}
	backend := callerCheck{storetest.New()}
	backend.Put("fixture/NAME", []byte("synthetic"))
	asked := make(chan struct{})
	approve := func(ctx context.Context, _ string) (string, error) {
		close(asked)
		<-ctx.Done()
		return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	recorder := new(storetest.Recorder)
	path, stop, finished := startTestService(t, backend, recorder, func(context.Context) error { return nil }, approve, notReplaced)
	client := exec.Command(executable, "-test.run=^TestServiceClientProcess$")
	client.Env = append(os.Environ(), "MASKEY_TEST_SERVICE_SOCKET="+path)
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		client.Process.Kill()
		t.Fatal("the client's grant did not reach the owner")
	}
	// Killed, as by Ctrl-C, the client hangs up.
	client.Process.Kill()
	client.Wait()
	stop()
	<-finished
	records := recorder.Records()
	if len(records) != 1 || records[0].Op != "grant" || records[0].Result != "cancelled" || records[0].Caller.Exe != executable {
		t.Fatalf("records %+v, want the client's withdrawn grant under %s", records, executable)
	}
}

// Apple's interpreter is another executable, which the service rejects.
func connectAsOtherExecutable(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("/usr/bin/python3", "-I", "-S", "-c", "import socket,sys\ns=socket.socket(socket.AF_UNIX)\ns.connect(sys.argv[1])\ns.recv(1)\n", path)
	cmd.Env = []string{"PATH=" + servicePATH}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("other executable: %v: %s", err, output)
	}
}

// A rejected client ends the service only once its executable has been
// replaced, so the client cannot force a restart.
func TestServeRetiresOnlyWhenReplaced(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(fmt.Sprintf("replaced=%v", replaced), func(t *testing.T) {
			var asked atomic.Int32
			path, _, finished := startTestService(t, storetest.New(), nil, func(context.Context) error { return nil }, nil, func() bool {
				asked.Add(1)
				return replaced
			})
			connectAsOtherExecutable(t, path)
			select {
			case err := <-finished:
				if !replaced || err != nil {
					t.Fatalf("service stopped: %v", err)
				}
			case <-time.After(time.Second):
				if replaced {
					t.Fatal("service kept running on a replaced executable")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := (&Client{path: path}).remote(ctx, request{Op: "check"}); err != nil {
					t.Fatalf("service after a rejected client: %v", err)
				}
			}
			if asked.Load() != 1 {
				t.Fatalf("replacement checked %d times", asked.Load())
			}
		})
	}
}

// A client that reaches a service of another build waits for its restart
// instead of failing at once, and gives up if no matching service appears.
func TestClientWaitsForRestartedService(t *testing.T) {
	for _, restarts := range []bool{true, false} {
		t.Run(fmt.Sprintf("restarts=%v", restarts), func(t *testing.T) {
			path := filepath.Join(serviceTempDir(t), "keychain.sock")
			// The stand-in for the old build rejects one connection and exits,
			// as a retiring service does, or keeps rejecting.
			script := "import os,socket,sys\ns=socket.socket(socket.AF_UNIX)\ns.bind(sys.argv[1])\ns.listen(4)\nprint(flush=True)\nwhile True:\n c,_=s.accept()\n c.close()\n if sys.argv[2]=='once':\n  break\nos.unlink(sys.argv[1])\n"
			mode := "always"
			if restarts {
				mode = "once"
			}
			old := exec.Command("/usr/bin/python3", "-I", "-S", "-c", script, path, mode)
			old.Env = []string{"PATH=" + servicePATH}
			ready, err := old.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := old.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				old.Process.Kill()
				old.Wait()
			})
			if _, err := ready.Read(make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			started := time.Now()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*restartWait)
				defer cancel()
				_, err := (&Client{path: path}).remote(ctx, request{Op: "check"})
				result <- err
			}()
			if !restarts {
				err := <-result
				if !store.IsKind(err, store.KindUnavailable) || time.Since(started) < restartWait {
					t.Fatalf("persistent mismatch after %v: %v", time.Since(started), err)
				}
				return
			}
			if err := old.Wait(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan error, 1)
			go func() {
				finished <- serve(ctx, path, storetest.New(), nil, func(context.Context) error { return nil }, nil, notReplaced)
			}()
			defer func() {
				cancel()
				<-finished
			}()
			if err := <-result; err != nil {
				t.Fatalf("after the restart: %v", err)
			}
		})
	}
}

func TestAwaitServicePreservesHostFailure(t *testing.T) {
	var checks atomic.Int32
	failure := &store.Error{Kind: store.KindPrerequisite, Message: "synthetic host check failure"}
	path, _, _ := startTestService(t, storetest.New(), nil, func(context.Context) error { checks.Add(1); return failure }, nil, notReplaced)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := awaitService(ctx, path)
	if !store.IsKind(err, store.KindPrerequisite) || !strings.Contains(err.Error(), failure.Message) || checks.Load() != 1 {
		t.Fatalf("readiness failure=%v checks=%d", err, checks.Load())
	}
}

func serviceTempDir(t *testing.T) string {
	t.Helper()
	listener := peerListener(t)
	dir := filepath.Dir(listener.Addr().String())
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func notReplaced() bool { return false }

func startTestService(t *testing.T, backend store.Backend, recorder activity.Recorder, check func(context.Context) error, approve store.Approve, replaced func() bool) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	path := filepath.Join(serviceTempDir(t), "keychain.sock")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, path, backend, recorder, check, approve, replaced); close(finished) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("test service did not stop")
		}
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Lstat(path); err == nil {
			return path, cancel, finished
		}
		select {
		case err := <-finished:
			t.Fatalf("service start: %v", err)
		case <-deadline.C:
			t.Fatal("service socket did not appear")
		case <-time.After(time.Millisecond):
		}
	}
}
