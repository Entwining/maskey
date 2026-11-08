package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

func TestEntryCommands(t *testing.T) {
	h := newHarness(t)
	if outcome, stdout, stderr := h.run(t, "", "list"); outcome.Code != 0 || stdout != "" || stderr != "mk: no entries\n" {
		t.Fatalf("empty list: %+v %q %q", outcome, stdout, stderr)
	}
	if outcome, stdout, _ := h.run(t, "first", "set", "--stdin", "app/API_TOKEN"); outcome.Code != 0 || stdout != "created app/API_TOKEN\n" {
		t.Fatalf("set: %+v %q", outcome, stdout)
	}
	if outcome, stdout, _ := h.run(t, "second", "set", "app/API_TOKEN", "--stdin"); outcome.Code != 0 || stdout != "replaced app/API_TOKEN\n" {
		t.Fatalf("replacing set: %+v %q", outcome, stdout)
	}
	h.set(t, "db/PASSWORD")

	if got := h.list(t); got != "app/API_TOKEN\ndb/PASSWORD\n" {
		t.Fatalf("list: %q", got)
	}
	if _, stdout, _ := h.run(t, "", "list", "db"); stdout != "db/PASSWORD\n" {
		t.Fatalf("list db: %q", stdout)
	}
	// A namespace exists only through its entries.
	if outcome, _, stderr := h.run(t, "", "list", "missing"); outcome.Code != 3 || !strings.HasPrefix(stderr, "mk: error[not_found]: argument 1: the namespace has no entries\n") {
		t.Fatalf("list missing: %+v %q", outcome, stderr)
	}

	if outcome, stdout, _ := h.run(t, "", "rename", "db/PASSWORD", "app/DB_PASSWORD"); outcome.Code != 0 || stdout != "renamed db/PASSWORD to app/DB_PASSWORD\n" {
		t.Fatalf("rename: %+v %q", outcome, stdout)
	}
	// The destination exists, so the error names it.
	outcome, _, stderr := h.run(t, "", "rename", "app/API_TOKEN", "app/DB_PASSWORD")
	if want := "mk: error[exists]: app/DB_PASSWORD: the entry already exists\nmk: hint: choose a different destination for mk rename\n"; outcome.Code != 4 || stderr != want {
		t.Fatalf("rename onto an entry: %+v %q", outcome, stderr)
	}
	if outcome, _, stderr := h.run(t, "", "rename", "app/MISSING", "app/MISSING"); outcome.Code != 2 || errorKind(stderr) != "usage" {
		t.Fatalf("rename onto itself: %+v %q", outcome, stderr)
	}
	// A rename whose result cannot be written was still done.
	var errOut bytes.Buffer
	outcome = Run(context.Background(), []string{"rename", "app/DB_PASSWORD", "app/PASSWORD"}, h.env(nil, failingWriter{}, &errOut))
	if outcome.Code != 12 || !strings.HasSuffix(errOut.String(), "mk: hint: the entry was renamed; mk list shows it under the new reference\n") || !strings.Contains(h.list(t), "app/PASSWORD\n") {
		t.Fatalf("rename, failed stdout: %+v %q %q", outcome, errOut.String(), h.list(t))
	}

	if outcome, stdout, _ := h.run(t, "", "delete", "--yes", "app/API_TOKEN"); outcome.Code != 0 || stdout != "deleted app/API_TOKEN\n" {
		t.Fatalf("delete: %+v %q", outcome, stdout)
	}
	// A missing entry is named by its position.
	outcome, stdout, stderr := h.run(t, "", "delete", "-y", "app/API_TOKEN")
	want := "mk: error[not_found]: argument 1: the entry does not exist\nmk: hint: run mk list to see the entries\n"
	if outcome.Code != 3 || stdout != "" || stderr != want {
		t.Fatalf("second delete: %+v %q %q", outcome, stdout, stderr)
	}
}

// A namespace keeps the case it was given: GitHub and github are two
// namespaces wherever an entry is selected or listed.
func TestNamespaceCase(t *testing.T) {
	h := newHarness(t)
	h.run(t, "synthetic-upper", "set", "--stdin", "GitHub/TOKEN")
	h.run(t, "synthetic-lower", "set", "--stdin", "github/TOKEN")
	if got := h.list(t); got != "GitHub/TOKEN\ngithub/TOKEN\n" {
		t.Fatalf("list: %q", got)
	}
	if _, stdout, _ := h.run(t, "", "list", "GitHub"); stdout != "GitHub/TOKEN\n" {
		t.Fatalf("list GitHub: %q", stdout)
	}
	if _, stdout, _ := h.run(t, "", "__complete", "list", ""); !strings.HasPrefix(stdout, "GitHub\ngithub\n:") {
		t.Fatalf("completion: %q", stdout)
	}
	h.backend.SetProgramAccess(context.Background(), "GitHub/TOKEN", true)
	if _, stdout, _ := h.run(t, "", "get", "GitHub/TOKEN"); stdout != "synthetic-upper" {
		t.Fatalf("get GitHub/TOKEN: %q", stdout)
	}
	if outcome, _, _ := h.run(t, "", "get", "github/TOKEN"); outcome.Code != 11 {
		t.Fatalf("get github/TOKEN: %+v, want not_granted", outcome)
	}
	for _, selector := range []string{"GitHub", "GitHub/TOKEN"} {
		if outcome, _, stderr := h.run(t, "", "run", selector, "--", "/bin/sh", "-c", `[ "$TOKEN" = synthetic-upper ]`); outcome.Code != 0 {
			t.Fatalf("run %s: %+v %q", selector, outcome, stderr)
		}
	}
	if _, _, stderr := h.run(t, "", "run", "GitHub", "github", "--", "true"); !strings.HasPrefix(stderr, "mk: error[conflict]: GitHub and github both supply TOKEN\n") {
		t.Fatalf("run GitHub github: %q", stderr)
	}
}

// set warns before creating an entry whose name differs from existing ones
// only in case, in its namespace, NAME, or both.
func TestSetCaseWarning(t *testing.T) {
	const hint = "mk: hint: names are case-sensitive, so this creates a second entry; mk delete removes the one you do not need\n"
	for _, ref := range []string{"app/token", "App/TOKEN", "App/token"} {
		h := newHarness(t)
		h.set(t, "app/TOKEN")
		outcome, stdout, stderr := h.run(t, "v", "set", "--stdin", ref)
		if want := "mk: warning: " + ref + " differs only in case from app/TOKEN\n" + hint; outcome.Code != 0 || stdout != "created "+ref+"\n" || stderr != want || strings.Count(h.list(t), "\n") != 2 {
			t.Errorf("%s: %+v %q %q %q", ref, outcome, stdout, stderr, h.list(t))
		}
	}

	h := newHarness(t)
	h.set(t, "app/TOKEN")
	outcome, _, stderr := h.runOn(terminal(t, "v\n"), "set", "app/token")
	if want := "mk: warning: app/token differs only in case from app/TOKEN\n" + hint + "Value for app/token (input hidden): \n"; outcome.Code != 0 || stderr != want {
		t.Fatalf("hidden prompt: %+v %q", outcome, stderr)
	}
	// The warning stays on stderr when the prompt is on the controlling
	// terminal.
	h = newHarness(t)
	h.set(t, "app/TOKEN")
	outcome, _, stderr, onTerminal := h.runAsking(t, "v\n", "set", "app/token")
	if want := "mk: warning: app/token differs only in case from app/TOKEN\n" + hint; outcome.Code != 0 || stderr != want || !strings.Contains(onTerminal, "Value for app/token (input hidden): ") || strings.Contains(onTerminal, "warning") {
		t.Fatalf("prompt on the terminal: %+v %q %q", outcome, stderr, onTerminal)
	}

	// The warning comes before the write.
	h = newHarness(t)
	h.set(t, "app/TOKEN")
	var stdout, errOut bytes.Buffer
	h.backend.Before = func(op, _ string) error {
		if op == "add" && errOut.Len() == 0 {
			t.Error("written before the warning")
		}
		return nil
	}
	outcome = Run(context.Background(), []string{"set", "--stdin", "--json", "app/token"}, h.env(stdinHolding(t, "v"), &stdout, &errOut))
	wantOut := `{"results":[{"namespace":"app","name":"token","action":"created"}]}` + "\n"
	wantErr := `{"warning":{"message":"app/token differs only in case from app/TOKEN","recovery":"names are case-sensitive, so this creates a second entry; mk delete removes the one you do not need"}}` + "\n"
	if outcome.Code != 0 || stdout.String() != wantOut || errOut.String() != wantErr {
		t.Fatalf("JSON: %+v %s %s", outcome, stdout.String(), errOut.String())
	}

	// Replacing an entry, or creating one with no twin, warns of nothing.
	h = newHarness(t)
	h.set(t, "app/TOKEN")
	for _, ref := range []string{"app/TOKEN", "app/OTHER"} {
		if outcome, _, stderr := h.run(t, "v", "set", "--stdin", ref); outcome.Code != 0 || stderr != "" {
			t.Errorf("%s: %+v %q", ref, outcome, stderr)
		}
	}

	h = newHarness(t)
	h.set(t, "app/TOKEN", "App/TOKEN")
	if _, _, stderr := h.run(t, "v", "set", "--stdin", "app/token"); !strings.HasPrefix(stderr, "mk: warning: app/token differs only in case from App/TOKEN and app/TOKEN\n") {
		t.Errorf("two twins: %q", stderr)
	}
}

// A listing that fails fails set before it writes.
func TestSetListingFails(t *testing.T) {
	h := newHarness(t)
	h.backend.Before = func(op, _ string) error {
		switch op {
		case "items":
			return &store.Error{Kind: store.KindLocked, Message: "injected"}
		case "add", "update":
			t.Error("written after the listing failed")
		}
		return nil
	}
	if outcome, _, stderr := h.run(t, "v", "set", "--stdin", "app/X"); outcome.Code != 7 || errorKind(stderr) != "locked" {
		t.Fatalf("%+v %q", outcome, stderr)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic output failure") }

// A batch writes in order and stops at its first failure, reporting what it
// confirmed and what it did not try.
func TestBatchFailures(t *testing.T) {
	refs := []string{"ex/ONE", "ex/TWO", "ex/THREE"}
	failOn := func(h *harness, ref string, kind store.Kind) {
		h.backend.Before = func(op, account string) error {
			if op == "delete" && account == ref {
				return &store.Error{Kind: kind, Message: "injected"}
			}
			return nil
		}
	}
	h := newHarness(t)
	h.set(t, refs...)
	failOn(h, "ex/TWO", store.KindUncertain)
	outcome, stdout, stderr := h.run(t, "", append([]string{"delete", "--yes"}, refs...)...)
	want := `mk: error[uncertain]: argument 2: injected
mk: completed: argument 1 ex/ONE (deleted)
mk: not attempted: argument 3
mk: hint: check the entry in Maskey before repeating the write; mk list cannot show whether a value was replaced
`
	if outcome.Code != 9 || stdout != "deleted ex/ONE\n" || stderr != want {
		t.Fatalf("text: %+v %q %q", outcome, stdout, stderr)
	}

	h = newHarness(t)
	h.set(t, refs...)
	failOn(h, "ex/TWO", store.KindUncertain)
	outcome, stdout, stderr = h.run(t, "", append([]string{"delete", "--yes", "--json"}, refs...)...)
	want = `{"error":{"kind":"uncertain","message":"injected","argument":2,"exit_code":9,"completed":[{"namespace":"ex","name":"ONE","action":"deleted"}],"not_attempted":[3],"recovery":"check the entry in Maskey before repeating the write; mk list cannot show whether a value was replaced"}}` + "\n"
	if outcome.Code != 9 || stdout != "" || stderr != want {
		t.Fatalf("JSON: %+v %q %s", outcome, stdout, stderr)
	}

	h = newHarness(t)
	h.set(t, refs...)
	failOn(h, "ex/ONE", store.KindKeychain)
	outcome, _, stderr = h.run(t, "", append([]string{"delete", "--yes"}, refs...)...)
	if want := "mk: completed: none\nmk: not attempted: arguments 2, 3\n"; outcome.Code != 8 || !strings.Contains(stderr, want) {
		t.Fatalf("first write failed: %+v %q", outcome, stderr)
	}

	// An entry deleted after the check was found by its exact name, so its
	// failure suggests no similar one.
	h = newHarness(t)
	h.set(t, append(refs, "ex/TWOS")...)
	h.backend.Before = func(op, account string) error {
		if op == "delete" && account == "ex/TWO" {
			h.backend.Before = nil
			return h.backend.Delete(context.Background(), account)
		}
		return nil
	}
	outcome, _, stderr = h.run(t, "", append([]string{"delete", "--yes"}, refs...)...)
	if want := "mk: hint: run mk list to see the entries\n"; outcome.Code != 3 || !strings.HasSuffix(stderr, want) {
		t.Fatalf("entry gone before its write: %+v %q", outcome, stderr)
	}

	// An interruption between writes fails no write.
	h = newHarness(t)
	h.set(t, refs...)
	ctx, cancel := context.WithCancel(context.Background())
	h.backend.Before = func(op, _ string) error {
		if op == "delete" {
			cancel()
		}
		return nil
	}
	var errOut bytes.Buffer
	outcome = Run(ctx, append([]string{"delete", "--yes", "--json"}, refs...), h.env(nil, io.Discard, &errOut))
	want = `{"error":{"kind":"cancelled","message":"cancelled before the next write","exit_code":130,"completed":[{"namespace":"ex","name":"ONE","action":"deleted"}],"not_attempted":[2,3]}}` + "\n"
	if outcome.Code != 130 || errOut.String() != want {
		t.Fatalf("cancelled: %+v %s", outcome, errOut.String())
	}

	// A result that cannot be written stops the batch after the write it
	// reports.
	h = newHarness(t)
	h.set(t, refs...)
	errOut.Reset()
	outcome = Run(context.Background(), append([]string{"delete", "--yes"}, refs...), h.env(nil, failingWriter{}, &errOut))
	if !strings.HasPrefix(errOut.String(), "mk: error[io]: cannot finish writing the result\nmk: completed: argument 1 ex/ONE (deleted)\nmk: not attempted: arguments 2, 3\n") || outcome.Code != 12 || h.list(t) != "ex/THREE\nex/TWO\n" {
		t.Fatalf("output failure: %+v %q", outcome, errOut.String())
	}
	// The JSON document comes after every write, so all are confirmed.
	h = newHarness(t)
	h.set(t, refs...)
	errOut.Reset()
	outcome = Run(context.Background(), append([]string{"delete", "--yes", "--json"}, refs...), h.env(nil, failingWriter{}, &errOut))
	want = `{"error":{"kind":"io","message":"cannot finish writing the result","exit_code":12,"completed":[{"namespace":"ex","name":"ONE","action":"deleted"},{"namespace":"ex","name":"TWO","action":"deleted"},{"namespace":"ex","name":"THREE","action":"deleted"}],"not_attempted":[],"recovery":"` + outputHint + `"}}` + "\n"
	if outcome.Code != 12 || errOut.String() != want {
		t.Fatalf("JSON output failure: %+v %s", outcome, errOut.String())
	}
}

func TestJSONOutput(t *testing.T) {
	h := newHarness(t)
	modified := time.Date(2026, 10, 30, 13, 7, 0, 0, time.UTC)
	h.backend.Now = func() time.Time { return modified }
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"set", "--stdin", "--json", "app/GITHUB_TOKEN"}, `{"results":[{"namespace":"app","name":"GITHUB_TOKEN","action":"created"}]}`},
		{[]string{"list", "--json"}, `{"entries":[{"namespace":"app","name":"GITHUB_TOKEN","kind":"token","modified":"2026-10-30T13:07:00Z","program_access":false}]}`},
		{[]string{"rename", "--json", "app/GITHUB_TOKEN", "gh/GITHUB_TOKEN"}, `{"from":{"namespace":"app","name":"GITHUB_TOKEN"},"to":{"namespace":"gh","name":"GITHUB_TOKEN"}}`},
		{[]string{"delete", "--json", "--yes", "gh/GITHUB_TOKEN"}, `{"results":[{"namespace":"gh","name":"GITHUB_TOKEN","action":"deleted"}]}`},
		{[]string{"list", "--json"}, `{"entries":[]}`},
	} {
		if outcome, stdout, stderr := h.run(t, "v", test.args...); outcome.Code != 0 || stdout != test.want+"\n" || stderr != "" {
			t.Errorf("%q: %+v %s %s", test.args, outcome, stdout, stderr)
		}
	}
	// Errors go to stderr, as JSON with --json.
	outcome, stdout, stderr := h.run(t, "", "delete", "--json", "--yes", "gh/GITHUB_TOKEN")
	want := `{"error":{"kind":"not_found","message":"the entry does not exist","argument":1,"exit_code":3,"recovery":"run mk list to see the entries"}}` + "\n"
	if outcome.Code != 3 || stdout != "" || stderr != want {
		t.Fatalf("%+v stdout %q stderr %s", outcome, stdout, stderr)
	}
	// A result that cannot be written is an io failure.
	h.set(t, "app/TOKEN")
	for _, args := range [][]string{{"list", "--json"}, {"list"}, {"doctor"}, {"skill", "show"}} {
		var errOut bytes.Buffer
		if outcome := Run(context.Background(), args, h.env(nil, failingWriter{}, &errOut)); outcome.Code != 12 || errorKind(errOut.String()) != "io" && !strings.HasPrefix(errOut.String(), `{"error":{"kind":"io",`) {
			t.Errorf("%q with an unwritable result: %+v %q", args, outcome, errOut.String())
		}
	}
}

func TestGet(t *testing.T) {
	h := newHarness(t)
	h.run(t, "line 1\nline 2\n", "set", "--stdin", "app/TOKEN")
	h.set(t, "app/OTHER")
	h.backend.SetProgramAccess(context.Background(), "app/TOKEN", true)
	// --json is for errors; the value stays raw.
	for _, args := range [][]string{{"get", "app/TOKEN"}, {"get", "--json", "app/TOKEN"}} {
		if outcome, stdout, stderr := h.run(t, "", args...); outcome.Code != 0 || stdout != "line 1\nline 2\n" || stderr != "" {
			t.Fatalf("%q: %+v %q %q", args, outcome, stdout, stderr)
		}
	}
	outcome, stdout, stderr := h.run(t, "", "get", "app/OTHER")
	want := "mk: error[not_granted]: app/OTHER: program access is not granted\nmk: hint: use mk run app/OTHER -- <command>, or ask the owner to grant program access\n"
	if outcome.Code != 11 || stdout != "" || stderr != want {
		t.Fatalf("not granted: %+v %q %q", outcome, stdout, stderr)
	}
	if outcome, _, stderr := h.run(t, "", "get", "app/MISSING"); outcome.Code != 3 || !strings.HasPrefix(stderr, "mk: error[not_found]: argument 1: the entry does not exist\n") {
		t.Fatalf("missing: %+v %q", outcome, stderr)
	}
	// A terminal on stdout is refused before the value is looked up.
	h.backend.Before = func(op, _ string) error {
		if op == "get" {
			t.Error("the value was looked up")
		}
		return nil
	}
	var errOut bytes.Buffer
	outcome = Run(context.Background(), []string{"get", "app/TOKEN"}, h.env(nil, terminal(t, ""), &errOut))
	if outcome.Code != 2 || !strings.HasPrefix(errOut.String(), "mk: error[usage]: get cannot write a raw value to terminal stdout\n") {
		t.Fatalf("terminal: %+v %q", outcome, errOut.String())
	}
}
