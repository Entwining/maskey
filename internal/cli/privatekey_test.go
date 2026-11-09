package cli

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/Entwining/maskey/internal/store"
)

const passphrase = "synthetic passphrase"

// keyFixture is a private key as a passphrase protects it and as it is
// without one.
type keyFixture struct {
	name             string
	protected, plain []byte
}

var (
	// pkcs8 is an ENCRYPTED PRIVATE KEY block, which set knows by its type.
	pkcs8       = pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("synthetic")})
	certificate = "-----BEGIN CERTIFICATE-----\nc3ludGhldGlj\n-----END CERTIFICATE-----\n"
)

func keyFixtures(t *testing.T) []keyFixture {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	openssh, err := ssh.MarshalPrivateKeyWithPassphrase(ed, "comment", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	opensshPlain, err := ssh.MarshalPrivateKey(ed, "")
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(rsaKey)
	return []keyFixture{
		{"openssh", pem.EncodeToMemory(openssh), pem.EncodeToMemory(opensshPlain)},
		{"legacy", legacyEncrypted(t, der), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})},
	}
}

func legacyEncrypted(t *testing.T, der []byte) []byte {
	t.Helper()
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", der, []byte(passphrase), x509.PEMCipherAES256) //nolint:staticcheck // the legacy format set reads has no other writer
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func TestProtectionOf(t *testing.T) {
	for _, f := range keyFixtures(t) {
		if got := protectionOf(f.protected); got != removable {
			t.Errorf("%s protected: %d", f.name, got)
		}
		if got := protectionOf(f.plain); got != unprotected {
			t.Errorf("%s plain: %d", f.name, got)
		}
	}
	if got := protectionOf([]byte(certificate + string(pkcs8))); got != unremovable {
		t.Errorf("pkcs8: %d", got)
	}
	for _, value := range []string{"synthetic-token", "line 1\nline 2\n", certificate} {
		if got := protectionOf([]byte(value)); got != unprotected {
			t.Errorf("%q: %d", value, got)
		}
	}
}

// The key comes back in its own container, without the passphrase, and a
// wrong passphrase is the error set asks again on.
func TestWithoutPassphrase(t *testing.T) {
	for _, f := range keyFixtures(t) {
		plain, err := withoutPassphrase(f.protected, []byte(passphrase))
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if got, want := blockType(plain), blockType(f.protected); got != want {
			t.Errorf("%s: %s became %s", f.name, want, got)
		}
		if publicKey(t, plain) != publicKey(t, f.plain) {
			t.Errorf("%s: another key", f.name)
		}
		if _, err := withoutPassphrase(f.protected, []byte("wrong")); !errors.Is(err, x509.IncorrectPasswordError) {
			t.Errorf("%s wrong passphrase: %v", f.name, err)
		}
	}
	// What a wrong passphrase that passes the padding check decrypts.
	if _, err := withoutPassphrase(legacyEncrypted(t, []byte("synthetic")), []byte(passphrase)); !errors.Is(err, x509.IncorrectPasswordError) {
		t.Errorf("not DER: %v", err)
	}
}

// A key bundled with other text, such as its certificate, loses only its
// passphrase, byte for byte.
func TestWithoutPassphraseKeepsBundle(t *testing.T) {
	f := keyFixtures(t)[1]
	before, after := "Note: -----BEGIN RSA PRIVATE KEY----- is the format\n"+certificate, "trailer\n"
	plain, err := withoutPassphrase([]byte(before+string(f.protected)+after), []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	if want := before + string(f.plain) + after; string(plain) != want {
		t.Errorf("got %q, want %q", plain, want)
	}
}

func blockType(key []byte) string {
	block, _ := pem.Decode(key)
	return block.Type
}

func publicKey(t *testing.T, key []byte) string {
	t.Helper()
	parsed, err := ssh.ParseRawPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(parsed.(crypto.Signer).Public())
	if err != nil {
		t.Fatal(err)
	}
	return string(public.Marshal())
}

// set offers on the terminal to store a protected key without its
// passphrase, keeps it on an empty answer, and, where it cannot ask or
// cannot decrypt, keeps it and warns.
func TestSetOffersToRemovePassphrase(t *testing.T) {
	openssh := keyFixtures(t)[0]
	h := newHarness(t)
	master, tty := openTerminal(t, "wrong\n"+passphrase+"\n")
	h.terminal = tty
	outcome, stdout, stderr := h.run(t, string(openssh.protected), "set", "--stdin", "app/KEY")
	if outcome.Code != 0 || stdout != "created app/KEY\n" || stderr != "" || publicKey(t, []byte(h.value("app/KEY"))) != publicKey(t, openssh.plain) {
		t.Fatalf("removed: %+v %q %q", outcome, stdout, stderr)
	}
	shown := make([]byte, 4096)
	n, err := master.Read(shown)
	if err != nil {
		t.Fatal(err)
	}
	shown = shown[:n]
	for _, text := range []string{"app/KEY is protected by a passphrase. Enter the passphrase to store the key without it, or press Return to store the key as it is.", "Passphrase (input hidden): ", "Bad passphrase, try again (input hidden): "} {
		if !strings.Contains(string(shown), text) {
			t.Errorf("terminal %q lacks %q", shown, text)
		}
	}

	h = newHarness(t)
	h.terminal = terminal(t, "\n")
	if outcome, _, stderr := h.run(t, string(openssh.protected), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != "" || h.value("app/KEY") != string(openssh.protected) {
		t.Fatalf("kept: %+v %q", outcome, stderr)
	}

	h = newHarness(t)
	warning := "mk: warning: the private key is protected by a passphrase\nmk: hint: mk set --stdin app/KEY < <file> in a terminal offers to store the key without it; keep it when the program reads the passphrase from another entry\n"
	if outcome, _, stderr := h.run(t, string(openssh.protected), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != warning || h.value("app/KEY") != string(openssh.protected) {
		t.Fatalf("no terminal: %+v %q", outcome, stderr)
	}

	h = newHarness(t)
	h.terminal = terminal(t, passphrase+"\n")
	outcome, _, stderr = h.run(t, string(pkcs8), "set", "--stdin", "app/KEY")
	if outcome.Code != 0 || h.terminalOpens != 0 || h.value("app/KEY") != string(pkcs8) || !strings.HasPrefix(stderr, "mk: warning: the private key is protected by a passphrase that mk cannot remove\nmk: hint: openssl pkey -in <file> | mk set --stdin app/KEY stores the key without it, with OpenSSL asking for the passphrase on the terminal; keep it when the program reads the passphrase from another entry\n") {
		t.Fatalf("pkcs8: %+v %d %q", outcome, h.terminalOpens, stderr)
	}

	// x/crypto decrypts OpenSSH keys with no other cipher.
	block, _ := pem.Decode(openssh.protected)
	block.Bytes = bytes.Replace(block.Bytes, []byte("aes256-ctr"), []byte("aes128-ctr"), 1)
	unsupported := pem.EncodeToMemory(block)
	h = newHarness(t)
	h.terminal = terminal(t, passphrase+"\n")
	warning = "mk: warning: mk cannot remove the passphrase from this key\nmk: hint: ssh-keygen -p -f <file> rewrites the key in OpenSSH format with a cipher mk reads; enter the same passphrase at its new-passphrase prompts to keep it, then retry; mk cannot remove the passphrase of a security key (sk-)\n"
	if outcome, _, stderr := h.run(t, string(unsupported), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != warning || h.value("app/KEY") != string(unsupported) {
		t.Fatalf("cannot remove: %+v %q", outcome, stderr)
	}

	for _, value := range [][]byte{openssh.plain, []byte("line 1\nline 2\n")} {
		h = newHarness(t)
		h.terminal = terminal(t, passphrase+"\n")
		if outcome, _, stderr := h.run(t, string(value), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != "" || h.terminalOpens != 0 || !bytes.Equal([]byte(h.value("app/KEY")), value) {
			t.Fatalf("unprotected %q: %+v %d %q", value, outcome, h.terminalOpens, stderr)
		}
	}
}

// Interrupting the passphrase prompt writes nothing and gives the terminal
// its echo back.
func TestSetCancelledAtPassphrase(t *testing.T) {
	h := newHarness(t)
	master, tty := openTerminal(t, "")
	// Ends the read that cancelling leaves waiting, which would otherwise
	// keep the terminal from closing.
	t.Cleanup(func() { _, _ = master.WriteString("\n") })
	h.terminal = tty
	fd := int(tty.Fd())
	before, err := term.GetState(fd)
	if err != nil {
		t.Fatal(err)
	}
	stdin := stdinHolding(t, string(keyFixtures(t)[0].protected))
	ctx, cancel := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	done := make(chan Outcome, 1)
	go func() {
		done <- Run(ctx, []string{"set", "--stdin", "app/KEY"}, h.env(stdin, io.Discard, &stderr))
	}()
	// The prompt is waiting once it has turned echo off.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		state, err := term.GetState(fd)
		if err != nil {
			t.Fatal(err)
		}
		if *state != *before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("set did not ask for the passphrase")
		}
	}
	cancel()
	select {
	case outcome := <-done:
		if outcome.Code != 130 || stderr.String() != "mk: error[cancelled]: cancelled before any entry was written\n" || h.value("app/KEY") != "" {
			t.Fatalf("%+v %q", outcome, stderr.String())
		}
		if after, err := term.GetState(fd); err != nil || *after != *before {
			t.Errorf("the terminal was left changed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("set kept asking after it was cancelled")
	}
}

// set stores an SSH key, which mk run's agent cannot unlock, only without
// its passphrase: an empty answer asks again, and where set cannot ask or
// cannot decrypt, it writes nothing.
func TestSetRemovesSSHKeyPassphrase(t *testing.T) {
	openssh := keyFixtures(t)[0]
	h := newHarness(t)
	master, tty := openTerminal(t, "\n"+passphrase+"\n")
	h.terminal = tty
	outcome, stdout, stderr := h.run(t, string(openssh.protected), "set", "--stdin", "app/DEPLOY_SSH_KEY")
	if outcome.Code != 0 || stdout != "created app/DEPLOY_SSH_KEY\n" || stderr != "" || publicKey(t, []byte(h.value("app/DEPLOY_SSH_KEY"))) != publicKey(t, openssh.plain) {
		t.Fatalf("removed: %+v %q %q", outcome, stdout, stderr)
	}
	shown := make([]byte, 4096)
	n, _ := master.Read(shown)
	if want := "app/DEPLOY_SSH_KEY is protected by a passphrase, which mk run cannot ask for. Enter the passphrase to store the key without it, or press Ctrl-C to stop."; !strings.Contains(string(shown[:n]), want) {
		t.Errorf("terminal %q lacks %q", shown[:n], want)
	}

	// An existing entry's kind decides, whatever its name suggests.
	h = newHarness(t)
	h.store.Create(context.Background(), store.Ref{Namespace: "app", Name: "KEY"}, openssh.plain, store.Details{Kind: store.ValueSSH})
	for _, test := range []struct {
		name  string
		value []byte
		want  string
	}{
		{"no terminal", openssh.protected, "mk: error[invalid]: app/KEY: the SSH key is protected by a passphrase, which mk run cannot ask for\nmk: hint: mk set --stdin app/KEY < <file> in a terminal stores the key without it\n"},
		{"pkcs8", pkcs8, "mk: error[invalid]: app/KEY: the private key is protected by a passphrase that mk cannot remove\nmk: hint: openssl pkey -in <file> | mk set --stdin app/KEY stores the key without it, with OpenSSL asking for the passphrase on the terminal\n"},
	} {
		if outcome, _, stderr := h.run(t, string(test.value), "set", "--stdin", "app/KEY"); outcome.Code != 2 || stderr != test.want || h.value("app/KEY") != string(openssh.plain) {
			t.Errorf("%s: %+v %q", test.name, outcome, stderr)
		}
	}
}
