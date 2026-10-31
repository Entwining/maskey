package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
)

func TestRun(t *testing.T) {
	h := newHarness(t)
	h.run(t, "synthetic-c3d4", "set", "--stdin", "app/TOKEN")
	h.run(t, "synthetic-e5f6", "set", "--stdin", "app/URL")
	h.run(t, "synthetic-a7b8", "set", "--stdin", "work/TOKEN")

	// Everything after the first -- is the command's, and stdin reaches it.
	outcome, stdout, stderr := h.run(t, "piped input", "run", "--json", "app", "--", "/bin/sh", "-c", `cat; printf ' %s %s out %s' "$1" "$2" "$TOKEN"; printf 'err %s' "$URL" >&2; exit 7`, "sh", "--json", "--")
	if outcome.Code != 7 || stdout != "piped input --json -- out ***" || stderr != "err ***" {
		t.Fatalf("run: %+v stdout %q stderr %q", outcome, stdout, stderr)
	}
	// An entry replaces the same name from a namespace, whose other entries
	// stay; repeating a selector changes nothing.
	outcome, stdout, _ = h.run(t, "", "run", "app", "work/TOKEN", "app", "--", "/bin/sh", "-c", `[ "$TOKEN" = synthetic-a7b8 ] && printf %s "$URL"`)
	if outcome.Code != 0 || stdout != "***" {
		t.Fatalf("entry over namespace: %+v stdout %q", outcome, stdout)
	}
	if outcome, stdout, _ := h.run(t, "", "run", "app/TOKEN", "--", "/bin/sh", "-c", `printf %s "$TOKEN${URL:-unset}"`); outcome.Code != 0 || stdout != "***unset" {
		t.Fatalf("one entry: %+v stdout %q", outcome, stdout)
	}

	marker := filepath.Join(t.TempDir(), "started")
	for _, test := range []struct {
		args []string
		code int
		err  string
	}{
		{[]string{"run", "app", "work", "--", "/usr/bin/touch", marker}, 125, "conflict]: app and work both supply TOKEN"},
		{[]string{"run", "app/TOKEN", "work/TOKEN", "--", "/usr/bin/touch", marker}, 125, "conflict]: app/TOKEN and work/TOKEN both set TOKEN"},
		{[]string{"run", "app", "missing", "--", "/usr/bin/touch", marker}, 125, "not_found]: argument 2: the namespace has no entries"},
		{[]string{"run", "app/MISSING", "--", "/usr/bin/touch", marker}, 125, "not_found]: argument 1: the entry does not exist"},
		{[]string{"run", "app/", "--", "/usr/bin/touch", marker}, 125, "invalid]: argument 1: NAME starts with an ASCII letter or '_', followed by ASCII letters, digits or '_'\n"},
		{[]string{"run", "app", ".app", "--", "/usr/bin/touch", marker}, 125, "invalid]: argument 2: a namespace starts with an ASCII letter or digit, followed by ASCII letters, digits, '.', '_' or '-'\n"},
		{[]string{"run", "-", "--", "/usr/bin/touch", marker}, 125, "invalid]: argument 1: a namespace starts with"},
		{[]string{"run", "app", "/usr/bin/touch", marker}, 125, "usage]: run needs -- before the command"},
		{[]string{"run", "--", "/usr/bin/touch", marker}, 125, "usage]: run needs at least one namespace or entry before --"},
		{[]string{"run", "app", "--"}, 125, "usage]: run needs a command after --"},
		{[]string{"run", "app", "--json", "--", "/usr/bin/touch", marker}, 125, "usage]: mk flags must precede the first selector"},
		{[]string{"run", "-n", "app", "--", "/usr/bin/touch", marker}, 125, "usage]: run no longer accepts -n, --namespace, -e, or --entry"},
		{[]string{"run", "app", "--entry=app/TOKEN", "--", "/usr/bin/touch", marker}, 125, "usage]: run no longer accepts -n, --namespace, -e, or --entry"},
		{[]string{"run", "--bogus", "app", "--", "/usr/bin/touch", marker}, 125, "usage]: unknown flag"},
		{[]string{"run", "app", "--", "maskey-no-such-command"}, 127, "command_not_found]: "},
	} {
		outcome, stdout, stderr := h.run(t, "", test.args...)
		if outcome.Code != test.code || stdout != "" || !strings.HasPrefix(stderr, "mk: error["+test.err) {
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
	h.set(t, "app/TOKEN")
	ctx, cancel := context.WithCancel(context.Background())
	h.backend.Before = func(op, _ string) error {
		if op == "read" {
			cancel()
		}
		return nil
	}
	marker := filepath.Join(t.TempDir(), "started")
	var stderr bytes.Buffer
	outcome := Run(ctx, []string{"run", "app", "--", "/usr/bin/touch", marker}, h.env(nil, io.Discard, &stderr))
	if outcome.Code != 125 || errorKind(stderr.String()) != "cancelled" {
		t.Fatalf("%+v %s", outcome, stderr.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command started")
	}
}

type taggedReads struct {
	*storetest.Backend
	reads *[]string
}

func (b taggedReads) Read(ctx context.Context, accounts []string) (map[string][]byte, error) {
	*b.reads = append(*b.reads, activity.ViaOf(ctx))
	return b.Backend.Read(ctx, accounts)
}

// mk run's reads say they are for a command, which the activity record keeps.
func TestRunTagsReads(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/TOKEN")
	var reads []string
	h.store = store.New(taggedReads{h.backend, &reads}, nil)
	if outcome, _, stderr := h.run(t, "", "run", "app", "--", "true"); outcome.Code != 0 || !slices.Equal(reads, []string{"run"}) {
		t.Fatalf("run: %+v %q, reads for %q", outcome, stderr, reads)
	}
}
