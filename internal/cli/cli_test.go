package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

type harness struct {
	backend  *storetest.Backend
	store    *store.Store
	activity *activity.Log
	logs     string // the activity record's directory
	// approve answers for the owner; it approves unless set.
	approve store.Approve
	check   error
	service error
	home    string
	// executable is the app's executable, and source the skill's directory
	// beside it.
	executable string
	source     string
}

func newHarness(t *testing.T) *harness {
	backend := storetest.New()
	// Resolved, as main resolves it: the temporary directory is behind a link.
	contents, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	contents = filepath.Join(contents, "Maskey.app", "Contents")
	source := filepath.Join(contents, "Resources", "skills", "maskey")
	executable := filepath.Join(contents, "MacOS", "Maskey")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(t.TempDir(), "Logs")
	return &harness{backend: backend, store: store.New(backend, new(storetest.Recorder)), activity: activity.New(logs, nil), logs: logs, home: t.TempDir(), executable: executable, source: source}
}

func (h *harness) env(stdin *os.File, stdout, stderr io.Writer) Env {
	return Env{
		Store: h.store,
		ChangeAccess: func(ctx context.Context, refs []store.Ref, granted bool) ([]store.AccessChange, string, error) {
			approve := h.approve
			if approve == nil {
				approve = func(context.Context, string) (string, error) { return "dialog", nil }
			}
			return h.store.ChangeAccess(ctx, refs, granted, approve)
		},
		Activity:      h.activity,
		Check:         func(context.Context) error { return h.check },
		CheckService:  func(context.Context) error { return h.service },
		ServiceItems:  noService,
		RemoveService: noService,
		Skill:         []byte("synthetic skill\n"),
		App:           "Maskey",
		Executable:    h.executable,
		Home:          h.home,
		Stdin:         stdin,
		Stdout:        stdout,
		Stderr:        stderr,
		Version:       "1.2.3",
	}
}

// noService lists and removes the Keychain service where it is not loaded
// and none of its files exist.
func noService(context.Context, string) []agent.ServiceItem {
	return []agent.ServiceItem{{Item: "Keychain service", Status: "absent"}}
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
	return h.runOn(stdin, args...)
}

func (h *harness) runOn(stdin *os.File, args ...string) (Outcome, string, string) {
	var stdout, stderr bytes.Buffer
	outcome := Run(context.Background(), args, h.env(stdin, &stdout, &stderr))
	return outcome, stdout.String(), stderr.String()
}

// set stores entries the way a pipe to mk set --stdin does.
func (h *harness) set(t *testing.T, refs ...string) {
	t.Helper()
	for _, ref := range refs {
		if outcome, _, stderr := h.run(t, "synthetic-"+ref, "set", "--stdin", ref); outcome.Code != 0 {
			t.Fatalf("set %s: %+v %s", ref, outcome, stderr)
		}
	}
}

func (h *harness) value(ref string) string {
	namespace, name, _ := strings.Cut(ref, "/")
	value, _ := h.store.Value(context.Background(), store.Ref{Namespace: namespace, Name: name})
	return string(value)
}

func (h *harness) list(t *testing.T) string {
	t.Helper()
	_, stdout, _ := h.run(t, "", "list")
	return stdout
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
