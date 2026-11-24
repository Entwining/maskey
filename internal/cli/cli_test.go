package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
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
	// terminal is the controlling terminal, when the process has one, and
	// terminalOpens counts the attempts to open it.
	terminal      *os.File
	terminalOpens int
	// config is this Mac's stores, and work the store named "work" when
	// config lists it. storeChanges are the changes asked of the Maskey
	// service, which fail with storeErr, and added the last store added.
	// configErr is why config cannot be read. With failingStore, only
	// changes to that store fail.
	config       stores.Config
	configErr    error
	work         *storetest.Backend
	storeChanges []string
	storeErr     error
	failingStore string
	added        stores.Addition
}

// connect adds the store named work to this Mac's stores.
func (h *harness) connect() {
	h.config.Stores = append(h.config.Stores, stores.Connection{ID: "work", Type: stores.Bitwarden, Region: "eu", Organization: "org-1", Project: "p-1", ProjectName: "Deploy"})
}

// access is the store named id as main opens it.
func (h *harness) access(id string) (StoreAccess, error) {
	connection, err := h.config.Resolve(id)
	if err != nil {
		return StoreAccess{}, err
	}
	opened := h.store
	if connection.ID != stores.ICloud {
		opened = store.New(h.work, new(storetest.Recorder))
	}
	return StoreAccess{Connection: connection, Store: opened, ChangeAccess: func(ctx context.Context, refs []store.Ref, granted bool) ([]store.AccessChange, string, error) {
		approve := h.approve
		if approve == nil {
			approve = func(context.Context, string) (string, error) { return "dialog", nil }
		}
		return opened.ChangeAccess(ctx, refs, granted, approve)
	}}, nil
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
	return &harness{backend: backend, store: store.New(backend, new(storetest.Recorder)), activity: activity.New(logs, nil), logs: logs, config: stores.Config{Default: stores.ICloud}, work: storetest.New(), home: t.TempDir(), executable: executable, source: source}
}

func (h *harness) env(stdin *os.File, stdout, stderr io.Writer) Env {
	return Env{
		OpenStore: func(_ context.Context, id string) (StoreAccess, error) { return h.access(id) },
		Stores:    func(context.Context) (stores.Config, error) { return h.config, h.configErr },
		AddStore: func(_ context.Context, addition stores.Addition) (stores.Connection, error) {
			h.added = addition
			return stores.Connection{ID: cmp.Or(addition.ID, stores.Bitwarden), Type: addition.Type, Region: addition.Region,
				Organization: "org-1", Project: "p-1", ProjectName: "Deploy"}, h.storeErr
		},
		SetToken: func(_ context.Context, id, target string, token []byte) error {
			h.storeChanges = append(h.storeChanges, fmt.Sprintf("token %s target=%q %s", id, target, token))
			return h.storeErr
		},
		ChangeStore: func(_ context.Context, op, id string) error {
			h.storeChanges = append(h.storeChanges, op+" "+id)
			if _, ok := h.config.Find(id); !ok {
				return stores.ErrNoStore
			}
			if h.failingStore != "" && h.failingStore != id {
				return nil
			}
			return h.storeErr
		},
		RemoveStore: func(_ context.Context, connection stores.Connection) error {
			h.storeChanges = append(h.storeChanges, fmt.Sprintf("store_remove %s target=%q", connection.ID, connection.Target()))
			return h.storeErr
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
		Terminal: func() (*os.File, error) {
			h.terminalOpens++
			if h.terminal == nil {
				return nil, errors.New("no controlling terminal")
			}
			return h.terminal, nil
		},
		Stdout:  stdout,
		Stderr:  stderr,
		Version: "1.2.3",
	}
}

// noService lists and removes the Maskey service where it is not loaded
// and none of its files exist.
func noService(context.Context, string) []agent.ServiceItem {
	return []agent.ServiceItem{{Item: "Maskey service", Status: "absent"}}
}

// run executes a command line with stdin holding input.
func (h *harness) run(t *testing.T, input string, args ...string) (Outcome, string, string) {
	t.Helper()
	return h.runOn(stdinHolding(t, input), args...)
}

// stdinHolding is a file to read input from as stdin.
func stdinHolding(t *testing.T, input string) *os.File {
	t.Helper()
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close() })
	stdin.WriteString(input)
	stdin.Seek(0, io.SeekStart)
	return stdin
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
