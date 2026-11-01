package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// terminal returns a terminal whose input holds what was typed.
func terminal(t *testing.T, typed string) *os.File {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); tty.Close() })
	if _, err := master.WriteString(typed); err != nil {
		t.Fatal(err)
	}
	return tty
}

// A piped value of one line loses its line ending, as gh auth token or echo
// adds one; a value of several lines keeps every byte.
func TestSetStdinLineEnding(t *testing.T) {
	h := newHarness(t)
	for _, test := range []struct{ input, stored string }{
		{"token\n", "token"},
		{"token\r\n", "token"},
		{"token", "token"},
		{"line 1\nline 2\n", "line 1\nline 2\n"},
		{"token\n\n", "token\n\n"},
		{"to\rken\n", "to\rken"},
	} {
		if outcome, _, stderr := h.run(t, test.input, "set", "--stdin", "app/TOKEN"); outcome.Code != 0 || h.value("app/TOKEN") != test.stored {
			t.Errorf("stdin %q stored %q: %+v %s", test.input, h.value("app/TOKEN"), outcome, stderr)
		}
	}
	for _, input := range []string{"\n", "\r\n", "", "a\x00b"} {
		if outcome, _, stderr := h.run(t, input, "set", "--stdin", "app/TOKEN"); outcome.Code != 2 || !strings.HasPrefix(stderr, "mk: error[invalid]: argument 1: the value ") {
			t.Errorf("stdin %q: %+v %s", input, outcome, stderr)
		}
	}
}

// Each hidden prompt names its entry, and its place in a batch; every
// reference is checked before the first prompt.
func TestSetPrompts(t *testing.T) {
	h := newHarness(t)
	outcome, stdout, stderr := h.runOn(terminal(t, "one\ntwo\n"), "set", "app/ONE", "app/TWO")
	if outcome.Code != 0 || stdout != "created app/ONE\ncreated app/TWO\n" || h.value("app/ONE") != "one" || h.value("app/TWO") != "two" {
		t.Fatalf("batch: %+v %q %q", outcome, stdout, stderr)
	}
	if want := "Value for app/ONE (1 of 2, input hidden): \nValue for app/TWO (2 of 2, input hidden): \n"; stderr != want {
		t.Fatalf("batch prompts %q, want %q", stderr, want)
	}
	if outcome, _, stderr := h.runOn(terminal(t, "three\n"), "set", "app/ONE"); outcome.Code != 0 || stderr != "Value for app/ONE (input hidden): \n" || h.value("app/ONE") != "three" {
		t.Fatalf("single: %+v %q", outcome, stderr)
	}
	// An empty value fails before anything is written.
	if outcome, _, stderr := h.runOn(terminal(t, "four\n\n"), "set", "app/ONE", "app/TWO"); outcome.Code != 2 || !strings.HasSuffix(stderr, "mk: error[invalid]: argument 2: the value is empty\n") || h.value("app/ONE") != "three" {
		t.Fatalf("empty second value: %+v %q", outcome, stderr)
	}
	if outcome, _, stderr := h.runOn(terminal(t, "five\n"), "set", "app/ONE", ".app/TWO"); outcome.Code != 2 || strings.Contains(stderr, "Value for") {
		t.Fatalf("invalid second reference: %+v %q", outcome, stderr)
	}
}

func TestDeleteConfirmation(t *testing.T) {
	const prompt = "Delete 2 entries from this Mac and Macs syncing this Keychain:\n  app/A\n  app/B\nThis cannot be undone. Continue? [y/N] "
	for _, test := range []struct {
		name, typed string
		deleted     bool
	}{
		{"yes", "yes\n", true},
		{"y in capitals", " Y \n", true},
		{"no", "no\n", false},
		{"empty", "\n", false},
		{"unterminated yes", "yes\x04\x04", false},
		{"end of input", "\x04", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.set(t, "app/A", "app/B", "app/C")
			outcome, stdout, stderr := h.runOn(terminal(t, test.typed), "delete", "app/A", "app/B")
			if !strings.HasPrefix(stderr, prompt) {
				t.Fatalf("prompt %q, want %q", stderr, prompt)
			}
			switch {
			case test.deleted && (outcome.Code != 0 || stdout != "deleted app/A\ndeleted app/B\n" || h.list(t) != "app/C\n"):
				t.Fatalf("%+v %q %q", outcome, stdout, h.list(t))
			case !test.deleted && (outcome.Code != 130 || !strings.HasSuffix(stderr, "mk: error[cancelled]: no entries were deleted\n") || h.list(t) != "app/A\napp/B\napp/C\n"):
				t.Fatalf("%+v %q %q", outcome, stderr, h.list(t))
			}
		})
	}
	// Every entry is checked before the question.
	h := newHarness(t)
	h.set(t, "app/A")
	if outcome, _, stderr := h.runOn(terminal(t, "yes\n"), "delete", "app/A", "app/MISSING"); outcome.Code != 3 || strings.Contains(stderr, "Delete") || h.list(t) != "app/A\n" {
		t.Fatalf("missing second entry: %+v %q", outcome, stderr)
	}
}

// Without a terminal to prompt on, set needs --stdin, and delete and
// uninstall --yes.
func TestNonInteractiveNeedsFlags(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/TOKEN")
	for _, test := range []struct {
		args []string
		flag string
	}{
		{[]string{"set", "app/TOKEN"}, "--stdin"},
		{[]string{"delete", "app/TOKEN"}, "--yes"},
		{[]string{"uninstall"}, "--yes"},
	} {
		if outcome, _, stderr := h.run(t, "y\n", test.args...); outcome.Code != 2 || !strings.HasPrefix(stderr, "mk: error[usage]: standard input is not a terminal and "+test.args[0]+" needs "+test.flag+"\n") {
			t.Errorf("%q: %+v %q", test.args, outcome, stderr)
		}
	}
	if h.list(t) != "app/TOKEN\n" || h.value("app/TOKEN") != "synthetic-app/TOKEN" {
		t.Fatal("the entry changed")
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
		if outcome.Code != 130 || stderr.String() != "mk: error[cancelled]: cancelled before any entry was written\n" {
			t.Fatalf("%+v %s", outcome, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("set kept reading after it was cancelled")
	}
}
