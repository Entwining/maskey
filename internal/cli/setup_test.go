package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/TOKEN")
	h.run(t, "", "skill", "install")
	outcome, stdout, _ := h.run(t, "", "doctor")
	want := `ok       interpreter  /usr/bin/python3
ok       keychain     1 entries in 1 namespaces
ok       service      running this build
ok       skill        linked in ~/.claude/skills and ~/.agents/skills
`
	if outcome.Code != 0 || stdout != want {
		t.Fatalf("doctor: %+v\n%s", outcome, stdout)
	}

	// A missing service is a warning; the first failed check decides the
	// status, and failures stay in the report on stdout.
	h.service = &store.Error{Kind: store.KindUnavailable, Message: "the Keychain service is not reachable"}
	h.check = &store.Error{Kind: store.KindPrerequisite, Message: "no interpreter"}
	h.backend.Before = func(string, string) error { return &store.Error{Kind: store.KindLocked, Message: "locked"} }
	outcome, stdout, stderr := h.run(t, "", "doctor")
	if outcome.Code != 6 || stderr != "" || !strings.HasPrefix(stdout, "error    interpreter  no interpreter\nerror    keychain     locked\n                      hint: unlock this Mac, then retry\n") {
		t.Fatalf("doctor: %+v %q %q", outcome, stdout, stderr)
	}
	outcome, stdout, _ = h.run(t, "", "doctor", "--json")
	document := decode(t, stdout)
	checks, _ := document["checks"].([]any)
	statuses := ""
	for _, c := range checks {
		statuses += c.(map[string]any)["status"].(string) + " "
	}
	if outcome.Code != 6 || document["version"] != "1.2.3" || statuses != "error error warning ok " {
		t.Fatalf("doctor: %+v %s", outcome, stdout)
	}
	// No row sends the user back to mk doctor.
	for _, kind := range []store.Kind{store.KindPrerequisite, store.KindKeychain, store.KindUnavailable} {
		h.check = &store.Error{Kind: kind, Message: "failed"}
		h.backend.Before = func(string, string) error { return h.check }
		if _, stdout, _ := h.run(t, "", "doctor"); strings.Contains(stdout, "mk doctor") {
			t.Errorf("doctor after a failure of kind %s:\n%s", kind, stdout)
		}
	}
}

func TestSetup(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
	}{
		{"success", nil, 0},
		// The installer's own errors are plain; they become prerequisite.
		{"failure", errors.New("synthetic setup failure"), 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			var stdout, stderr bytes.Buffer
			calls := 0
			env := h.env(nil, &stdout, &stderr)
			env.Setup = func(context.Context) error { calls++; return test.err }
			outcome := Run(context.Background(), []string{"setup"}, env)
			if calls != 1 || outcome.Code != test.code {
				t.Fatalf("setup calls=%d, %+v, stdout %q, stderr %q", calls, outcome, &stdout, &stderr)
			}
			if test.err == nil && stdout.String() != "installed the Keychain service\n" || test.err != nil && errorKind(stderr.String()) != "prerequisite" {
				t.Fatalf("stdout %q stderr %q", &stdout, &stderr)
			}
		})
	}
}

func TestSkill(t *testing.T) {
	h := newHarness(t)
	if _, stdout, _ := h.run(t, "", "skill", "show"); stdout != "synthetic skill\n" {
		t.Fatalf("show: %q", stdout)
	}
	claude := filepath.Join(h.home, ".claude", "skills", "maskey")
	agents := filepath.Join(h.home, ".agents", "skills", "maskey")
	// A link left by another copy of the app is replaced.
	os.MkdirAll(filepath.Dir(agents), 0o755)
	os.Symlink(t.TempDir(), agents)
	for range 2 {
		if outcome, stdout, stderr := h.run(t, "", "skill", "install"); outcome.Code != 0 || stdout != "linked "+claude+"\nlinked "+agents+"\n" {
			t.Fatalf("install: %+v %q %q", outcome, stdout, stderr)
		}
	}
	for _, link := range []string{claude, agents} {
		if target, err := os.Readlink(link); err != nil || target != h.source {
			t.Fatalf("%s links to %q, %v", link, target, err)
		}
	}
	// A directory of the user's own is left alone.
	os.Remove(claude)
	os.Mkdir(claude, 0o755)
	if outcome, _, stderr := h.run(t, "", "skill", "install"); outcome.Code != 4 || errorKind(stderr) != "exists" {
		t.Fatalf("install over a directory: %+v %q", outcome, stderr)
	}
	h.executable = filepath.Join(t.TempDir(), "Maskey")
	if outcome, _, stderr := h.run(t, "", "skill", "install"); outcome.Code != 6 || errorKind(stderr) != "prerequisite" {
		t.Fatalf("install outside the app: %+v %q", outcome, stderr)
	}
}

func TestInstallCLI(t *testing.T) {
	home := t.TempDir()
	app := filepath.Join(t.TempDir(), "Maskey")
	os.WriteFile(app, nil, 0o755)
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(app, alias)

	link, err := InstallCLI(home, alias)
	if err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != mustEval(t, app) {
		t.Fatalf("mk links to %q, want the resolved executable %q", target, app)
	}

	// An earlier link, as to a moved app, is replaced.
	os.Remove(link)
	os.Symlink("/old/Maskey", link)
	if _, err := InstallCLI(home, app); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != mustEval(t, app) {
		t.Fatalf("mk still links to %q", target)
	}

	// A file of the user's is never replaced.
	os.Remove(link)
	os.WriteFile(link, []byte("user file"), 0o755)
	if _, err := InstallCLI(home, app); err == nil {
		t.Fatal("InstallCLI replaced a regular file")
	}
	if b, _ := os.ReadFile(link); string(b) != "user file" {
		t.Fatal("the user's file changed")
	}

	// Nor is one at the name of the temporary link.
	os.Remove(link)
	temporary := fmt.Sprintf("%s.maskey-new-%d", link, os.Getpid())
	os.WriteFile(temporary, []byte("user file"), 0o755)
	if _, err := InstallCLI(home, app); err == nil {
		t.Fatal("InstallCLI replaced a file at its temporary name")
	}
	if b, _ := os.ReadFile(temporary); string(b) != "user file" {
		t.Fatal("the file at the temporary name changed")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
