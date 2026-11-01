package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/store"
)

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Everything Maskey and the window write under the home directory, written
// by the code that writes it where a test can run that code, is gone after
// uninstall, except what is not Maskey's; a second run finds nothing left.
func TestUninstall(t *testing.T) {
	h := newHarness(t)
	t.Setenv("HOME", h.home)
	dir, err := activity.Dir(store.Service)
	if err != nil {
		t.Fatal(err)
	}
	h.activity = activity.New(dir, nil)
	t.Cleanup(func() { h.activity.Remove() })
	h.activity.Record(context.Background(), activity.Record{Op: "get", Entry: "app/TOKEN", Result: "ok"})
	if outcome, _, stderr := h.run(t, "", "skill", "install"); outcome.Code != 0 {
		t.Fatalf("skill install: %+v %s", outcome, stderr)
	}
	if _, err := InstallCLI(h.home, h.executable); err != nil {
		t.Fatal(err)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	// MyGo keeps the window's state in os.UserConfigDir()/<app name>.
	writeFile(t, filepath.Join(config, "Maskey", "window-state.json"))
	// The user's own: a program beside mk, and a skill in place of the link.
	other := filepath.Join(h.home, ".local", "bin", "other")
	writeFile(t, other)
	claude := filepath.Join(h.home, ".claude", "skills", "maskey")
	os.Remove(claude)
	writeFile(t, filepath.Join(claude, "SKILL.md"))

	outcome, stdout, stderr := h.run(t, "", "uninstall", "--yes")
	want := strings.ReplaceAll(`absent   Keychain service
removed  ~/Library/Logs/io.github.entwining.maskey
removed  ~/Library/Application Support/Maskey
kept     ~/.claude/skills/maskey
         not a link
removed  ~/.agents/skills/maskey
removed  ~/.local/bin/mk
Maskey.app is still at `+filepath.Dir(filepath.Dir(filepath.Dir(h.executable)))+`: move it to the Trash, or run brew uninstall --cask maskey if Homebrew installed it.
`, "~", h.home)
	if outcome.Code != 0 || stdout != want || stderr != "" {
		t.Fatalf("uninstall: %+v\n%s\nwant\n%s%s", outcome, stdout, want, stderr)
	}
	var left []string
	filepath.WalkDir(h.home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !entry.IsDir() {
			left = append(left, path)
		}
		return nil
	})
	if !slices.Equal(left, []string{filepath.Join(claude, "SKILL.md"), other}) {
		t.Fatalf("left in the home directory: %v", left)
	}

	// Run again on a terminal: nothing is left to ask about.
	outcome, stdout, stderr = h.runOn(terminal(t, ""), "uninstall", "--json")
	statuses := ""
	for _, item := range decode(t, stdout)["items"].([]any) {
		statuses += item.(map[string]any)["status"].(string) + " "
	}
	if outcome.Code != 0 || stderr != "" || statuses != "absent absent absent kept absent absent " {
		t.Fatalf("second uninstall: %+v %s %s", outcome, stdout, stderr)
	}
}

// A failed row of the Keychain service fails uninstall after the rest goes,
// with a hint that names the executable, since the mk link is gone.
func TestUninstallServiceFailure(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", "skill", "install")
	var stdout, stderr bytes.Buffer
	env := h.env(nil, &stdout, &stderr)
	env.RemoveService = func(context.Context, string) []agent.ServiceItem {
		return []agent.ServiceItem{
			{Item: "Keychain service", Status: "failed", Detail: "synthetic launchctl failure"},
			{Item: "service.plist", Status: "kept", Detail: "the Keychain service may still be loaded"},
		}
	}
	outcome := Run(context.Background(), []string{"uninstall", "--yes"}, env)
	rows := "failed   Keychain service\n         synthetic launchctl failure\nkept     service.plist\n         the Keychain service may still be loaded\n"
	hint := "mk: error[prerequisite]: 1 item was not removed\nmk: hint: resolve the failed items, then run " + h.executable + " uninstall again\n"
	if outcome.Code != 6 || !strings.HasPrefix(stdout.String(), rows) || stderr.String() != hint {
		t.Fatalf("%+v\n%s%s", outcome, &stdout, &stderr)
	}
	if _, err := os.Lstat(filepath.Join(h.home, ".claude", "skills", "maskey")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the skill link stayed")
	}
}

// A link uninstall cannot read is a failure, not a link to keep.
func TestUninstallUnreadableLink(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", "skill", "install")
	skills := filepath.Join(h.home, ".agents", "skills")
	os.Chmod(skills, 0)
	t.Cleanup(func() { os.Chmod(skills, 0o755) })
	outcome, stdout, _ := h.run(t, "", "uninstall", "--yes")
	if outcome.Code != 6 || !strings.Contains(stdout, "failed   "+filepath.Join(skills, "maskey")+"\n") {
		t.Fatalf("%+v\n%s", outcome, stdout)
	}
}

// The question lists only what uninstall would remove, and declining
// removes nothing.
func TestUninstallConfirmation(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", "skill", "install")
	var stdout, stderr bytes.Buffer
	env := h.env(terminal(t, "no\n"), &stdout, &stderr)
	env.ServiceItems = func(context.Context, string) []agent.ServiceItem {
		return []agent.ServiceItem{{Item: "Keychain service"}, {Item: "foreign.plist", Status: "kept", Detail: "not this Maskey's"}}
	}
	outcome := Run(context.Background(), []string{"uninstall"}, env)
	link := filepath.Join(h.home, ".claude", "skills", "maskey")
	prompt := "Remove what Maskey installed on this Mac:\n  Keychain service\n  " + link + "\n  " + filepath.Join(h.home, ".agents", "skills", "maskey") + "\nEntries stay in iCloud Keychain, and Maskey.app stays where it is. Continue? [y/N] "
	if _, err := os.Lstat(link); outcome.Code != 130 || !strings.HasPrefix(stderr.String(), prompt) || err != nil {
		t.Fatalf("%+v %q, link %v", outcome, &stderr, err)
	}
}

// writeFunc is an io.Writer that calls itself.
type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) { return f(p) }

// What uninstall removes is decided once the owner confirms: a link that
// comes to point at another copy while the question waits stays, and the
// Keychain service is removed only after the answer.
func TestUninstallDecidesAfterConfirmation(t *testing.T) {
	h := newHarness(t)
	h.run(t, "", "skill", "install")
	link := filepath.Join(h.home, ".agents", "skills", "maskey")
	other := t.TempDir()
	var steps []string
	var stdout bytes.Buffer
	stderr := writeFunc(func(p []byte) (int, error) {
		if bytes.HasPrefix(p, []byte("Remove what Maskey installed")) {
			steps = append(steps, "asked")
			os.Remove(link)
			os.Symlink(other, link)
		}
		return len(p), nil
	})
	env := h.env(terminal(t, "y\n"), &stdout, stderr)
	env.ServiceItems = func(ctx context.Context, home string) []agent.ServiceItem {
		steps = append(steps, "listed")
		return noService(ctx, home)
	}
	env.RemoveService = func(ctx context.Context, home string) []agent.ServiceItem {
		steps = append(steps, "removed")
		return noService(ctx, home)
	}
	outcome := Run(context.Background(), []string{"uninstall"}, env)
	target, err := os.Readlink(link)
	if outcome.Code != 0 || !strings.Contains(stdout.String(), "kept     "+link+"\n         links elsewhere\n") || target != other || err != nil {
		t.Fatalf("%+v\n%s, link %q %v", outcome, &stdout, target, err)
	}
	if !slices.Equal(steps, []string{"listed", "asked", "removed"}) {
		t.Fatalf("steps %v", steps)
	}
}

// Without a home directory, uninstall cannot find what to remove, and fails
// before it does anything.
func TestUninstallWithoutHome(t *testing.T) {
	h := newHarness(t)
	var stdout, stderr bytes.Buffer
	env := h.env(nil, &stdout, &stderr)
	env.Home = ""
	env.RemoveService = func(context.Context, string) []agent.ServiceItem {
		t.Fatal("the Keychain service was removed")
		return nil
	}
	outcome := Run(context.Background(), []string{"uninstall", "--yes"}, env)
	if outcome.Code != 6 || stdout.Len() != 0 || errorKind(stderr.String()) != "prerequisite" {
		t.Fatalf("%+v %s%s", outcome, &stdout, &stderr)
	}
}

// homePathFiles are the files that derive paths in the home directory, each
// with the path it derives. A file that starts writing there belongs here
// only once uninstall removes what it writes and TestUninstall shows it.
var homePathFiles = map[string]string{
	"main.go":                            "Env.Home",
	"gui.go":                             "the window's layout, in MyGo's directory for the window",
	"gui_actions.go":                     "the home InstallCLI links mk in",
	"internal/activity/activity.go":      "the activity record",
	"internal/agent/agent.go":            "the service's socket",
	"internal/agent/service_darwin.go":   "the service's configuration",
	"internal/agent/uninstall_darwin.go": "the service's socket directory",
	"internal/cli/setup.go":              "the skill and mk links",
	"internal/cli/uninstall.go":          "MyGo's directory for the window",
}

func TestHomePathFiles(t *testing.T) {
	root := os.DirFS(filepath.Join("..", ".."))
	found := map[string]string{}
	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.IsDir() && path != "." && (strings.HasPrefix(entry.Name(), ".") || entry.Name() == "build"):
			return fs.SkipDir
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		for _, marker := range []string{"os.UserHomeDir(", "os.UserConfigDir(", "os.UserCacheDir(", "env.Home", `"Library"`, `".local"`, `".claude"`, `".agents"`, "App.Path("} {
			if bytes.Contains(data, []byte(marker)) {
				found[path] = homePathFiles[path]
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, writes := range found {
		if writes == "" {
			t.Errorf("%s derives a path in the home directory: make uninstall remove what it writes, then list it in homePathFiles", path)
		}
	}
	for path := range homePathFiles {
		if _, ok := found[path]; !ok {
			t.Errorf("%s no longer derives a home path; remove it from homePathFiles", path)
		}
	}
}
