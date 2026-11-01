package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testLog(t *testing.T, now *time.Time) *Log {
	t.Helper()
	l := New(filepath.Join(t.TempDir(), "Logs"), func(err error) { t.Errorf("recording failed: %v", err) })
	l.now = func() time.Time { return *now }
	// The append-only flag refuses the removal of the test's directory.
	t.Cleanup(func() {
		entries, _ := os.ReadDir(l.dir)
		for _, entry := range entries {
			clearAppendOnly(filepath.Join(l.dir, entry.Name()))
		}
	})
	return l
}

// The record format is a contract its readers rely on: any change to the
// fields an operation records must be a deliberate edit of this test.
func TestRecordFields(t *testing.T) {
	now := time.Date(2026, 11, 1, 5, 30, 12, 123456789, time.UTC)
	caller := Caller{Exe: "/bin/zsh", Chain: []string{"/usr/bin/login", "/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"}, TTY: "ttys001"}
	ctx := WithCaller(WithAuth(WithVia(context.Background(), "run"), "dialog"), caller)
	base := []string{"caller", "entry", "op", "result", "time", "v"}
	for _, test := range []struct {
		record Record
		extra  []string
	}{
		{Record{Op: "get", Entry: "ns/A", Result: "not_granted"}, nil},
		{Record{Op: "read", Entry: "ns/A", Result: "ok"}, []string{"via"}},
		{Record{Op: "add", Entry: "ns/A", Result: "ok"}, nil},
		{Record{Op: "update", Entry: "ns/A", Result: "uncertain"}, nil},
		{Record{Op: "edit", Entry: "ns/A", To: "ns/B", Result: "ok"}, []string{"to"}},
		{Record{Op: "delete", Entry: "ns/B", Result: "not_found"}, nil},
		{Record{Op: "grant", Entry: "ns/B", Result: "ok"}, []string{"auth"}},
		{Record{Op: "revoke", Entry: "ns/B", Result: "cancelled"}, nil},
	} {
		l := testLog(t, &now)
		l.Record(ctx, test.record)
		lines, _, err := l.Read(now.Add(-time.Second), func(Record) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(lines[0].Raw, &fields); err != nil {
			t.Fatal(err)
		}
		want := slices.Sorted(slices.Values(append(slices.Clone(base), test.extra...)))
		if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, want) {
			t.Errorf("%s records %v, want %v", test.record.Op, got, want)
		}
		if got, want := string(fields["caller"]), `{"exe":"/bin/zsh","chain":["/usr/bin/login","/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal"],"tty":"ttys001"}`; got != want {
			t.Errorf("%s records the caller as %s, want %s", test.record.Op, got, want)
		}
		if got := string(fields["time"]); got != `"2026-11-01T05:30:12.123Z"` {
			t.Errorf("time %s, want milliseconds in UTC", got)
		}
	}
}

// Each record is one line appended to its month's file, which only its owner
// can read and which refuses anything but appending.
func TestRecordFile(t *testing.T) {
	now := time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)
	l := testLog(t, &now)
	for range 2 {
		l.Record(context.Background(), Record{Op: "get", Entry: "ns/A", Result: "ok"})
	}
	path := filepath.Join(l.dir, "activity-2026-11.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Split(data, []byte("\n")); len(lines) != 3 || len(lines[2]) != 0 {
		t.Fatalf("file holds %q, want two lines", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file: %v, %v", info, err)
	}
	if err := os.WriteFile(path, nil, 0600); err == nil {
		t.Fatal("the record was truncated")
	}
	if dir, err := os.Stat(l.dir); err != nil || dir.Mode().Perm() != 0700 {
		t.Fatalf("directory: %v, %v", dir, err)
	}
}

// The process that starts a month removes the files of months more than
// twelve before it, append-only or not, and nothing else.
func TestPrune(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	l := testLog(t, &now)
	if err := os.MkdirAll(l.dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"activity-2025-11.jsonl", "activity-2025-12.jsonl", "activity-2026-11.jsonl", "notes.txt"} {
		path := filepath.Join(l.dir, name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := setAppendOnly(int(file.Fd())); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	names := func() []string {
		entries, _ := os.ReadDir(l.dir)
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	l.Record(context.Background(), Record{Op: "get", Entry: "ns/A", Result: "ok"})
	if got := names(); len(got) != 4 {
		t.Fatalf("a month's later record pruned: %v", got)
	}
	now = now.AddDate(0, 1, 0)
	l.Record(context.Background(), Record{Op: "get", Entry: "ns/A", Result: "ok"})
	want := []string{"activity-2025-12.jsonl", "activity-2026-11.jsonl", "activity-2026-12.jsonl", "notes.txt"}
	if got := names(); !slices.Equal(got, want) {
		t.Fatalf("after the month's first record: %v, want %v", got, want)
	}
}

// A failure to record is reported once per process, whether it happened
// here or in the Keychain service for this process, and goes instead to a
// context's own report, as the service's for its client.
func TestRecordFailureWarnsOnce(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var warnings, reported []error
	l := New(filepath.Join(blocker, "Logs"), func(err error) { warnings = append(warnings, err) })
	ctx := WithFailureReport(context.Background(), func(err error) { reported = append(reported, err) })
	l.Record(ctx, Record{Op: "get", Entry: "ns/A", Result: "ok"})
	if len(warnings) != 0 || len(reported) != 1 || !errors.Is(reported[0], unix.ENOTDIR) {
		t.Fatalf("warnings %v, reported %v; want one reported", warnings, reported)
	}
	for range 2 {
		l.Record(context.Background(), Record{Op: "get", Entry: "ns/A", Result: "ok"})
	}
	l.ReportFailure(errors.New("the service's failure"))
	if len(warnings) != 1 || !errors.Is(warnings[0], unix.ENOTDIR) {
		t.Fatalf("warnings %v, want one", warnings)
	}
	l = New(t.TempDir(), func(err error) { warnings = append(warnings, err) })
	for range 2 {
		l.ReportFailure(errors.New("the service's failure"))
	}
	if len(warnings) != 2 || warnings[1].Error() != "the service's failure" {
		t.Fatalf("warnings %v, want the service's once", warnings)
	}
}

// A failure to write a record says the operation was not recorded and names
// the file; a failure to prune afterwards says only that.
func TestRecordFailureStage(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	var reported []error
	ctx := WithFailureReport(context.Background(), func(err error) { reported = append(reported, err) })

	readOnly := testLog(t, &now)
	if err := os.Mkdir(readOnly.dir, 0500); err != nil {
		t.Fatal(err)
	}
	readOnly.Record(ctx, Record{Op: "get", Entry: "ns/A", Result: "ok"})
	var path *os.PathError
	if len(reported) != 1 || !strings.HasPrefix(reported[0].Error(), "an operation was not recorded: ") ||
		!errors.As(reported[0], &path) || path.Path != filepath.Join(readOnly.dir, "activity-2026-11.jsonl") {
		t.Fatalf("write failure reported %v", reported)
	}

	reported = nil
	l := testLog(t, &now)
	// A directory named for an old month cannot be removed while it holds a
	// file.
	if err := os.MkdirAll(filepath.Join(l.dir, "activity-2024-01.jsonl", "file"), 0700); err != nil {
		t.Fatal(err)
	}
	l.Record(ctx, Record{Op: "get", Entry: "ns/A", Result: "ok"})
	lines, _, err := l.Read(now.Add(-time.Second), func(Record) bool { return true })
	if err != nil || len(lines) != 1 {
		t.Fatalf("the record before the prune: %v, %v", lines, err)
	}
	if len(reported) != 1 || !strings.HasPrefix(reported[0].Error(), "older activity files were not removed: ") {
		t.Fatalf("prune failure reported %v", reported)
	}
}

// Writable answers before any write whether the record's directory, or the
// directory that would hold it, accepts writes.
func TestWritable(t *testing.T) {
	writable := t.TempDir()
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0500); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		dir  string
		want bool
	}{
		{writable, true},
		{filepath.Join(writable, "Logs"), true},
		{readOnly, false},
		{filepath.Join(readOnly, "Logs"), false},
		{filepath.Join(file, "Logs"), false},
	} {
		if got := New(test.dir, nil).Writable(); got != test.want {
			t.Errorf("Writable() in %s = %v, want %v", test.dir, got, test.want)
		}
	}
	if entries, _ := os.ReadDir(writable); len(entries) != 0 {
		t.Fatalf("Writable created %v", entries)
	}
}

// Read returns the matching records of the window newest first, and counts
// the lines it cannot use instead of failing on them.
func TestRead(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	l := testLog(t, &now)
	if err := os.MkdirAll(l.dir, 0700); err != nil {
		t.Fatal(err)
	}
	line := func(at, entry string) string {
		return `{"v":1,"time":"` + at + `","op":"get","entry":"` + entry + `","result":"ok","caller":{"exe":"/bin/sh","chain":[]}}`
	}
	files := map[string][]string{
		// Outside the window: never opened, so its bad line is not counted.
		"activity-2026-09.jsonl": {"not a record"},
		"activity-2026-10.jsonl": {
			line("2026-10-20T00:00:00.000Z", "ns/OLD"),
			line("2026-10-30T00:00:00.000Z", "ns/A"),
			`{"v":2,"time":"2026-10-30T00:00:00.000Z"}`,
		},
		"activity-2026-11.jsonl": {
			line("2026-11-02T00:00:00.000Z", "ns/A"),
			line("2026-11-01T00:00:00.000Z", "other/B"),
			"{" + strings.Repeat(" ", maxLine) + "}",
			`{"v":1,"time":"yesterday"}`,
			line("2026-11-01T06:00:00.000Z", "ns/C"),
		},
	}
	for name, lines := range files {
		if err := os.WriteFile(filepath.Join(l.dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	lines, skipped, err := l.Read(since, func(r Record) bool { return r.Entry != "ns/C" })
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range lines {
		got = append(got, line.Time+" "+line.Entry)
	}
	want := []string{"2026-11-02T00:00:00.000Z ns/A", "2026-11-01T00:00:00.000Z other/B", "2026-10-30T00:00:00.000Z ns/A"}
	if !slices.Equal(got, want) || skipped != (Skipped{Unreadable: 2, Newer: 1}) {
		t.Fatalf("read %v, skipped %+v; want %v, 2 unreadable, 1 newer", got, skipped, want)
	}
}
