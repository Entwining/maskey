package cli

import (
	"context"
	"errors"
	"maps"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

// A missing operand suggests the one existing name that differs from it
// only in case, or else the one within two edits of it, and nothing when
// there is none or more than one.
func TestSimilarNames(t *testing.T) {
	h := newHarness(t)
	h.set(t, "github/GITHUB_TOKEN", "github/GITLAB_TOKEN", "npm/NPM_TOKEN", "Work/NPM_TOKEN", "work/NPM_TOKEN")
	for _, test := range []struct {
		args []string
		hint string
	}{
		{[]string{"get", "github/GITHUB_TOKN"}, "did you mean github/GITHUB_TOKEN?"},
		{[]string{"get", "github/GITXXB_TOKEN"}, "run mk list to see the entries"},
		{[]string{"list", "gihub"}, "did you mean github?"},
		{[]string{"run", "npm", "gthb", "--", "true"}, "did you mean github?"},
		{[]string{"get", "npm/OTHER_TOKEN"}, "run mk list to see the entries"},
		{[]string{"list", "GITHUB"}, "did you mean github?"},
		{[]string{"get", "GitHub/github_token"}, "did you mean github/GITHUB_TOKEN?"},
		{[]string{"list", "WORK"}, "run mk list to see the entries"},
	} {
		if _, _, stderr := h.run(t, "", test.args...); !strings.HasSuffix(stderr, "mk: hint: "+test.hint+"\n") {
			t.Errorf("%q: %q", test.args, stderr)
		}
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
		{[]string{"print"}, "usage", "did you mean mk get?"},
		{[]string{"bogus"}, "usage", "run mk --help for the commands"},
		{[]string{"--bogus"}, "usage", "run mk --help for its flags"},
		{[]string{"set", "--bogus", "app/TOKEN"}, "usage", "run mk set --help for its flags"},
		{[]string{"skill", "bogus"}, "usage", "run mk skill --help for the commands"},
		{[]string{"completion", "bogus"}, "usage", "run mk completion --help for the commands"},
		{[]string{"completion", "zsh", "extra"}, "usage", "usage: mk completion zsh [--no-descriptions]"},
		{[]string{"help", "bogus"}, "usage", "usage: mk help [<command> [<subcommand>]] or mk help exit-codes"},
		{[]string{"exit-codes", "extra"}, "usage", "usage: mk help exit-codes"},
		{[]string{"list", "a", "b"}, "usage", "usage: mk list [--json] [<namespace>]"},
		{[]string{"set"}, "usage", "usage: mk set [--json] <namespace>/<NAME>... or mk set --stdin [--json] <namespace>/<NAME>"},
		{[]string{"set", "--stdin", "app/A", "app/B"}, "usage", "usage: mk set --stdin [--json] <namespace>/<NAME>"},
		{[]string{"set", "--stdin", "app/A", "app/B", "app/A"}, "usage", "pass each entry once"},
		{[]string{"set", "--stdin", "app", "TOKEN"}, "usage", "enter the value at the hidden prompt or pipe one value to mk set --stdin <namespace>/<NAME>"},
		{[]string{"set", "--stdin", ".app/X"}, "invalid", ""},
		{[]string{"get"}, "usage", "usage: mk get [--json] <namespace>/<NAME>"},
		{[]string{"delete", "--yes"}, "usage", "usage: mk delete [--yes] [--json] <namespace>/<NAME>..."},
		{[]string{"rename", "app/A"}, "usage", "usage: mk rename [--json] <namespace>/<NAME> <new-namespace>/<NEW_NAME>"},
		{[]string{"doctor", "extra"}, "usage", "usage: mk doctor [--json]"},
	} {
		outcome, stdout, stderr := h.run(t, "synthetic", test.args...)
		if outcome.Code != 2 || stdout != "" || errorKind(stderr) != test.kind || test.hint != "" && !strings.HasSuffix(stderr, "mk: hint: "+test.hint+"\n") {
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
	h.set(t, "github/TOKEN")
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
		{"get", "github/" + value},
		{"delete", "--yes", "github/TOKEN", "github/" + value},
		{"rename", "github/" + value, "github/OTHER"},
		{"run", "github", value},
		{"run", "github/" + value, "--", "true"},
		{"run", "github/TOKEN=" + value, "--", "true"},
		{"run", "github", "-n" + value, "--", "true"},
		{"run", "github", "--" + value, "--", "true"},
		{"list", value + "/"},
		{"list", "a" + value},
		{"help", value},
		{"skill", value},
		{"completion", "zsh", value},
	} {
		for _, format := range [][]string{nil, {"--json"}} {
			_, stdout, stderr := h.run(t, "", append(format, args...)...)
			if strings.Contains(stdout+stderr, value) {
				t.Errorf("%q printed the argument: %q %q", args, stdout, stderr)
			}
		}
	}
}

// mk help exit-codes lists every kind with its exit status, and errors of
// each kind exit with that status.
func TestErrorKindsAndExitCodes(t *testing.T) {
	h := newHarness(t)
	_, topic, _ := h.run(t, "", "help", "exit-codes")
	documented := map[store.Kind]int{}
	// The table's rows, then the statuses of mk run's own failures.
	for _, pattern := range []string{`(?m)^  (\d+) +([a-z_]+) `, `(\d+)\s+for\s+(?:the kinds above or\s+)?([a-z_]+)`} {
		for _, match := range regexp.MustCompile(pattern).FindAllStringSubmatch(topic, -1) {
			documented[store.Kind(match[2])], _ = strconv.Atoi(match[1])
		}
	}
	delete(documented, "success")
	if !maps.Equal(documented, exitCodes) {
		t.Errorf("mk help exit-codes lists %v, want %v", documented, exitCodes)
	}
	for kind, code := range documented {
		h.backend.Before = func(string, string) error { return &store.Error{Kind: kind, Message: "injected"} }
		outcome, stdout, stderr := h.run(t, "", "list", "--json")
		e, _ := decode(t, stderr)["error"].(map[string]any)
		if outcome.Code != code || e["kind"] != string(kind) || e["exit_code"] != float64(code) || stdout != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %s; want exit %d", kind, outcome.Code, stdout, stderr, code)
		}
	}
	// An error mk did not classify is its own fault, not the caller's.
	h.backend.Before = func(string, string) error { return errors.New("synthetic untyped failure") }
	if outcome, _, stderr := h.run(t, "", "list"); outcome.Code != 1 || stderr != "mk: error[internal_error]: an internal error prevented completion\n" {
		t.Errorf("untyped: %+v %q", outcome, stderr)
	}
}
