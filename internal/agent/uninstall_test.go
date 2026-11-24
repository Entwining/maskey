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
	"github.com/Entwining/maskey/internal/stores"
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
	lock, err := lockFile(filepath.Join(filepath.Dir(socket), "service.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	// The stores' configuration and snapshots go with the socket's directory.
	if dir, err := stores.Dir(); err != nil || dir != filepath.Dir(socket) {
		t.Fatalf("the stores are kept in %s, outside the service's directory: %v", dir, err)
	}

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
			writeFile(t, filepath.Join(home, "Library", "Application Support", store.Service, "service.lock"), nil)
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

// While mk setup, mk uninstall, or the window holds the configuration's
// lock, removing or installing the service fails at once, says why, and
// changes nothing.
func TestServiceChangeDuringAnother(t *testing.T) {
	home := t.TempDir()
	path := plistPath(home)
	writeFile(t, path, jobPlist("/previous/Maskey"))
	writeFile(t, filepath.Join(home, "Library", "Application Support", store.Service, "service.lock"), nil)
	lock, err := lockFile(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	job := launchd{loaded: true}
	items := removeService(context.Background(), home, job.control)
	if !slices.Equal(statuses(items), []string{"failed", "kept", "kept", "kept"}) || !strings.Contains(items[0].Detail, "mk uninstall, or Install Command Line Tool…") || len(job.calls) != 0 {
		t.Fatalf("%v, launchctl %v", items, job.calls)
	}
	executable := filepath.Join(home, "Maskey")
	writeFile(t, executable, nil)
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	ready := func(context.Context) error { t.Error("install waited for the service"); return nil }
	if err := install(context.Background(), executable, path, job.control, ready); !errors.Is(err, errServiceChanging) || len(job.calls) != 0 {
		t.Fatalf("install: %v, launchctl %v", err, job.calls)
	}
}

// Uninstall unlinks the lock while holding it, so a file opened before that
// and locked once another process has created and locked the path again
// must not count as held.
func TestLockFileIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.plist.lock")
	held, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	held.Close()
	recreated, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer recreated.Close()
	if err := lockOpened(opened, path); !errors.Is(err, errLocked) {
		t.Fatalf("locking the unlinked file: %v", err)
	}
}

// The Keychain service of an earlier Maskey goes with its files, its job
// last; a configuration under its label that is not Maskey's stays.
func TestRetireLegacy(t *testing.T) {
	home := t.TempDir()
	plist := legacyPlistPath(home)
	dir := filepath.Join(home, "Library", "Application Support", store.Service)
	legacy := bytes.ReplaceAll(bytes.ReplaceAll(jobPlist("/Applications/Maskey.app/Contents/MacOS/maskey"), []byte(serviceLabel), []byte(legacyLabel)), []byte(ServiceArg), []byte(LegacyArg))
	files := []string{plist, plist + ".lock", filepath.Join(filepath.Dir(plist), "."+legacyLabel+".backup-1"), filepath.Join(dir, "keychain.sock"), filepath.Join(dir, "keychain.lock")}
	for _, file := range files {
		writeFile(t, file, legacy)
	}
	kept := filepath.Join(dir, "stores.json")
	writeFile(t, kept, []byte("{}"))
	loaded, refuse := true, true
	var calls []string
	control := func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if _, err := os.Stat(plist); err == nil {
			t.Error("the job was asked about before its files were removed")
		}
		switch {
		case args[0] == "print" && !loaded:
			return errJobNotLoaded
		case args[0] == "bootout" && refuse:
			return errors.New("synthetic launchctl failure")
		case args[0] == "bootout":
			loaded = false
		}
		return nil
	}
	if item := legacyItem(home); item.Status != "" {
		t.Fatalf("before: %+v", item)
	}
	target := serviceDomain + "/" + legacyLabel
	// A job that could not be stopped keeps its configuration, by which
	// the next attempt knows it and stops it.
	if item := retireLegacy(context.Background(), home, control); item.Status != "failed" || !loaded || legacyItem(home).Status != "" {
		t.Fatalf("a refused stop: %+v, loaded %v, then %+v", item, loaded, legacyItem(home))
	}
	refuse, calls = false, nil
	item := retireLegacy(context.Background(), home, control)
	if item.Status != "removed" || loaded || !slices.Equal(calls, []string{"print " + target, "bootout " + target, "print " + target}) {
		t.Fatalf("%+v, loaded %v, calls %v", item, loaded, calls)
	}
	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			t.Errorf("%s stayed", file)
		}
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("the stores' configuration went with the earlier service")
	}
	if item := retireLegacy(context.Background(), home, control); item.Status != "absent" {
		t.Errorf("a second time: %+v", item)
	}

	writeFile(t, plist, jobPlist("/Applications/Maskey.app/Contents/MacOS/maskey"))
	if item := retireLegacy(context.Background(), home, control); item.Status != "kept" {
		t.Errorf("another job's configuration: %+v", item)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Error("another job's configuration was removed")
	}
}

// The Keychain service of an earlier Maskey writes the directory the
// services share and the activity record, so while its job may still run
// uninstalling keeps the directory and that job's files, and says a job may
// run; once the job stops, everything goes.
func TestRemoveServiceKeepsWhatAnEarlierJobWrites(t *testing.T) {
	home := t.TempDir()
	plist, earlier := plistPath(home), legacyPlistPath(home)
	dir := filepath.Join(home, "Library", "Application Support", store.Service)
	writeFile(t, plist, jobPlist("/Applications/Maskey.app/Contents/MacOS/maskey"))
	legacy := bytes.ReplaceAll(bytes.ReplaceAll(jobPlist("/Applications/Maskey.app/Contents/MacOS/maskey"), []byte(serviceLabel), []byte(legacyLabel)), []byte(ServiceArg), []byte(LegacyArg))
	writeFile(t, earlier, legacy)
	shared := []string{filepath.Join(dir, "keychain.sock"), filepath.Join(dir, "stores.json")}
	for _, file := range shared {
		writeFile(t, file, nil)
	}
	legacyTarget := serviceDomain + "/" + legacyLabel
	loaded := map[string]bool{serviceTarget: true, legacyTarget: true}
	refuse := true
	control := func(_ context.Context, args ...string) error {
		target := args[1]
		switch {
		case args[0] == "print" && !loaded[target]:
			return errJobNotLoaded
		case args[0] == "bootout" && target == legacyTarget && refuse:
			return errors.New("synthetic launchctl failure")
		case args[0] == "bootout":
			loaded[target] = false
		}
		return nil
	}

	items := removeService(context.Background(), home, control)
	if got := statuses(items); !slices.Equal(got, []string{"removed", "removed", "kept", "absent", "failed"}) {
		t.Fatalf("with the earlier job still loaded: %+v", items)
	}
	if last := items[len(items)-1]; !last.MayRun || last.Item != earlier || items[0].MayRun {
		t.Errorf("the rows say which job may run: %+v", items)
	}
	for _, file := range append(shared, earlier) {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("%s went while the earlier job may run: %v", file, err)
		}
	}
	if _, err := os.Stat(plist); err == nil || loaded[serviceTarget] {
		t.Error("the present service stayed")
	}

	refuse = false
	items = removeService(context.Background(), home, control)
	if got := statuses(items); !slices.Equal(got, []string{"absent", "absent", "removed", "absent", "removed"}) || loaded[legacyTarget] {
		t.Fatalf("once the earlier job stops: %+v", items)
	}
	if slices.ContainsFunc(items, func(item ServiceItem) bool { return item.MayRun }) {
		t.Errorf("a job may still run: %+v", items)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the shared directory stayed")
	}
	if _, err := os.Stat(earlier); err == nil {
		t.Error("the earlier job's configuration stayed")
	}
}
