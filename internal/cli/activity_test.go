package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func TestActivity(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	agent := activity.Caller{Exe: "/bin/zsh", Chain: []string{"/usr/local/bin/claude", "/Applications/Rex.app/Contents/MacOS/Rex"}}
	person := activity.Caller{Exe: "/bin/zsh", Chain: []string{"/usr/bin/login", "/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"}, TTY: "ttys003"}
	window := activity.Caller{Exe: "/Applications/Maskey.app/Contents/MacOS/Maskey", Chain: []string{}}
	records := []activity.Record{
		{Op: "edit", Entry: "app/A", To: "other/B", Result: "ok", Caller: agent},
		{Op: "read", Entry: "app/A", Result: "ok", Via: "run", Caller: agent},
		{Op: "get", Entry: "github/TOKEN", Result: "ok", Caller: person},
		{Op: "get", Entry: "github/TOKEN", Result: "ok", Caller: agent},
		{Op: "get", Entry: "github/TOKEN", Result: "ok", Caller: agent},
		{Op: "grant", Entry: "github/TOKEN", Result: "ok", Auth: "dialog", Caller: person},
		{Op: "read", Entry: "app/A", Result: "ok", Via: "reveal", Caller: window},
		// Older than the default week.
		{Op: "get", Entry: "github/OLD", Result: "not_granted", Caller: agent},
	}
	times := make([]time.Time, len(records))
	files := map[string][]string{}
	for i, r := range records {
		times[i] = now.Add(-time.Duration(i+1) * time.Hour)
		if r.Entry == "github/OLD" {
			times[i] = now.AddDate(0, 0, -10)
		}
		r.V, r.Time = 1, times[i].Format("2006-01-02T15:04:05.000Z07:00")
		line, _ := json.Marshal(r)
		name := times[i].Format("activity-2006-01.jsonl")
		// Files hold the oldest record first.
		files[name] = append([]string{string(line)}, files[name]...)
	}
	current := now.Format("activity-2006-01.jsonl")
	files[current] = append(files[current], "not a record")
	// Only a read reaching back three months finds these.
	older := now.AddDate(0, -3, 0)
	files[older.Format("activity-2006-01.jsonl")] = []string{"{}", fmt.Sprintf(`{"v":2,"time":%q}`, older.Format(time.RFC3339))}
	os.MkdirAll(h.logs, 0700)
	for name, lines := range files {
		if err := os.WriteFile(filepath.Join(h.logs, name), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(i int) string { return times[i].Local().Format(timeLayout) }
	cells := func(stdout string) [][]string {
		var rows [][]string
		for _, row := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
			rows = append(rows, regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(row), -1))
		}
		return rows
	}

	// Groups, newest first, split by any difference in the caller's chain.
	outcome, stdout, stderr := h.run(t, "", "activity")
	want := [][]string{
		{"LATEST", "COUNT", "ENTRY", "OP", "RESULT", "CALLER"},
		{at(0), "1", "app/A -> other/B", "edit", "ok", "zsh < claude < Rex"},
		{at(1), "1", "app/A", "read (run)", "ok", "zsh < claude < Rex"},
		{at(2), "1", "github/TOKEN", "get", "ok", "zsh < login < Terminal"},
		{at(3), "2", "github/TOKEN", "get", "ok", "zsh < claude < Rex"},
		{at(5), "1", "github/TOKEN", "grant", "ok", "zsh < login < Terminal"},
		{at(6), "1", "app/A", "read (reveal)", "ok", "Maskey"},
	}
	if outcome.Code != 0 || !slices.EqualFunc(cells(stdout), want, slices.Equal) || stderr != "mk: warning: skipped 1 unreadable line\n" {
		t.Fatalf("activity: %+v %q\n%s", outcome, stderr, stdout)
	}

	// Each record, newest first, with full paths, the terminal, and the approval.
	_, stdout, _ = h.run(t, "", "activity", "--full", "github")
	want = [][]string{
		{"TIME", "OP", "ENTRY", "RESULT", "CALLER"},
		{times[2].Local().Format(time.DateTime), "get", "github/TOKEN", "ok", "/bin/zsh < /usr/bin/login < /System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal on ttys003"},
		{times[3].Local().Format(time.DateTime), "get", "github/TOKEN", "ok", "/bin/zsh < /usr/local/bin/claude < /Applications/Rex.app/Contents/MacOS/Rex"},
		{times[4].Local().Format(time.DateTime), "get", "github/TOKEN", "ok", "/bin/zsh < /usr/local/bin/claude < /Applications/Rex.app/Contents/MacOS/Rex"},
		{times[5].Local().Format(time.DateTime), "grant (dialog)", "github/TOKEN", "ok", "/bin/zsh < /usr/bin/login < /System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal on ttys003"},
	}
	if got := cells(stdout); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("activity --full github:\n%s", stdout)
	}

	if _, _, stderr := h.run(t, "", "activity", "--since", "120d"); stderr != "mk: warning: skipped 2 unreadable lines and 1 record of a later Maskey\n" {
		t.Errorf("activity --since 120d: %q", stderr)
	}
	_, stdout, _ = h.run(t, "", "activity", "--since", "30d", "github")
	if !strings.Contains(stdout, "github/OLD") || strings.Contains(stdout, "app/") {
		t.Errorf("activity --since 30d github:\n%s", stdout)
	}
	// A rename matches by either reference; JSON is the record itself, newest first.
	_, stdout, _ = h.run(t, "", "activity", "--json", "other/B")
	if strings.Count(stdout, "\n") != 1 || !strings.HasPrefix(stdout, `{"v":1,"time":"`) || !strings.Contains(stdout, `"op":"edit","entry":"app/A","to":"other/B"`) {
		t.Errorf("activity --json other/B: %q", stdout)
	}
	_, stdout, _ = h.run(t, "", "activity", "--json", "app")
	if lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n"); len(lines) != 3 || !strings.Contains(lines[0], `"op":"edit"`) || !strings.Contains(lines[1], `"via":"run"`) || !strings.Contains(lines[2], `"via":"reveal"`) {
		t.Errorf("activity --json app: %q", stdout)
	}
	if outcome, stdout, stderr := h.run(t, "", "activity", "missing"); outcome.Code != 0 || stdout != "" || !strings.HasSuffix(stderr, "mk: no activity\n") {
		t.Errorf("no activity: %+v %q %q", outcome, stdout, stderr)
	}
	if outcome, _, stderr := h.run(t, "", "activity", "--since", "yesterday"); outcome.Code != 2 || !strings.HasPrefix(stderr, "mk: error[usage]: invalid value for --since\n") {
		t.Errorf("--since yesterday: %+v %q", outcome, stderr)
	}
}

// A record that cannot be read fails with its cause and where to look.
func TestActivityReadFailure(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	path := filepath.Join(h.logs, now.Format("activity-2006-01.jsonl"))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	outcome, _, stderr := h.run(t, "", "activity")
	if outcome.Code != 12 || !strings.HasPrefix(stderr, "mk: error[io]: cannot read the activity record: ") || !strings.Contains(stderr, path) || !strings.Contains(stderr, "~/Library/Logs/"+store.Service) {
		t.Errorf("activity: %+v %q", outcome, stderr)
	}
}

// recordingBackend records each get, as the Keychain backend does.
type recordingBackend struct {
	*storetest.Backend
	log *activity.Log
}

func (b recordingBackend) Get(ctx context.Context, account string) ([]byte, error) {
	value, err := b.Backend.Get(ctx, account)
	b.log.Record(ctx, activity.Record{Op: "get", Entry: account, Result: string(store.KindOf(err))})
	return value, err
}

// A failure to record warns on stderr, as JSON with --json, and leaves the
// value on stdout and the exit status alone.
func TestRecordFailureWarns(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/TOKEN")
	h.backend.SetProgramAccess(context.Background(), "app/TOKEN", true)
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0600)
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"get", "app/TOKEN"}, "mk: warning: the activity record failed: "},
		{[]string{"get", "--json", "app/TOKEN"}, `{"warning":{"message":"the activity record failed: `},
	} {
		h.activity = activity.New(filepath.Join(blocker, "Logs"), nil)
		h.store = store.New(recordingBackend{h.backend, h.activity}, nil)
		outcome, stdout, stderr := h.run(t, "", test.args...)
		if outcome.Code != 0 || stdout != "synthetic-app/TOKEN" || !strings.HasPrefix(stderr, test.want) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q: %+v %q %q", test.args, outcome, stdout, stderr)
		}
	}
}
