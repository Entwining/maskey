package cli

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

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
	dir := t.TempDir()
	script := filepath.Join(dir, "script")
	os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600)
	garbage := filepath.Join(dir, "garbage")
	os.WriteFile(garbage, []byte{0xff, 0xfe, 0, 0}, 0o700)
	const notFound = "mk: hint: check the command, the first word after --; mk runs it without a shell, so an alias or shell function needs sh -c\n"
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
		{[]string{"run", "app", "--", "maskey-no-such-command"}, 127, "command_not_found]: the command was not found in PATH\n" + notFound},
		{[]string{"run", "app", "--", filepath.Join(dir, "missing")}, 127, "command_not_found]: there is no file at the command's path\n" + notFound},
		{[]string{"run", "app", "--", script}, 126, "command_not_executable]: the command is not executable\nmk: hint: check the file's permissions\n"},
		{[]string{"run", "app", "--", dir}, 126, "command_not_executable]: the command is a directory\nmk: hint: give the path of the executable inside it\n"},
		{[]string{"run", "app", "--", garbage}, 126, "command_not_executable]: the command is not in a format this Mac can run\nmk: hint: add a #! line to a script, or run it through its interpreter, such as sh <file>\n"},
	} {
		outcome, stdout, stderr := h.run(t, "", test.args...)
		if outcome.Code != test.code || stdout != "" || !strings.HasPrefix(stderr, "mk: error["+test.err) {
			t.Errorf("%q: %+v stdout %q stderr %s", test.args, outcome, stdout, stderr)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a command started despite a failed selection")
	}

	// A command found only through a relative PATH entry is not run.
	os.WriteFile(filepath.Join(dir, "maskey-relative"), []byte("#!/bin/sh\n"), 0o700)
	t.Chdir(dir)
	t.Setenv("PATH", ".")
	want := "mk: error[command_not_found]: the command was found only through a relative PATH entry\nmk: hint: give its path, such as ./<command>\n"
	if outcome, _, stderr := h.run(t, "", "run", "app", "--", "maskey-relative"); outcome.Code != 127 || stderr != want {
		t.Errorf("relative PATH entry: %+v %q", outcome, stderr)
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

func (b taggedReads) Read(ctx context.Context, accounts []string) (map[string]store.Stored, error) {
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

// An entry of kind ssh reaches the command as an SSH agent that signs with
// its key, and as its public key under its name, never as the key itself.
func TestRunLendsSSHKeys(t *testing.T) {
	h := newHarness(t)
	fixtures := keyFixtures(t)
	h.run(t, string(fixtures[0].plain), "set", "--stdin", "deploy/ED_SSH_KEY")
	h.run(t, string(fixtures[1].plain), "set", "--stdin", "deploy/RSA_SSH_KEY")
	h.set(t, "deploy/TOKEN")

	dir := t.TempDir()
	// ssh-keygen signs through the agent given a public key, as git does
	// for SSH commit signatures; both are checked against their public keys.
	script := `set -e
env > "$1/env"
ssh-add -L > "$1/listed"
for name in ED_SSH_KEY RSA_SSH_KEY; do
  eval 'printf "%s\n" "$'$name'"' > "$1/$name.pub"
  echo signed > "$1/$name"
  ssh-keygen -q -Y sign -n test -f "$1/$name.pub" "$1/$name"
  ssh-keygen -q -Y check-novalidate -n test -s "$1/$name.sig" < "$1/$name" > /dev/null
done
printf '%s' "$TOKEN"`
	outcome, stdout, stderr := h.run(t, "", "run", "deploy", "--", "/bin/sh", "-c", script, "sh", dir)
	if outcome.Code != 0 || stdout != "***" {
		t.Fatalf("run: %+v stdout %q stderr %q", outcome, stdout, stderr)
	}
	environment, _ := os.ReadFile(filepath.Join(dir, "env"))
	listed, _ := os.ReadFile(filepath.Join(dir, "listed"))
	for _, fixture := range fixtures {
		key, _ := ssh.ParseRawPrivateKey(fixture.plain)
		signer, _ := ssh.NewSignerFromKey(key)
		public := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(signer.PublicKey())), "\n")
		if !bytes.Contains(environment, []byte("_SSH_KEY="+public+"\n")) || !bytes.Contains(listed, []byte(public)) {
			t.Errorf("%s: environment %q, listed %q, want its public key in both", fixture.name, environment, listed)
		}
		if bytes.Contains(environment, bytes.TrimSpace(bytes.Split(fixture.plain, []byte("\n"))[1])) {
			t.Errorf("%s: the private key reached the environment", fixture.name)
		}
	}
	for _, comment := range []string{"deploy/ED_SSH_KEY", "deploy/RSA_SSH_KEY"} {
		if !bytes.Contains(listed, []byte(" "+comment+"\n")) {
			t.Errorf("listed %q, want the comment %s", listed, comment)
		}
	}
	socket, _, _ := strings.Cut(string(environment[bytes.Index(environment, []byte("SSH_AUTH_SOCK="))+len("SSH_AUTH_SOCK="):]), "\n")
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the agent's socket %s outlived the command: %v", socket, err)
	}
}

// A process the command leaves behind holding its output, which mk waits
// for, finds no agent once the command has exited.
func TestRunEndsSSHAgentWithCommand(t *testing.T) {
	h := newHarness(t)
	h.run(t, string(keyFixtures(t)[0].plain), "set", "--stdin", "deploy/DEPLOY_SSH_KEY")
	dir := t.TempDir()
	script := `(sleep 1; ssh-add -L > /dev/null 2>&1; echo $? > "$1/listed") &`
	if outcome, _, stderr := h.run(t, "", "run", "deploy", "--", "/bin/sh", "-c", script, "sh", dir); outcome.Code != 0 {
		t.Fatalf("run: %+v %q", outcome, stderr)
	}
	if status, err := os.ReadFile(filepath.Join(dir, "listed")); err != nil || string(status) == "0\n" {
		t.Fatalf("ssh-add -L after the command exited: status %q, %v", status, err)
	}
}

// An entry of kind ssh that holds no key the agent can use stops mk before
// the command starts.
func TestRunRejectsUnusableSSHKeys(t *testing.T) {
	h := newHarness(t)
	h.store.Create(context.Background(), store.Ref{Namespace: "deploy", Name: "PROTECTED"}, keyFixtures(t)[0].protected, store.Details{Kind: store.ValueSSH})
	h.store.Create(context.Background(), store.Ref{Namespace: "deploy", Name: "PKCS8"}, pkcs8, store.Details{Kind: store.ValueSSH})
	h.store.Create(context.Background(), store.Ref{Namespace: "deploy", Name: "PUBLIC"}, []byte("ssh-ed25519 AAAA synthetic"), store.Details{Kind: store.ValueSSH})
	h.store.Create(context.Background(), store.Ref{Namespace: "deploy", Name: "P224"}, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: p224(t)}), store.Details{Kind: store.ValueSSH})
	h.store.Create(context.Background(), store.Ref{Namespace: "deploy", Name: "SECURITY"}, securityKey(), store.Details{Kind: store.ValueSSH})
	marker := filepath.Join(t.TempDir(), "started")
	for _, test := range []struct{ ref, want string }{
		{"deploy/PROTECTED", "mk: error[invalid]: deploy/PROTECTED: the SSH key is protected by a passphrase, which mk run does not ask for\nmk: hint: mk set --stdin deploy/PROTECTED < <file> in a terminal stores the key without it\n"},
		{"deploy/PKCS8", "mk: error[invalid]: deploy/PKCS8: the private key is protected by a passphrase that mk cannot remove\nmk: hint: openssl pkey -in <file> | mk set --stdin deploy/PKCS8 stores the key without it, with OpenSSL asking for the passphrase on the terminal\n"},
		{"deploy/PUBLIC", "mk: error[invalid]: deploy/PUBLIC: the value of an entry of kind ssh is not an SSH private key\nmk: hint: replace it with mk set --stdin deploy/PUBLIC < <file>, or choose another kind in Maskey\n"},
		{"deploy/P224", unlendable("deploy/P224")},
		{"deploy/SECURITY", unlendable("deploy/SECURITY")},
	} {
		if outcome, _, stderr := h.run(t, "", "run", test.ref, "--", "/usr/bin/touch", marker); outcome.Code != 125 || stderr != test.want {
			t.Errorf("%s: %+v %q", test.ref, outcome, stderr)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a command started with an unusable SSH key")
	}
}
