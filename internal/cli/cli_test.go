package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

type harness struct {
	backend *storetest.Backend
	store   *store.Store
	check   error
	service error
	home    string
	source  string
}

func newHarness(t *testing.T) *harness {
	backend := storetest.New()
	source := filepath.Join(t.TempDir(), "Maskey.app", "Contents", "Resources", "skills", "maskey")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	return &harness{backend: backend, store: store.New(backend), home: t.TempDir(), source: source}
}

func (h *harness) env(stdin *os.File, stdout, stderr io.Writer) Env {
	return Env{
		Store:        h.store,
		Check:        func(context.Context) error { return h.check },
		CheckService: func(context.Context) error { return h.service },
		Skill:        []byte("synthetic skill\n"),
		SkillSource:  h.source,
		Home:         h.home,
		Stdin:        stdin,
		Stdout:       stdout,
		Stderr:       stderr,
		Version:      "1.2.3",
	}
}

// run executes a command line with stdin holding input.
func (h *harness) run(t *testing.T, input string, args ...string) (Outcome, string, string) {
	t.Helper()
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	stdin.WriteString(input)
	stdin.Seek(0, 0)
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	outcome := Run(context.Background(), args, h.env(stdin, &stdout, &stderr))
	return outcome, stdout.String(), stderr.String()
}

func decode(t *testing.T, output string) map[string]any {
	t.Helper()
	var document map[string]any
	if strings.Count(output, "\n") != 1 || json.Unmarshal([]byte(output), &document) != nil {
		t.Fatalf("output %q is not one line of JSON", output)
	}
	return document
}

// errorKind is the kind of the text error on stderr.
func errorKind(stderr string) string {
	kind, _, _ := strings.Cut(strings.TrimPrefix(stderr, "mk: error["), "]")
	return kind
}

func TestManagementCommands(t *testing.T) {
	h := newHarness(t)
	value := "synthetic-a1b2\n"
	if outcome, stdout, _ := h.run(t, value, "set", "--stdin", "app/API_TOKEN"); outcome.Code != 0 || stdout != "created app/API_TOKEN\n" {
		t.Fatalf("set: %+v %q", outcome, stdout)
	}
	// --stdin keeps the exact bytes, trailing newline included.
	if got, _ := h.store.Value(context.Background(), store.Ref{Namespace: "app", Name: "API_TOKEN"}); string(got) != value {
		t.Fatalf("stored %q, want %q", got, value)
	}
	if outcome, stdout, _ := h.run(t, "second", "set", "app/API_TOKEN", "--stdin"); outcome.Code != 0 || stdout != "replaced app/API_TOKEN\n" {
		t.Fatalf("replacing set: %+v %q", outcome, stdout)
	}
	h.run(t, "v", "set", "--stdin", "db/PASSWORD")

	if _, stdout, _ := h.run(t, "", "list"); stdout != "app/API_TOKEN\ndb/PASSWORD\n" {
		t.Fatalf("list: %q", stdout)
	}
	if _, stdout, _ := h.run(t, "", "list", "db"); stdout != "db/PASSWORD\n" {
		t.Fatalf("list db: %q", stdout)
	}
	// A namespace exists only through its entries.
	if outcome, _, stderr := h.run(t, "", "list", "missing"); outcome.Code != 3 || errorKind(stderr) != "not_found" {
		t.Fatalf("list missing: %+v %q", outcome, stderr)
	}

	if outcome, stdout, _ := h.run(t, "", "rename", "db/PASSWORD", "app/DB_PASSWORD"); outcome.Code != 0 || stdout != "renamed db/PASSWORD to app/DB_PASSWORD\n" {
		t.Fatalf("rename: %+v %q", outcome, stdout)
	}
	if outcome, _, stderr := h.run(t, "", "rename", "app/API_TOKEN", "app/DB_PASSWORD"); outcome.Code != 4 || errorKind(stderr) != "exists" {
		t.Fatalf("rename onto an entry: %+v %q", outcome, stderr)
	}
	// The store takes an unchanged entry as renamed without looking for it.
	if outcome, _, stderr := h.run(t, "", "rename", "app/MISSING", "app/MISSING"); outcome.Code != 2 || errorKind(stderr) != "usage" {
		t.Fatalf("rename onto itself: %+v %q", outcome, stderr)
	}

	if outcome, stdout, _ := h.run(t, "", "delete", "--yes", "app/API_TOKEN"); outcome.Code != 0 || stdout != "deleted app/API_TOKEN\n" {
		t.Fatalf("delete: %+v %q", outcome, stdout)
	}
	outcome, stdout, stderr := h.run(t, "", "delete", "-y", "app/API_TOKEN")
	want := "mk: error[not_found]: app/API_TOKEN: the entry does not exist\nmk: hint: run mk list app to see its entries\n"
	if outcome.Code != 3 || stdout != "" || stderr != want {
		t.Fatalf("second delete: %+v %q %q", outcome, stdout, stderr)
	}
}

// Without a terminal to ask on, delete needs --yes.
func TestDeleteNeedsConfirmation(t *testing.T) {
	h := newHarness(t)
	h.run(t, "v", "set", "--stdin", "app/TOKEN")
	if outcome, _, stderr := h.run(t, "y\n", "delete", "app/TOKEN"); outcome.Code != 2 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("%+v %q", outcome, stderr)
	}
	if _, stdout, _ := h.run(t, "", "list"); stdout != "app/TOKEN\n" {
		t.Fatalf("the entry was deleted: %q", stdout)
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
		{[]string{"set", "--stdin", "--json", "app/GITHUB_TOKEN"}, `{"created":true,"name":"GITHUB_TOKEN","namespace":"app"}`},
		{[]string{"list", "--json"}, `{"entries":[{"namespace":"app","name":"GITHUB_TOKEN","kind":"token","modified":"2026-10-30T13:07:00Z"}]}`},
		{[]string{"rename", "--json", "app/GITHUB_TOKEN", "gh/GITHUB_TOKEN"}, `{"from":{"namespace":"app","name":"GITHUB_TOKEN"},"to":{"namespace":"gh","name":"GITHUB_TOKEN"}}`},
		{[]string{"delete", "--json", "--yes", "gh/GITHUB_TOKEN"}, `{"deleted":true,"name":"GITHUB_TOKEN","namespace":"gh"}`},
	} {
		if outcome, stdout, _ := h.run(t, "v", test.args...); outcome.Code != 0 || stdout != test.want+"\n" {
			t.Errorf("%q: %+v %s", test.args, outcome, stdout)
		}
	}
	// Errors go to stderr, as JSON with --json.
	outcome, stdout, stderr := h.run(t, "", "delete", "--json", "--yes", "gh/GITHUB_TOKEN")
	e, _ := decode(t, stderr)["error"].(map[string]any)
	if outcome.Code != 3 || stdout != "" || e["kind"] != "not_found" || e["recovery"] == nil || e["entry"] == nil {
		t.Fatalf("%+v stdout %q stderr %s", outcome, stdout, stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, test := range []struct {
		args []string
		kind string
		hint string
	}{
		{[]string{"ls"}, "usage", "did you mean mk list?"},
		{[]string{"lsit"}, "usage", "did you mean mk list?"},
		{[]string{"bogus"}, "usage", "run mk --help for the commands"},
		{[]string{"--bogus"}, "usage", "run mk --help for the commands"},
		{[]string{"skill", "bogus"}, "usage", "run mk skill --help for the commands"},
		{[]string{"help", "bogus"}, "usage", "usage: mk help [<command> | exit-codes]"},
		{[]string{"list", "a", "b"}, "usage", "usage: mk list [<namespace>]"},
		{[]string{"set", "app/TOKEN"}, "usage", "usage: mk set [--stdin] <namespace>/<name>"}, // stdin is not a terminal
		{[]string{"set", "app", "TOKEN"}, "usage", ""},
		{[]string{"set", "--value=v", "app/TOKEN"}, "usage", ""},
		{[]string{"set", "--stdin", "App/X"}, "invalid", ""},
		{[]string{"doctor", "extra"}, "usage", "usage: mk doctor"},
	} {
		outcome, stdout, stderr := h.run(t, "synthetic", test.args...)
		if outcome.Code != 2 || stdout != "" || errorKind(stderr) != test.kind || test.hint != "" && !strings.Contains(stderr, "mk: hint: "+test.hint) {
			t.Errorf("%q: %+v stdout %q stderr %q", test.args, outcome, stdout, stderr)
		}
	}
	if names, _ := h.store.Namespaces(context.Background()); len(names) != 0 {
		t.Fatalf("an entry was stored: %v", names)
	}
}

// A value put where an argument belongs, the way callers misuse a command
// line, stays out of every error.
func TestErrorsNeverQuoteArguments(t *testing.T) {
	h := newHarness(t)
	const value = "synthetic-value-5e1d"
	for _, args := range [][]string{
		{value},
		{"GITHUB_TOKEN=" + value, "gh", "auth", "status"},
		{"set", "github/TOKEN", value},
		{"set", "github/TOKEN=" + value},
		{"set", "github/" + value},
		{"set", "--stdin=" + value, "github/TOKEN"},
		{"set", "--" + value, "github/TOKEN"},
		{"set", "-x" + value, "github/TOKEN"},
		{"set", "--=" + value, "github/TOKEN"},
		{"run", "-n", "app", value},
		{"run", "-e", "app/TOKEN=" + value, "--", "true"},
		{"list", value + "/"},
		{"help", value},
		{"skill", value},
		{"completion", "zsh", value},
	} {
		for _, format := range [][]string{nil, {"--json"}} {
			_, stdout, stderr := h.run(t, "", append(args, format...)...)
			if strings.Contains(stdout+stderr, value) {
				t.Errorf("%q printed the argument: %q %q", args, stdout, stderr)
			}
		}
	}
}

func TestErrorKindsAndExitCodes(t *testing.T) {
	h := newHarness(t)
	for kind, code := range map[store.Kind]int{
		store.KindNotFound:     3,
		store.KindConflict:     5,
		store.KindPrerequisite: 6,
		store.KindLocked:       7,
		store.KindKeychain:     8,
		store.KindUncertain:    9,
		store.KindUnavailable:  10,
		store.KindCancelled:    130,
	} {
		h.backend.Before = func(string, string) error { return &store.Error{Kind: kind, Message: "injected"} }
		outcome, stdout, stderr := h.run(t, "", "list", "--json")
		e, _ := decode(t, stderr)["error"].(map[string]any)
		if outcome.Code != code || e["kind"] != string(kind) || stdout != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %s; want exit %d", kind, outcome.Code, stdout, stderr, code)
		}
	}
}

func TestSetCancelledWhileReading(t *testing.T) {
	h := newHarness(t)
	// The writer stays open, so the read never ends by itself.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	done := make(chan Outcome, 1)
	go func() {
		done <- Run(ctx, []string{"set", "--stdin", "app/TOKEN"}, h.env(r, io.Discard, &stderr))
	}()
	cancel()
	select {
	case outcome := <-done:
		if outcome.Code != 130 || errorKind(stderr.String()) != "cancelled" {
			t.Fatalf("%+v %s", outcome, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("set kept reading after it was cancelled")
	}
}

func TestRun(t *testing.T) {
	h := newHarness(t)
	h.run(t, "synthetic-c3d4", "set", "--stdin", "app/TOKEN")

	// Options after -- belong to the command, and stdin reaches it.
	outcome, stdout, stderr := h.run(t, "piped input", "run", "-n", "app", "--", "/bin/sh", "-c", `cat; printf ' %s out %s' "$1" "$TOKEN"; printf 'err %s' "$TOKEN" >&2; exit 7`, "sh", "--json")
	if outcome.Code != 7 || stdout != "piped input --json out ***" || stderr != "err ***" {
		t.Fatalf("run: %+v stdout %q stderr %q", outcome, stdout, stderr)
	}
	if outcome, stdout, _ := h.run(t, "", "run", "-e", "app/TOKEN", "--", "/bin/sh", "-c", `printf %s "$TOKEN"`); outcome.Code != 0 || stdout != "***" {
		t.Fatalf("run -e: %+v stdout %q", outcome, stdout)
	}

	marker := filepath.Join(t.TempDir(), "started")
	for _, test := range []struct {
		args []string
		code int
		kind string
	}{
		{[]string{"run", "-n", "missing", "--", "/usr/bin/touch", marker}, 125, "not_found"},
		{[]string{"run", "-e", "app/MISSING", "--", "/usr/bin/touch", marker}, 125, "not_found"},
		{[]string{"run", "-e", "app", "--", "/usr/bin/touch", marker}, 125, "usage"},
		{[]string{"run", "-n", "app", "/usr/bin/touch", marker}, 125, "usage"},
		{[]string{"run", "-n", "app", "/usr/bin/touch", "--", marker}, 125, "usage"},
		{[]string{"run", "-n", "app", "--"}, 125, "usage"},
		{[]string{"run", "--bogus", "--", "/usr/bin/touch", marker}, 125, "usage"},
		{[]string{"run", "-n", "app", "--", "maskey-no-such-command"}, 127, "command_not_found"},
	} {
		outcome, stdout, stderr := h.run(t, "", test.args...)
		if outcome.Code != test.code || stdout != "" || errorKind(stderr) != test.kind {
			t.Errorf("%q: %+v stdout %q stderr %s", test.args, outcome, stdout, stderr)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a command started despite a failed selection")
	}
}

// An interruption after the values were read, before the command started,
// keeps it from starting.
func TestRunInterruptedBeforeStart(t *testing.T) {
	h := newHarness(t)
	h.run(t, "v", "set", "--stdin", "app/TOKEN")
	ctx, cancel := context.WithCancel(context.Background())
	h.backend.Before = func(op, _ string) error {
		if op == "read" {
			cancel()
		}
		return nil
	}
	marker := filepath.Join(t.TempDir(), "started")
	var stderr bytes.Buffer
	outcome := Run(ctx, []string{"run", "-n", "app", "--", "/usr/bin/touch", marker}, h.env(nil, io.Discard, &stderr))
	if outcome.Code != 125 || errorKind(stderr.String()) != "cancelled" {
		t.Fatalf("%+v %s", outcome, stderr.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command started")
	}
}

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	h.run(t, "v", "set", "--stdin", "app/TOKEN")
	h.run(t, "", "skill", "install")
	outcome, stdout, _ := h.run(t, "", "doctor")
	want := `ok       interpreter  /usr/bin/python3
ok       keychain     1 entries in 1 namespaces
ok       service      running this build
ok       skill        linked in ~/.claude/skills and ~/.agents/skills
`
	if outcome.Code != 0 || stdout != want {
		t.Fatalf("doctor: %+v\n%s", outcome, stdout)
	}

	// A missing service is a warning; the first failed check decides the
	// status.
	h.service = &store.Error{Kind: store.KindUnavailable, Message: "the Keychain service is not reachable"}
	h.check = &store.Error{Kind: store.KindPrerequisite, Message: "no interpreter"}
	h.backend.Before = func(string, string) error { return &store.Error{Kind: store.KindLocked, Message: "locked"} }
	outcome, stdout, _ = h.run(t, "", "doctor", "--json")
	document := decode(t, stdout)
	checks, _ := document["checks"].([]any)
	statuses := ""
	for _, c := range checks {
		statuses += c.(map[string]any)["status"].(string) + " "
	}
	if outcome.Code != 6 || document["version"] != "1.2.3" || statuses != "error error warning ok " {
		t.Fatalf("doctor: %+v %s", outcome, stdout)
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	_, root, _ := h.run(t, "", "--help")
	for _, part := range []string{"Core Commands:\n  run ", "Setup Commands:\n  doctor ", "Examples:\n", "mk exit-codes", "--version"} {
		if !strings.Contains(root, part) {
			t.Errorf("root help lacks %q:\n%s", part, root)
		}
	}
	if _, bare, _ := h.run(t, ""); bare != root {
		t.Errorf("bare mk printed %q", bare)
	}
	_, list, _ := h.run(t, "", "list", "--help")
	if _, viaHelp, _ := h.run(t, "", "help", "list"); viaHelp != list || !strings.Contains(list, "Usage:\n  mk list [<namespace>]\n") {
		t.Errorf("list help %q, help list %q", list, viaHelp)
	}
	if outcome, topic, _ := h.run(t, "", "help", "exit-codes"); outcome.Code != 0 || !strings.Contains(topic, "10   unavailable") {
		t.Errorf("exit-codes: %+v %q", outcome, topic)
	}
	if _, version, _ := h.run(t, "", "--version"); version != "mk version 1.2.3\n" {
		t.Errorf("version %q", version)
	}
	// -v stays free: it is ambiguous between verbose and version.
	if outcome, _, _ := h.run(t, "", "-v"); outcome.Code != 2 {
		t.Errorf("-v: %+v", outcome)
	}
}

func TestSetup(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
	}{
		{"success", nil, 0},
		// The installer's own errors are plain; they become prerequisite.
		{"failure", errors.New("synthetic setup failure"), 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			var stdout, stderr bytes.Buffer
			calls := 0
			env := h.env(nil, &stdout, &stderr)
			env.Setup = func(context.Context) error { calls++; return test.err }
			outcome := Run(context.Background(), []string{"setup"}, env)
			if calls != 1 || outcome.Code != test.code {
				t.Fatalf("setup calls=%d, %+v, stdout %q, stderr %q", calls, outcome, &stdout, &stderr)
			}
			if test.err == nil && stdout.String() != "installed the Keychain service\n" || test.err != nil && errorKind(stderr.String()) != "prerequisite" {
				t.Fatalf("stdout %q stderr %q", &stdout, &stderr)
			}
		})
	}
}

func TestSkill(t *testing.T) {
	h := newHarness(t)
	if _, stdout, _ := h.run(t, "", "skill", "show"); stdout != "synthetic skill\n" {
		t.Fatalf("show: %q", stdout)
	}
	claude := filepath.Join(h.home, ".claude", "skills", "maskey")
	agents := filepath.Join(h.home, ".agents", "skills", "maskey")
	// A link left by another copy of the app is replaced.
	os.MkdirAll(filepath.Dir(agents), 0o755)
	os.Symlink(t.TempDir(), agents)
	for range 2 {
		if outcome, stdout, stderr := h.run(t, "", "skill", "install"); outcome.Code != 0 || stdout != "linked "+claude+"\nlinked "+agents+"\n" {
			t.Fatalf("install: %+v %q %q", outcome, stdout, stderr)
		}
	}
	for _, link := range []string{claude, agents} {
		if target, err := os.Readlink(link); err != nil || target != h.source {
			t.Fatalf("%s links to %q, %v", link, target, err)
		}
	}
	// A directory of the user's own is left alone.
	os.Remove(claude)
	os.Mkdir(claude, 0o755)
	if outcome, _, stderr := h.run(t, "", "skill", "install"); outcome.Code != 4 || errorKind(stderr) != "exists" {
		t.Fatalf("install over a directory: %+v %q", outcome, stderr)
	}
	h.source = filepath.Join(t.TempDir(), "missing")
	if outcome, _, stderr := h.run(t, "", "skill", "install"); outcome.Code != 6 || errorKind(stderr) != "prerequisite" {
		t.Fatalf("install outside the app: %+v %q", outcome, stderr)
	}
}

func TestCompletion(t *testing.T) {
	h := newHarness(t)
	h.run(t, "v", "set", "--stdin", "app/TOKEN")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "run", "-n", ""}, "app\n:4\n"},
		{[]string{"__complete", "delete", ""}, "app/TOKEN\n:4\n"},
		{[]string{"__complete", "run", "-n", "app", "--", ""}, ":0\n"},
	} {
		if _, stdout, _ := h.run(t, "", test.args...); !strings.HasPrefix(stdout, test.want) {
			t.Errorf("%q: %q", test.args, stdout)
		}
	}
}
