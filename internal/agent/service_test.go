//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func TestInstallCanonicalJob(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(home, "Maskey <&>")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "mk")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	var calls [][]string
	control := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		if args[0] == "print" {
			return errJobNotLoaded
		}
		return nil
	}
	ready := false
	if err := install(context.Background(), link, path, control, func(context.Context) error { ready = true; return nil }); err != nil {
		t.Fatal(err)
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	want := [][]string{{"print", domain + "/" + serviceLabel}, {"bootstrap", domain, path}}
	if !reflect.DeepEqual(calls, want) || !ready {
		t.Fatalf("install calls=%v ready=%v", calls, ready)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var plist struct {
		Args []string `xml:"dict>array>string"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plist.Args, []string{canonical, "--keychain-service"}) {
		t.Fatalf("program arguments=%v", plist.Args)
	}
	for _, property := range []string{
		"<key>PATH</key><string>/usr/bin:/bin</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>AssociatedBundleIdentifiers</key><string>io.github.entwining.maskey</string>",
		"<key>ProcessType</key><string>Background</string>",
		"<key>ExitTimeOut</key><integer>35</integer>",
	} {
		if !bytes.Contains(data, []byte(property)) {
			t.Errorf("missing launchd property %s", property)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("plist permissions: %v %v", info, err)
	}
	cmd := exec.Command("/usr/bin/plutil", "-lint", "--", path)
	cmd.Env = []string{"PATH=" + servicePATH}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid launchd plist: %v: %s", err, output)
	}
}

func TestInstallUpgradeAndFailures(t *testing.T) {
	for _, failure := range []string{"", "print", "bootout", "bootstrap", "ready"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "Maskey")
			if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, serviceLabel+".plist")
			old := bytes.Replace(jobPlist("/previous/Maskey"), []byte("</dict>\n</plist>"), []byte("<key>Nice</key><integer>5</integer>\n</dict>\n</plist>"), 1)
			if err := os.WriteFile(path, old, 0644); err != nil {
				t.Fatal(err)
			}
			injected := &store.Error{Kind: store.KindPrerequisite, Message: "synthetic host check failure"}
			var calls []string
			control := func(_ context.Context, args ...string) error {
				calls = append(calls, args[0])
				if args[0] == failure {
					return injected
				}
				return nil
			}
			err := install(context.Background(), executable, path, control, func(context.Context) error {
				calls = append(calls, "ready")
				if failure == "ready" {
					return injected
				}
				return nil
			})
			want := []string{"print", "bootout", "bootstrap", "ready"}
			if failure != "" {
				for i, step := range want {
					if step == failure {
						want = want[:i+1]
						break
					}
				}
				if err == nil {
					t.Fatal("failed installation reported success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if failure == "ready" && !errors.Is(err, injected) {
				t.Fatalf("host check failure was hidden: %v", err)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
			backups, err := filepath.Glob(filepath.Join(dir, "."+serviceLabel+".backup-*"))
			if err != nil {
				t.Fatal(err)
			}
			if failure == "print" {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, old) || len(backups) != 0 {
					t.Fatal("failed status query changed the existing configuration")
				}
				return
			}
			if len(backups) != 1 {
				t.Fatalf("configuration backups=%v", backups)
			}
			data, err := os.ReadFile(backups[0])
			if err != nil || !bytes.Equal(data, old) {
				t.Fatal("previous configuration was not preserved")
			}
		})
	}
}

// Reinstalling the same configuration has nothing to preserve, so repeated
// setups leave no backups behind.
func TestReinstallKeepsNoIdenticalBackup(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "Maskey")
	if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, serviceLabel+".plist")
	if err := os.WriteFile(path, jobPlist(resolved), 0600); err != nil {
		t.Fatal(err)
	}
	control := func(context.Context, ...string) error { return nil }
	ready := func(context.Context) error { return nil }
	for range 2 {
		if err := install(context.Background(), executable, path, control, ready); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(dir, "."+serviceLabel+".backup-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("configuration backups=%v %v", backups, err)
	}
}

func TestInstallRejectsForeignConfiguration(t *testing.T) {
	for _, kind := range []string{"label", "arguments", "symlink", "registered-only"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, "Maskey")
			if err := os.WriteFile(executable, []byte("unused"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, serviceLabel+".plist")
			data := jobPlist(executable)
			switch kind {
			case "label":
				data = bytes.Replace(data, []byte(serviceLabel), []byte("another.job"), 1)
			case "arguments":
				data = bytes.Replace(data, []byte("--keychain-service"), []byte("--another-service"), 1)
			}
			if kind == "symlink" {
				if err := os.Symlink(executable, path); err != nil {
					t.Fatal(err)
				}
			} else if kind != "registered-only" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			err := install(context.Background(), executable, path, func(_ context.Context, args ...string) error {
				calls = append(calls, args[0])
				return nil
			}, func(context.Context) error { t.Fatal("foreign job reached readiness"); return nil })
			if !errors.Is(err, errServiceCollision) {
				t.Fatalf("foreign job: %v", err)
			}
			wantCalls := 0
			if kind == "registered-only" {
				wantCalls = 1
			}
			if len(calls) != wantCalls {
				t.Fatalf("foreign job reached launchctl: %v", calls)
			}
			if kind != "symlink" && kind != "registered-only" {
				current, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(current, data) {
					t.Fatal("foreign configuration was replaced")
				}
			}
		})
	}
}

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
	path, cancel, finished := startTestService(t, backend, func(context.Context) error { return nil }, notReplaced)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	if err := serve(context.Background(), path, backend, func(context.Context) error { return nil }, notReplaced); !errors.Is(err, errServiceRunning) {
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
			path, _, finished := startTestService(t, storetest.New(), func(context.Context) error { return nil }, func() bool {
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
				finished <- serve(ctx, path, storetest.New(), func(context.Context) error { return nil }, notReplaced)
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
	path, _, _ := startTestService(t, storetest.New(), func(context.Context) error { checks.Add(1); return failure }, notReplaced)
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

func startTestService(t *testing.T, backend store.Backend, check func(context.Context) error, replaced func() bool) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	path := filepath.Join(serviceTempDir(t), "keychain.sock")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, path, backend, check, replaced); close(finished) }()
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
