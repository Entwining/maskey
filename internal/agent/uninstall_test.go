//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Entwining/maskey/internal/store"
)

// launchd stands in for launchctl: its job is loaded from bootstrap until
// bootout, unless stuck, and the subcommand fail names fails.
type launchd struct {
	loaded, stuck bool
	fail          string
	calls         []string
}

func (l *launchd) control(_ context.Context, args ...string) error {
	l.calls = append(l.calls, args[0])
	switch {
	case args[0] == l.fail:
		return errors.New("synthetic launchctl failure")
	case args[0] == "print" && !l.loaded:
		return errJobNotLoaded
	case args[0] == "bootout":
		l.loaded = l.stuck
	case args[0] == "bootstrap":
		l.loaded = true
	}
	return nil
}

func statuses(items []ServiceItem) []string {
	var s []string
	for _, item := range items {
		s = append(s, item.Status)
	}
	return s
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// What an upgrade that keeps a backup and the running service leave under
// home is listed, and removing the service leaves nothing.
func TestRemoveServiceUndoesInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	executable := filepath.Join(t.TempDir(), "Maskey")
	writeFile(t, executable, nil)
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	path := plistPath(home)
	writeFile(t, path, jobPlist("/previous/Maskey"))
	var job launchd
	if err := install(context.Background(), executable, path, job.control, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	socket, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	// The service's lock, as serve takes it.
	if err := privateDir(filepath.Dir(socket)); err != nil {
		t.Fatal(err)
	}
	lock, err := lockFile(filepath.Join(filepath.Dir(socket), "keychain.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()

	listed := serviceItems(context.Background(), home, job.control)
	removed := removeService(context.Background(), home, job.control)
	if !slices.Equal(statuses(listed), []string{"", "", "", "", ""}) || !slices.Equal(statuses(removed), []string{"removed", "removed", "removed", "removed", "removed"}) || job.loaded {
		t.Fatalf("listed %v, removed %v, loaded %v", listed, removed, job.loaded)
	}
	var left []string
	filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !entry.IsDir() {
			left = append(left, path)
		}
		return nil
	})
	if len(left) != 0 {
		t.Fatalf("left in the home directory: %v", left)
	}
}

// The job is stopped only when its configuration is this Maskey's, the
// configuration stays when it is not, and the other files stay while a job
// may still be loaded; a listing shows the same, so the owner is not asked
// about what stays. The rows are the job, the configuration, the socket's
// directory, and the install lock.
func TestRemoveServiceKeeps(t *testing.T) {
	ours := jobPlist("/previous/Maskey")
	foreign := bytes.Replace(ours, []byte(serviceLabel), []byte("another.job"), 1)
	for _, test := range []struct {
		name            string
		plist           []byte
		symlink         bool
		job             launchd
		listed, removed []string
	}{
		{name: "status", plist: ours, job: launchd{loaded: true, fail: "print"},
			listed: []string{"failed", "kept", "kept", ""}, removed: []string{"failed", "kept", "kept", "removed"}},
		{name: "stop", plist: ours, job: launchd{loaded: true, fail: "bootout"},
			listed: []string{"", "", "", ""}, removed: []string{"failed", "kept", "kept", "removed"}},
		{name: "stuck", plist: ours, job: launchd{loaded: true, stuck: true},
			listed: []string{"", "", "", ""}, removed: []string{"failed", "kept", "kept", "removed"}},
		{name: "foreign", plist: foreign, job: launchd{loaded: true},
			listed: []string{"kept", "kept", "kept", ""}, removed: []string{"kept", "kept", "kept", "removed"}},
		{name: "foreign unloaded", plist: foreign,
			listed: []string{"absent", "kept", "", ""}, removed: []string{"absent", "kept", "removed", "removed"}},
		{name: "symlink", plist: ours, symlink: true,
			listed: []string{"absent", "kept", "", ""}, removed: []string{"absent", "kept", "removed", "removed"}},
		{name: "loaded without configuration", job: launchd{loaded: true},
			listed: []string{"kept", "absent", "kept", ""}, removed: []string{"kept", "absent", "kept", "removed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			path := plistPath(home)
			writeFile(t, path+".lock", nil)
			writeFile(t, filepath.Join(home, "Library", "Application Support", store.Service, "keychain.lock"), nil)
			switch {
			case test.symlink:
				target := filepath.Join(t.TempDir(), "job.plist")
				writeFile(t, target, test.plist)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case test.plist != nil:
				writeFile(t, path, test.plist)
			}
			// A stuck job keeps the stop waiting until its deadline.
			synctest.Test(t, func(t *testing.T) {
				job := test.job
				listed := serviceItems(context.Background(), home, job.control)
				removed := removeService(context.Background(), home, job.control)
				if !slices.Equal(statuses(listed), test.listed) || !slices.Equal(statuses(removed), test.removed) || job.loaded != test.job.loaded {
					t.Fatalf("listed %v, removed %v, loaded %v", listed, removed, job.loaded)
				}
				for _, item := range removed[1:] {
					if _, err := os.Lstat(item.Item); (err == nil) != (item.Status == "kept") {
						t.Errorf("%s is %s: %v", item.Item, item.Status, err)
					}
				}
			})
		})
	}
}

// Where the service was never installed, nothing is listed to remove, and
// removing it leaves nothing behind, not even its own lock.
func TestRemoveServiceNotInstalled(t *testing.T) {
	home := t.TempDir()
	var job launchd
	listed := serviceItems(context.Background(), home, job.control)
	removed := removeService(context.Background(), home, job.control)
	absent := []string{"absent", "absent", "absent", "absent"}
	if !slices.Equal(statuses(listed), absent) || !slices.Equal(statuses(removed), absent) {
		t.Fatalf("listed %v, removed %v", listed, removed)
	}
	if entries, err := os.ReadDir(filepath.Dir(plistPath(home))); err != nil || len(entries) != 0 {
		t.Fatalf("left %v %v", entries, err)
	}
}

// A file that cannot be removed is a failure, not a success.
func TestRemoveServiceFailure(t *testing.T) {
	home := t.TempDir()
	path := plistPath(home)
	writeFile(t, path, jobPlist("/previous/Maskey"))
	writeFile(t, path+".lock", nil)
	os.Chmod(filepath.Dir(path), 0o500)
	t.Cleanup(func() { os.Chmod(filepath.Dir(path), 0o700) })
	job := launchd{loaded: true}
	removed := removeService(context.Background(), home, job.control)
	if !slices.Equal(statuses(removed), []string{"removed", "failed", "absent", "failed"}) {
		t.Fatalf("removed %v", removed)
	}
}

// While an install holds its lock, removing the service fails at once and
// changes nothing.
func TestRemoveServiceDuringInstall(t *testing.T) {
	home := t.TempDir()
	path := plistPath(home)
	writeFile(t, path, jobPlist("/previous/Maskey"))
	writeFile(t, filepath.Join(home, "Library", "Application Support", store.Service, "keychain.lock"), nil)
	lock, err := lockFile(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	job := launchd{loaded: true}
	items := removeService(context.Background(), home, job.control)
	if !slices.Equal(statuses(items), []string{"failed", "kept", "kept", "kept"}) || !strings.Contains(items[0].Detail, "installing") || len(job.calls) != 0 {
		t.Fatalf("%v, launchctl %v", items, job.calls)
	}
}
