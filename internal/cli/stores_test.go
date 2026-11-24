package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// A command works on the store --store names, or else on this Mac's
// default, and on no other.
func TestCommandsUseOneStore(t *testing.T) {
	h := newHarness(t)
	h.connect()
	ctx := context.Background()
	h.backend.Add(ctx, "app/TOKEN", []byte("icloud-value"), store.Details{Kind: store.ValueToken})
	h.work.Add(ctx, "app/TOKEN", []byte("work-value"), store.Details{Kind: store.ValueToken})
	h.work.SetProgramAccess(ctx, "app/TOKEN", "", true)
	h.work.Add(ctx, "deploy/KEY", []byte("deploy-value"), store.Details{Kind: store.ValueToken})

	if outcome, stdout, stderr := h.run(t, "", "get", "--store", "work", "app/TOKEN"); outcome.Code != 0 || stdout != "work-value" {
		t.Errorf("get --store work: %+v %q %s", outcome, stdout, stderr)
	}
	// mk run takes the entries of the one store it names.
	if outcome, _, stderr := h.run(t, "", "run", "--store", "work", "app", "deploy", "--", "/bin/sh", "-c", `[ "$TOKEN" = work-value ] && [ "$KEY" = deploy-value ]`); outcome.Code != 0 {
		t.Errorf("run --store work: %+v %s", outcome, stderr)
	}
	// The default store does not answer for an entry only another holds.
	if outcome, _, stderr := h.run(t, "", "list", "deploy"); outcome.Code != 3 || errorKind(stderr) != "not_found" {
		t.Errorf("list in the default store: %+v %s", outcome, stderr)
	}
	h.config.Default = "work"
	if outcome, stdout, stderr := h.run(t, "", "list", "deploy"); outcome.Code != 0 || !strings.Contains(stdout, "deploy/KEY") {
		t.Errorf("list in the default store work: %+v %q %s", outcome, stdout, stderr)
	}
	if outcome, _, stderr := h.run(t, "", "get", "--store", "icloud", "app/TOKEN"); outcome.Code == 0 {
		t.Errorf("get --store icloud read an entry without program access: %s", stderr)
	}
	if outcome, stdout, stderr := h.run(t, "", "grant", "--store", "icloud", "app/TOKEN"); outcome.Code != 0 {
		t.Errorf("grant --store icloud: %+v %q %s", outcome, stdout, stderr)
	}
	if value, err := h.backend.Get(ctx, "app/TOKEN"); string(value) != "icloud-value" {
		t.Errorf("grant --store icloud did not change the entry in iCloud Keychain: %v", err)
	}
}

func TestUnknownStore(t *testing.T) {
	h := newHarness(t)
	outcome, stdout, stderr := h.run(t, "", "list", "--store", "work")
	if outcome.Code != 3 || stdout != "" || stderr != "mk: error[not_found]: this Mac has no store with that name\nmk: hint: run mk store list to see this Mac's stores\n" {
		t.Fatalf("%+v %q %s", outcome, stdout, stderr)
	}
}

func TestStoreList(t *testing.T) {
	h := newHarness(t)
	h.connect()
	h.config.Default = "work"
	outcome, stdout, stderr := h.run(t, "", "store", "list")
	want := "NAME    TYPE                       DEFAULT  TARGET\n" +
		"icloud  iCloud Keychain                     this Apple Account\n" +
		"work    Bitwarden Secrets Manager  yes      EU / Deploy\n"
	if outcome.Code != 0 || stdout != want || stderr != "" {
		t.Fatalf("%+v\n%s%s", outcome, stdout, stderr)
	}
	_, stdout, _ = h.run(t, "", "store", "list", "--json")
	listed := decode(t, stdout)["stores"].([]any)
	work := listed[1].(map[string]any)
	if len(listed) != 2 || work["id"] != "work" || work["type"] != "bitwarden" || work["default"] != true || work["project_name"] != "Deploy" {
		t.Fatalf("%s", stdout)
	}
}

// The access token reaches the service from standard input or the hidden
// prompt, and no argument can carry it.
func TestStoreAdd(t *testing.T) {
	h := newHarness(t)
	outcome, stdout, stderr := h.run(t, "synthetic-token\n", "store", "add", "bitwarden", "--stdin", "--name", "work", "--region", "eu", "--project", "Deploy")
	if outcome.Code != 0 || stdout != "work: added, connected to the project Deploy\n" || stderr != "" {
		t.Fatalf("%+v %q %s", outcome, stdout, stderr)
	}
	want := stores.Addition{ID: "work", Type: stores.Bitwarden, Region: "eu", Project: "Deploy", Token: []byte("synthetic-token")}
	if h.added.ID != want.ID || h.added.Type != want.Type || h.added.Region != want.Region || h.added.Project != want.Project || string(h.added.Token) != string(want.Token) {
		t.Fatalf("added %+v", h.added)
	}

	h.terminal = terminal(t, "typed-token\n")
	if outcome, _, stderr := h.runOn(h.terminal, "store", "add", "bitwarden"); outcome.Code != 0 || string(h.added.Token) != "typed-token" || h.added.ID != "" {
		t.Fatalf("at the prompt: %+v %+v %s", outcome, h.added, stderr)
	}

	if outcome, _, stderr := h.run(t, "", "store", "add", "bitwarden", "synthetic-token"); outcome.Code != 2 || errorKind(stderr) != "usage" {
		t.Fatalf("a token as an argument: %+v %s", outcome, stderr)
	}
	if outcome, _, stderr := h.run(t, "", "store", "add", "vault", "--stdin"); outcome.Code != 2 || errorKind(stderr) != "usage" {
		t.Fatalf("another type: %+v %s", outcome, stderr)
	}

	h.storeErr = &store.Error{Kind: store.KindExists, Message: "this Mac already has a store with that name"}
	_, _, stderr = h.run(t, "synthetic-token\n", "store", "add", "bitwarden", "--stdin")
	if stderr != "mk: error[exists]: this Mac already has a store with that name\nmk: hint: pass another name with --name, or remove the store first with mk store remove\n" {
		t.Fatalf("%s", stderr)
	}
}

func TestStoreChanges(t *testing.T) {
	h := newHarness(t)
	h.connect()
	for _, c := range []struct{ command, done string }{{"default", "now the default"}, {"sync", "synced"}, {"remove", "removed"}} {
		outcome, stdout, stderr := h.run(t, "", "store", c.command, "work")
		if outcome.Code != 0 || stdout != "work: "+c.done+"\n" || stderr != "" {
			t.Errorf("store %s: %+v %q %s", c.command, outcome, stdout, stderr)
		}
	}
	if !slices.Equal(h.storeChanges, []string{"store_default work", "store_sync work", "store_remove work"}) {
		t.Fatalf("%v", h.storeChanges)
	}
	outcome, _, stderr := h.run(t, "", "store", "remove", "gone")
	if outcome.Code != 3 || stderr != "mk: error[not_found]: argument 1: this Mac has no store with that name\nmk: hint: run mk store list to see this Mac's stores\n" {
		t.Fatalf("%+v %s", outcome, stderr)
	}
	// A lost answer leaves a store to check, not an entry.
	h.storeErr = &store.Error{Kind: store.KindUncertain, Message: "the Maskey service ended before it answered"}
	if _, _, stderr = h.run(t, "", "store", "default", "work"); !strings.HasSuffix(stderr, "mk: hint: run mk store list to see whether the change was made, then repeat the command if it was not\n") {
		t.Fatalf("%s", stderr)
	}
	// The list shows nothing of a sync or of a token.
	if _, _, stderr = h.run(t, "", "store", "sync", "work"); !strings.HasSuffix(stderr, "mk: hint: repeat the command, which is safe to run again\n") {
		t.Fatalf("%s", stderr)
	}
}

// A hint names the store the command was given, and a prompt about a
// Bitwarden store says where the change lands.
func TestHintsAndPromptsNameTheStore(t *testing.T) {
	h := newHarness(t)
	h.connect()
	h.work.Add(context.Background(), "app/TOKEN", []byte("work-value"), store.Details{Kind: store.ValueToken})
	_, _, stderr := h.run(t, "", "get", "--store", "work", "app/TOKEN")
	if want := "mk: hint: use mk run --store work app/TOKEN -- <command>, or ask the owner to grant program access\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("get: %s", stderr)
	}
	if _, _, stderr = h.run(t, "", "list", "--store", "work", "nosuch"); !strings.HasSuffix(stderr, "mk: hint: run mk list --store work to see the entries\n") {
		t.Errorf("list: %s", stderr)
	}
	// A warning's hint names it too.
	if _, _, stderr = h.run(t, "new-value", "set", "--stdin", "--store", "work", "app/token"); !strings.Contains(stderr, "mk delete --store work removes the one you do not need\n") {
		t.Errorf("set a name that differs in case: %s", stderr)
	}
	// Without --store the commands a hint names use the default too.
	h.config.Default = "work"
	if _, _, stderr = h.run(t, "", "list", "nosuch"); !strings.HasSuffix(stderr, "mk: hint: run mk list to see the entries\n") {
		t.Errorf("list in the default store: %s", stderr)
	}
	_, _, _, onTerminal := h.runAsking(t, "n\n", "delete", "app/TOKEN")
	if want := "Delete 1 entry from the Bitwarden project Deploy (store work), for everyone who uses it:\n  app/TOKEN\n"; !strings.Contains(onTerminal, want) {
		t.Errorf("delete asked %q", onTerminal)
	}
	_, _, _, onTerminal = h.runAsking(t, "n\n", "set", "app/TOKEN")
	if want := "Replace the value of 1 entry in the Bitwarden project Deploy (store work):\n  app/TOKEN\n"; !strings.Contains(onTerminal, want) {
		t.Errorf("set asked %q", onTerminal)
	}
}

// Only mk store default takes iCloud Keychain, so only it completes it.
func TestStoreCompletion(t *testing.T) {
	h := newHarness(t)
	h.connect()
	for command, want := range map[string]string{"default": "icloud\nwork\n:4\n", "sync": "work\n:4\n", "token": "work\n:4\n", "remove": "work\n:4\n"} {
		if _, stdout, _ := h.run(t, "", "__complete", "store", command, ""); !strings.HasPrefix(stdout, want) {
			t.Errorf("store %s: %q", command, stdout)
		}
	}
}

// Uninstall removes each remote store before the service, and nothing when
// a store stays, whose Keychain items a later uninstall could not find.
func TestUninstallRemovesStoresFirst(t *testing.T) {
	h := newHarness(t)
	h.connect()
	h.run(t, "", "skill", "install")
	outcome, stdout, stderr := h.run(t, "", "uninstall", "--yes")
	if outcome.Code != 0 || !strings.HasPrefix(stdout, "removed  store work, with its access token and program access\nabsent   Maskey service\n") || !slices.Equal(h.storeChanges, []string{"store_remove work"}) {
		t.Fatalf("%+v %v\n%s%s", outcome, h.storeChanges, stdout, stderr)
	}

	h = newHarness(t)
	h.connect()
	h.run(t, "", "skill", "install")
	h.storeErr = &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service is not running"}
	outcome, stdout, stderr = h.run(t, "", "uninstall", "--yes")
	if outcome.Code == 0 || stdout != "" || !strings.Contains(stderr, "hint: nothing was removed;") {
		t.Fatalf("%+v %q %s", outcome, stdout, stderr)
	}
	if _, err := os.Lstat(filepath.Join(h.home, ".agents", "skills", "maskey")); err != nil {
		t.Fatalf("a skill link went: %v", err)
	}

	// The hint counts the stores that went before the one that stays.
	h.config.Stores = append(h.config.Stores, stores.Connection{ID: "zeta", Type: stores.Bitwarden})
	h.failingStore = "zeta"
	if _, _, stderr = h.run(t, "", "uninstall", "--yes"); !strings.Contains(stderr, "hint: only 1 store was removed;") {
		t.Fatalf("%s", stderr)
	}

	// Only a service that was not reached surely removed nothing of the
	// store: its token goes first, so another failure may have left the
	// store partly removed.
	h.storeErr = &store.Error{Kind: store.KindKeychain, Message: "synthetic Keychain failure"}
	if _, _, stderr = h.run(t, "", "uninstall", "--yes"); !strings.Contains(stderr, "hint: the store zeta may be partly removed, and only 1 store was removed;") {
		t.Fatalf("%s", stderr)
	}
	h.failingStore = ""
	if _, _, stderr = h.run(t, "", "uninstall", "--yes"); !strings.Contains(stderr, "hint: the store work may be partly removed, and nothing else was removed;") {
		t.Fatalf("%s", stderr)
	}

	// A configuration that cannot be read may name stores whose Keychain
	// items would be left behind.
	h.storeErr, h.storeChanges = nil, nil
	h.configErr = &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}
	outcome, stdout, stderr = h.run(t, "", "uninstall", "--yes")
	if outcome.Code != 6 || stdout != "" || !strings.Contains(stderr, "hint: nothing was removed;") || len(h.storeChanges) != 0 {
		t.Fatalf("%+v %v %q %s", outcome, h.storeChanges, stdout, stderr)
	}
	if _, err := os.Lstat(filepath.Join(h.home, ".agents", "skills", "maskey")); err != nil {
		t.Fatalf("a skill link went: %v", err)
	}
	// So may one that only the service can read, when it is not reached.
	h.configErr = &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service is not reachable"}
	outcome, stdout, stderr = h.run(t, "", "uninstall", "--yes")
	if outcome.Code != 10 || stdout != "" || !strings.Contains(stderr, "hint: nothing was removed; run mk outside the sandbox") || len(h.storeChanges) != 0 {
		t.Fatalf("%+v %v %q %s", outcome, h.storeChanges, stdout, stderr)
	}
}

// The activity listing names the store of an entry outside iCloud Keychain.
func TestActivityNamesTheStore(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { h.activity.Remove() })
	ctx := context.Background()
	h.activity.Record(ctx, activity.Record{Op: "get", Entry: "app/TOKEN", Result: "ok"})
	h.activity.Record(activity.WithStore(ctx, "work"), activity.Record{Op: "get", Entry: "app/TOKEN", Result: "ok"})
	_, stdout, stderr := h.run(t, "", "activity")
	if strings.Count(stdout, "work:app/TOKEN") != 1 || strings.Count(stdout, "app/TOKEN") != 2 {
		t.Fatalf("%s%s", stdout, stderr)
	}
}

// mk doctor reads each other store from its remote; only the default
// store's failure decides the exit status.
func TestDoctorChecksStores(t *testing.T) {
	h := newHarness(t)
	h.connect()
	h.work.Put("app/TOKEN", []byte("synthetic-value"))
	outcome, stdout, _ := h.run(t, "", "doctor")
	if outcome.Code != 0 || !strings.Contains(stdout, "\nok       store work   1 entry in 1 namespace, read from Bitwarden Secrets Manager now\n") || !slices.Contains(h.storeChanges, "store_sync work") {
		t.Fatalf("%+v %v\n%s", outcome, h.storeChanges, stdout)
	}
	h.storeErr = &store.Error{Kind: store.KindNetwork, Message: "Bitwarden cannot be reached"}
	outcome, stdout, _ = h.run(t, "", "doctor")
	if outcome.Code != 0 || !strings.Contains(stdout, "\nwarning  store work   Bitwarden cannot be reached\n                      hint: check the network connection, then retry\n") {
		t.Fatalf("a store that is not the default: %+v\n%s", outcome, stdout)
	}
	h.config.Default = "work"
	outcome, stdout, _ = h.run(t, "", "doctor")
	if outcome.Code != 13 || !strings.Contains(stdout, "\nerror    store work   Bitwarden cannot be reached\n") {
		t.Fatalf("the default store: %+v\n%s", outcome, stdout)
	}
	// Without the configuration no command finds its default store.
	h.configErr = &store.Error{Kind: store.KindPrerequisite, Message: "this Mac's store configuration cannot be read"}
	outcome, stdout, _ = h.run(t, "", "doctor")
	if outcome.Code != 6 || !strings.Contains(stdout, "\nerror    stores") || !strings.Contains(stdout, "until then commands need --store icloud\n") {
		t.Fatalf("an unreadable configuration: %+v\n%s", outcome, stdout)
	}
	// Where mk may not read the file, the service reads it, so without the
	// service the recovery is to reach it, not to repair the file.
	h.configErr = &store.Error{Kind: store.KindUnavailable, Message: "the Maskey service is not reachable"}
	outcome, stdout, _ = h.run(t, "", "doctor")
	if outcome.Code != 10 || !strings.Contains(stdout, "service.sock; until then commands need --store icloud\n") || strings.Contains(stdout, "repair") {
		t.Fatalf("a configuration only the service can read: %+v\n%s", outcome, stdout)
	}
}

// A new access token reaches the service as the first one does.
func TestStoreToken(t *testing.T) {
	h := newHarness(t)
	h.connect()
	outcome, stdout, stderr := h.run(t, "synthetic-renewed\n", "store", "token", "--stdin", "work")
	if outcome.Code != 0 || stdout != "work: token replaced\n" || stderr != "" || !slices.Equal(h.storeChanges, []string{"token work synthetic-renewed"}) {
		t.Fatalf("%+v %q %s %v", outcome, stdout, stderr, h.storeChanges)
	}
	if outcome, _, stderr := h.run(t, "", "store", "token", "work", "synthetic-renewed"); outcome.Code != 2 || errorKind(stderr) != "usage" {
		t.Fatalf("a token as an argument: %+v %s", outcome, stderr)
	}
}
