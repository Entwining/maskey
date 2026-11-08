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
	_, tty := openTerminal(t, typed)
	return tty
}

// openTerminal returns a terminal whose input holds what was typed, and
// the side that shows what is written on it.
func openTerminal(t *testing.T, typed string) (master, tty *os.File) {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); tty.Close() })
	if _, err := master.WriteString(typed); err != nil {
		t.Fatal(err)
	}
	return master, tty
}

// shown is what was written on the terminal whose other side is master,
// with its line endings turned back into \n.
func shown(t *testing.T, master *os.File) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := master.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(buf[:n]), "\r\n", "\n")
}

// runAsking runs a command whose stdin is a terminal nobody types on, and
// whose controlling terminal is another, where typed is typed. A command
// that waits for stdin ends cancelled after a few seconds.
func (h *harness) runAsking(t *testing.T, typed string, args ...string) (outcome Outcome, stdout, stderr, onTerminal string) {
	t.Helper()
	stdinMaster, stdin := openTerminal(t, "")
	// Ends a read of stdin, which would otherwise keep it from closing.
	t.Cleanup(func() { _, _ = stdinMaster.WriteString("\n") })
	master, tty := openTerminal(t, typed)
	h.terminal, h.terminalOpens = tty, 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	outcome = Run(ctx, args, h.env(stdin, &out, &errOut))
	return outcome, out.String(), errOut.String(), shown(t, master)
}

// Questions and hidden prompts appear on the controlling terminal, which
// mk also reads, so with --json stderr holds only JSON lines.
func TestPromptsOnTerminal(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/A", "app/B")
	const prompt = "Delete 1 entry from this Mac and Macs syncing this Keychain:\n  app/A\nThis cannot be undone. Continue? [y/N] "
	outcome, stdout, stderr, onTerminal := h.runAsking(t, "n\n", "delete", "--json", "app/A")
	if body := decode(t, stderr)["error"].(map[string]any); outcome.Code != 130 || stdout != "" || body["kind"] != "cancelled" || !strings.Contains(onTerminal, prompt) || h.list(t) != "app/A\napp/B\n" {
		t.Fatalf("declined delete: %+v %q %q %q", outcome, stdout, stderr, onTerminal)
	}
	outcome, stdout, stderr, onTerminal = h.runAsking(t, "y\n", "delete", "app/A")
	if outcome.Code != 0 || stdout != "deleted app/A\n" || stderr != "" || !strings.Contains(onTerminal, prompt) {
		t.Fatalf("confirmed delete: %+v %q %q %q", outcome, stdout, stderr, onTerminal)
	}

	outcome, stdout, stderr, onTerminal = h.runAsking(t, "y\nv\n", "set", "--json", "app/B")
	if want := "Replace the value of 1 entry:\n  app/B\nThe current value is not kept. Continue? [y/N] "; outcome.Code != 0 || stdout != `{"results":[{"namespace":"app","name":"B","action":"replaced"}]}`+"\n" || stderr != "" || !strings.Contains(onTerminal, want) || !strings.Contains(onTerminal, "Value for app/B (input hidden): ") || h.value("app/B") != "v" {
		t.Fatalf("set: %+v %q %q %q", outcome, stdout, stderr, onTerminal)
	}
	// One command opens the terminal once, however often it asks.
	outcome, _, stderr, onTerminal = h.runAsking(t, "one\ntwo\n", "set", "app/C", "app/D")
	if outcome.Code != 0 || h.terminalOpens != 1 || !strings.Contains(onTerminal, "Value for app/D (2 of 2, input hidden): ") || h.value("app/D") != "two" {
		t.Fatalf("set two entries: %+v %d %q %q", outcome, h.terminalOpens, stderr, onTerminal)
	}
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
	if outcome, _, stderr := h.runOn(terminal(t, "y\nthree\n"), "set", "app/ONE"); outcome.Code != 0 || stderr != "Replace the value of 1 entry:\n  app/ONE\nThe current value is not kept. Continue? [y/N] Value for app/ONE (input hidden): \n" || h.value("app/ONE") != "three" {
		t.Fatalf("single: %+v %q", outcome, stderr)
	}
	// An empty value fails before anything is written.
	if outcome, _, stderr := h.runOn(terminal(t, "y\nfour\n\n"), "set", "app/ONE", "app/TWO"); outcome.Code != 2 || !strings.HasSuffix(stderr, "mk: error[invalid]: argument 2: the value is empty\n") || h.value("app/ONE") != "three" {
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

// set at its hidden prompt asks once, before the first value, about the
// entries it would replace, and only about those.
func TestSetReplaceConfirmation(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/A")
	outcome, stdout, stderr := h.runOn(terminal(t, "y\none\ntwo\n"), "set", "app/A", "app/NEW")
	want := "Replace the value of 1 entry:\n  app/A\nThe current value is not kept. Continue? [y/N] Value for app/A (1 of 2, input hidden): \nValue for app/NEW (2 of 2, input hidden): \n"
	if outcome.Code != 0 || stdout != "replaced app/A\ncreated app/NEW\n" || stderr != want || h.value("app/A") != "one" || h.value("app/NEW") != "two" {
		t.Fatalf("confirmed: %+v %q %q", outcome, stdout, stderr)
	}

	h = newHarness(t)
	h.set(t, "app/A", "app/B")
	outcome, _, stderr = h.runOn(terminal(t, "y\none\ntwo\n"), "set", "app/A", "app/B")
	if want := "Replace the values of 2 entries:\n  app/A\n  app/B\nTheir current values are not kept. Continue? [y/N] Value for app/A "; outcome.Code != 0 || !strings.HasPrefix(stderr, want) {
		t.Fatalf("plural: %+v %q", outcome, stderr)
	}

	for _, test := range []struct{ name, answer string }{
		{"no", "n\n"},
		{"empty", "\n"},
		{"unterminated yes", "yes\x04\x04"},
		{"end of input", "\x04"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.set(t, "app/A")
			// The values typed after the answer are there for a set that
			// would prompt anyway.
			outcome, _, stderr := h.runOn(terminal(t, test.answer+"one\ntwo\n"), "set", "app/A", "app/NEW")
			if outcome.Code != 130 || !strings.HasSuffix(stderr, "mk: error[cancelled]: no entries were written\n") || strings.Contains(stderr, "Value for") || h.list(t) != "app/A\n" || h.value("app/A") != "synthetic-app/A" {
				t.Fatalf("%+v %q %q", outcome, stderr, h.list(t))
			}
		})
	}

	h = newHarness(t)
	h.set(t, "app/A")
	if outcome, stdout, stderr := h.runOn(terminal(t, "v\n"), "set", "-y", "app/A"); outcome.Code != 0 || stdout != "replaced app/A\n" || stderr != "Value for app/A (input hidden): \n" || h.value("app/A") != "v" {
		t.Fatalf("--yes: %+v %q %q", outcome, stdout, stderr)
	}

	// A piped rotation never asks, not even on the controlling terminal.
	h = newHarness(t)
	h.set(t, "app/A")
	h.terminal = terminal(t, "n\n")
	if outcome, stdout, stderr := h.run(t, "v", "set", "--stdin", "app/A"); outcome.Code != 0 || stdout != "replaced app/A\n" || stderr != "" || h.terminalOpens != 0 || h.value("app/A") != "v" {
		t.Fatalf("--stdin: %+v %q %q %d", outcome, stdout, stderr, h.terminalOpens)
	}
}

// An entry created after the check was not confirmed, so set leaves it.
func TestSetEntryCreatedAfterCheck(t *testing.T) {
	h := newHarness(t)
	h.backend.Before = func(op, account string) error {
		if op == "add" && account == "app/NEW" {
			h.backend.Put(account, []byte("other"))
		}
		return nil
	}
	outcome, _, stderr := h.runOn(terminal(t, "v\n"), "set", "app/NEW")
	want := "Value for app/NEW (input hidden): \nmk: error[exists]: app/NEW: the entry already exists\nmk: completed: none\nmk: not attempted: none\nmk: hint: it was created after mk checked; run mk set again to replace it\n"
	if outcome.Code != 4 || stderr != want || h.value("app/NEW") != "other" {
		t.Fatalf("%+v %q %q", outcome, stderr, h.value("app/NEW"))
	}
}

// Without a terminal to prompt on, set needs --stdin, and delete and
// uninstall --yes.
func TestNonInteractiveNeedsFlags(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/TOKEN")
	// A controlling terminal does not make mk ask: a caller without one on
	// stdin may have no one at it.
	h.terminal = terminal(t, "y\n")
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
	if h.list(t) != "app/TOKEN\n" || h.value("app/TOKEN") != "synthetic-app/TOKEN" || h.terminalOpens != 0 {
		t.Fatalf("the entry changed, or the terminal was opened %d times", h.terminalOpens)
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
