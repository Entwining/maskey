package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const rootHelp = `Usage:
  mk [command] [flags]

Maskey stores credentials in iCloud Keychain. Each entry is <namespace>/<NAME>.
With mk run, github/GITHUB_TOKEN sets GITHUB_TOKEN in the child's environment.

Core commands:
  run         Run a command with selected credentials and mask its output
  list        List entry references
  set         Create entries or replace their values
  get         Write a granted entry's raw value to non-terminal stdout
  rename      Rename an entry or move it to another namespace
  delete      Delete entries from this Mac and Macs syncing its Keychain
  grant       Let any program obtain entries' values with mk get
  revoke      Take program access away from entries
  activity    Show which programs used entries on this Mac

Setup commands:
  doctor      Check Keychain access, interpreter, service, and agent skill
  setup       Install the Keychain service for sandboxes and program access
  skill       Show or install the agent skill
  uninstall   Remove what Maskey installed on this Mac outside its app

Additional commands:
  completion  Generate shell completion
  help        Show help for a command or topic

Examples:
  mk set github/GITHUB_TOKEN
  gh auth token | mk set --stdin github/GITHUB_TOKEN
  mk run github -- gh auth status

Give values to mk set at its hidden prompt or with --stdin. Keep secret
values out of command-line arguments.

Flags:
  -h, --help     Show help
      --version  Show the installed version

Use mk <command> --help for flags and examples.
Commands that support --json list it in their help; piping does not enable it.
See mk help exit-codes for error kinds and exit statuses.
See mk skill show for the agent workflow.
`

func TestHelp(t *testing.T) {
	h := newHarness(t)
	// Cobra's help command is listed, and the exit-codes topic is not.
	_, root, _ := h.run(t, "", "--help")
	if root != rootHelp {
		t.Errorf("root help:\n%s", root)
	}
	if _, bare, _ := h.run(t, ""); bare != root {
		t.Errorf("bare mk printed %q", bare)
	}
	_, set, _ := h.run(t, "", "set", "--help")
	if _, viaHelp, _ := h.run(t, "", "help", "set"); viaHelp != set ||
		!strings.HasPrefix(set, "Usage:\n  mk set [--json] <namespace>/<NAME>...\n  mk set --stdin [--json] <namespace>/<NAME>\n\nCreate entries") ||
		!strings.HasSuffix(set, "\n\nFlags:\n  -h, --help   Show help\n      --stdin  Read one value from standard input\n      --json   Write results or errors as JSON\n") {
		t.Errorf("set help %q, help set %q", set, viaHelp)
	}
	if _, skill, _ := h.run(t, "", "help", "skill"); !strings.Contains(skill, "\n\nCommands:\n  show     Print the skill\n  install  Link ") {
		t.Errorf("skill help %q", skill)
	}
	if _, completion, _ := h.run(t, "", "completion", "--help"); !strings.Contains(completion, "\n\nShells:\n  bash        Bash completion\n") ||
		!strings.HasSuffix(completion, "\n\nFlags:\n  -h, --help  Show help\n\nUse mk completion <shell> --help for loading instructions.\n") {
		t.Errorf("completion help %q", completion)
	}
	if _, get, _ := h.run(t, "", "get", "--help"); !strings.Contains(get, "\n\nHelper command to configure:\n  mk get anthropic/ANTHROPIC_API_KEY\n\nFlags:\n") {
		t.Errorf("get help %q", get)
	}
	// The topic is the same under both names, and lists no flags.
	_, topic, _ := h.run(t, "", "exit-codes")
	if outcome, viaHelp, _ := h.run(t, "", "help", "exit-codes"); outcome.Code != 0 || viaHelp != topic ||
		!strings.HasPrefix(topic, "Usage:\n  mk help exit-codes\n\n") || strings.Contains(topic, "Flags:") {
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

func TestCompletion(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/A", "app/B")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"__complete", "run", ""}, "app\napp/A\napp/B\n:4\n"},
		{[]string{"__complete", "run", "--json", "app", ""}, "app/A\napp/B\n:4\n"},
		// The command after -- is the shell's to complete.
		{[]string{"__complete", "run", "app", "--", ""}, ":0\n"},
		{[]string{"__complete", "run", "app", "--", "ls", ""}, ":0\n"},
		{[]string{"__complete", "delete", "app/A", ""}, "app/B\n:4\n"},
		{[]string{"__complete", "set", "app/B", ""}, "app/A\n:4\n"},
		{[]string{"__complete", "get", "app/A", ""}, ":4\n"},
		{[]string{"__complete", "list", ""}, "app\n:4\n"},
	} {
		if _, stdout, _ := h.run(t, "", test.args...); !strings.HasPrefix(stdout, test.want) {
			t.Errorf("%q: %q", test.args, stdout)
		}
	}
	if _, stdout, _ := h.run(t, "", "completion", "zsh"); !strings.HasPrefix(stdout, "#compdef mk\n") {
		t.Errorf("the zsh script begins %.40q", stdout)
	}
}

// helpOnTerminal is mk --help written to a terminal, as the terminal shows
// it, with its line endings turned back into \n. Closing the terminal can
// drop output not read yet, so it reads up to the page's last line.
func helpOnTerminal(t *testing.T, h *harness) string {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer tty.Close()
	chunks := make(chan []byte, 64)
	go func() {
		for {
			chunk := make([]byte, 4096)
			n, err := master.Read(chunk)
			chunks <- chunk[:n]
			if err != nil {
				return
			}
		}
	}()
	go Run(context.Background(), []string{"--help"}, h.env(nil, tty, io.Discard))
	var shown []byte
	for !bytes.HasSuffix(shown, []byte("See mk skill show for the agent workflow.\r\n")) {
		select {
		case chunk := <-chunks:
			shown = append(shown, chunk...)
		case <-time.After(5 * time.Second):
			t.Fatalf("the terminal showed %q", shown)
		}
	}
	return strings.ReplaceAll(string(shown), "\r\n", "\n")
}

// Help on a terminal is bold in its headings, command names and flag
// spellings, and otherwise the plain page; elsewhere it is plain text.
func TestHelpStyling(t *testing.T) {
	h := newHarness(t)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	Run(context.Background(), []string{"--help"}, h.env(nil, w, io.Discard))
	w.Close()
	if piped, _ := io.ReadAll(r); string(piped) != rootHelp {
		t.Errorf("piped help %q", piped)
	}

	styled := helpOnTerminal(t, h)
	for _, field := range []string{"Usage:", "Core commands:", "Additional commands:", "Flags:", "run", "completion", "-h, --help", "--version"} {
		if !strings.Contains(styled, "\x1b[1m"+field+"\x1b[0m") {
			t.Errorf("%q is not bold:\n%q", field, styled)
		}
	}
	// 6 headings, 15 commands and 2 flags; nothing else is styled.
	if n := strings.Count(styled, "\x1b[1m"); n != 23 {
		t.Errorf("%d bold spans, want 23", n)
	}
	if plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(styled, ""); plain != rootHelp {
		t.Errorf("styled help without its styling:\n%s", plain)
	}

	for name, value := range map[string]string{"NO_COLOR": "0", "TERM": "dumb"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if shown := helpOnTerminal(t, h); shown != rootHelp {
				t.Errorf("help with %s=%s:\n%q", name, value, shown)
			}
		})
	}
}
