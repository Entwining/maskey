package cli

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
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
		{"legacy", legacyEncrypted(t, "RSA PRIVATE KEY", der), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})},
	}
}

func legacyEncrypted(t *testing.T, blockType string, der []byte) []byte {
	t.Helper()
	block, err := x509.EncryptPEMBlock(rand.Reader, blockType, der, []byte(passphrase), x509.PEMCipherAES256) //nolint:staticcheck // the legacy format set reads has no other writer
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// p224 is an EC key on a curve SSH does not use, which the agent cannot lend.
func p224(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func unlendable(ref string) string {
	return "mk: error[invalid]: " + ref + ": the SSH agent cannot lend this private key\nmk: hint: store the whole key, of type Ed25519, RSA, or ECDSA P-256, P-384, or P-521; for a program that reads the key itself, choose another kind in Maskey\n"
}

// securityKey is an OpenSSH container protected by a passphrase whose
// public key is a security key's, as ssh-keygen writes one that only its
// authenticator can use.
func securityKey() []byte {
	public := ssh.Marshal(struct {
		Type        string
		Key         []byte
		Application string
	}{ssh.KeyAlgoSKED25519, make([]byte, ed25519.PublicKeySize), "ssh:"})
	container := ssh.Marshal(struct {
		CipherName, KdfName, KdfOpts string
		NumKeys                      uint32
		PubKey, PrivKeyBlock         []byte
	}{"aes256-ctr", "bcrypt", "", 1, public, []byte("synthetic")})
	return pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: append([]byte("openssh-key-v1\x00"), container...)})
}

func TestProtected(t *testing.T) {
	for _, f := range keyFixtures(t) {
		if !protected(f.protected) || protected(f.plain) {
			t.Errorf("%s: protected %v, plain %v", f.name, protected(f.protected), protected(f.plain))
		}
	}
	if !protected([]byte(certificate + string(pkcs8))) {
		t.Error("pkcs8 after a certificate is not protected")
	}
	for _, value := range []string{"synthetic-token", "line 1\nline 2\n", certificate} {
		if protected([]byte(value)) {
			t.Errorf("%q is protected", value)
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
	if _, err := withoutPassphrase(legacyEncrypted(t, "RSA PRIVATE KEY", []byte("synthetic")), []byte(passphrase)); !errors.Is(err, x509.IncorrectPasswordError) {
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

// An entry of another kind than ssh keeps a protected key as it is, for a
// program that reads the passphrase from another entry, and set warns
// without asking, whether or not it has a terminal.
func TestSetKeepsProtectedKeyOfOtherKind(t *testing.T) {
	const warning = "mk: warning: the private key is protected by a passphrase, which mk stores with it\nmk: hint: keep it when the program reads the passphrase from another entry"
	// The hint offers an SSH Key entry only for a key that entry would take.
	type warned struct {
		value []byte
		hint  string
	}
	cases := []warned{
		{pkcs8, "; for ssh or git, openssl pkey -in <file> | mk set --stdin app/<NAME>_SSH_KEY stores the key without it"},
		{securityKey(), ""},
	}
	for _, f := range keyFixtures(t) {
		cases = append(cases, warned{f.protected, "; for ssh or git, mk set --stdin app/<NAME>_SSH_KEY < <file> stores the key without it"})
	}
	for _, c := range cases {
		for _, withTerminal := range []bool{true, false} {
			h := newHarness(t)
			if withTerminal {
				h.terminal = terminal(t, passphrase+"\n")
			}
			if outcome, _, stderr := h.run(t, string(c.value), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != warning+c.hint+"\n" || h.terminalOpens != 0 || !bytes.Equal([]byte(h.value("app/KEY")), c.value) {
				t.Fatalf("%s, terminal %v: %+v %d %q", blockType(c.value), withTerminal, outcome, h.terminalOpens, stderr)
			}
		}
	}
	for _, value := range [][]byte{keyFixtures(t)[0].plain, []byte("line 1\nline 2\n")} {
		h := newHarness(t)
		if outcome, _, stderr := h.run(t, string(value), "set", "--stdin", "app/KEY"); outcome.Code != 0 || stderr != "" || !bytes.Equal([]byte(h.value("app/KEY")), value) {
			t.Fatalf("unprotected %q: %+v %q", value, outcome, stderr)
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
		done <- Run(ctx, []string{"set", "--stdin", "app/DEPLOY_SSH_KEY"}, h.env(stdin, io.Discard, &stderr))
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
		if outcome.Code != 130 || stderr.String() != "mk: error[cancelled]: cancelled before any entry was written\n" || h.value("app/DEPLOY_SSH_KEY") != "" {
			t.Fatalf("%+v %q", outcome, stderr.String())
		}
		if after, err := term.GetState(fd); err != nil || *after != *before {
			t.Errorf("the terminal was left changed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("set kept asking after it was cancelled")
	}
}

// set stores an SSH key, which mk run lends without asking for its
// passphrase, only without it: an empty answer or a wrong passphrase asks
// again, and where set cannot ask or cannot decrypt, it writes nothing.
func TestSetRemovesSSHKeyPassphrase(t *testing.T) {
	openssh := keyFixtures(t)[0]
	h := newHarness(t)
	master, tty := openTerminal(t, "\nwrong\n"+passphrase+"\n")
	h.terminal = tty
	outcome, stdout, stderr := h.run(t, string(openssh.protected), "set", "--stdin", "app/DEPLOY_SSH_KEY")
	if outcome.Code != 0 || stdout != "created app/DEPLOY_SSH_KEY\n" || stderr != "" || publicKey(t, []byte(h.value("app/DEPLOY_SSH_KEY"))) != publicKey(t, openssh.plain) {
		t.Fatalf("removed: %+v %q %q", outcome, stdout, stderr)
	}
	shown := make([]byte, 4096)
	n, _ := master.Read(shown)
	for _, want := range []string{"app/DEPLOY_SSH_KEY is protected by a passphrase, which mk run does not ask for. Enter the passphrase to store the key without it, or press Ctrl-C to stop.", "Enter the passphrase or press Ctrl-C (input hidden): ", "Bad passphrase, try again (input hidden): "} {
		if !strings.Contains(string(shown[:n]), want) {
			t.Errorf("terminal %q lacks %q", shown[:n], want)
		}
	}

	// x/crypto decrypts OpenSSH keys with no other cipher.
	block, _ := pem.Decode(openssh.protected)
	block.Bytes = bytes.Replace(block.Bytes, []byte("aes256-ctr"), []byte("aes128-ctr"), 1)
	unsupportedCipher := pem.EncodeToMemory(block)

	// An existing entry's kind decides, whatever its name suggests.
	h = newHarness(t)
	h.store.Create(context.Background(), store.Ref{Namespace: "app", Name: "KEY"}, openssh.plain, store.Details{Kind: store.ValueSSH})
	for _, test := range []struct {
		name  string
		value []byte
		opens int
		want  string
	}{
		{"no terminal", openssh.protected, 0, "mk: error[invalid]: app/KEY: the SSH key is protected by a passphrase, which mk run does not ask for\nmk: hint: mk set --stdin app/KEY < <file> in a terminal stores the key without it\n"},
		{"pkcs8", pkcs8, 0, "mk: error[invalid]: app/KEY: the private key is protected by a passphrase that mk cannot remove\nmk: hint: openssl pkey -in <file> | mk set --stdin app/KEY stores the key without it, with OpenSSL asking for the passphrase on the terminal\n"},
		{"public key", []byte("ssh-ed25519 AAAA synthetic\n"), 0, "mk: error[invalid]: app/KEY: the value of an entry of kind ssh is not an SSH private key\nmk: hint: store the private key itself, not its .pub file or a certificate before the key\n"},
		// mk run reads the first PEM block, so set asks for no passphrase.
		{"certificate first", []byte(certificate + string(openssh.protected)), 0, "mk: error[invalid]: app/KEY: the value of an entry of kind ssh is not an SSH private key\nmk: hint: store the private key itself, not its .pub file or a certificate before the key\n"},
		// x/crypto asks a passphrase for any encrypted block, which set
		// decrypts only in a private key.
		{"encrypted other block", pem.EncodeToMemory(&pem.Block{Type: "SYNTHETIC", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: []byte("synthetic")}), 0, "mk: error[invalid]: app/KEY: the value of an entry of kind ssh is not an SSH private key\nmk: hint: store the private key itself, not its .pub file or a certificate before the key\n"},
		// The agent lends no P-224 key, which decrypts all the same.
		{"unlendable once decrypted", legacyEncrypted(t, "EC PRIVATE KEY", p224(t)), 1, unlendable("app/KEY")},
		{"security key", securityKey(), 0, unlendable("app/KEY")},
		{"unsupported cipher", unsupportedCipher, 1, "mk: error[invalid]: app/KEY: mk cannot remove the passphrase from this key\nmk: hint: ssh-keygen -p -f <file> rewrites the key in OpenSSH format with a cipher mk reads; enter the same passphrase at its new-passphrase prompts to keep it, then retry\n"},
	} {
		h.terminal = terminal(t, passphrase+"\n")
		if test.name == "no terminal" {
			h.terminal = nil
		}
		h.terminalOpens = 0
		if outcome, _, stderr := h.run(t, string(test.value), "set", "--stdin", "app/KEY"); outcome.Code != 2 || stderr != test.want || h.value("app/KEY") != string(openssh.plain) || test.name != "no terminal" && h.terminalOpens != test.opens {
			t.Errorf("%s: %+v %d %q", test.name, outcome, h.terminalOpens, stderr)
		}
	}
	// The hidden prompt reads a key's first line alone.
	h.terminal = nil
	if outcome, _, stderr := h.runOn(terminal(t, "y\n"+strings.SplitAfter(string(openssh.plain), "\n")[0]), "set", "app/KEY"); outcome.Code != 2 || !strings.HasSuffix(stderr, "mk: error[invalid]: app/KEY: the value of an entry of kind ssh is not an SSH private key\nmk: hint: an SSH key has several lines, which the hidden prompt does not read; pipe it with mk set --stdin app/KEY < <file>\n") {
		t.Errorf("hidden prompt: %+v %q", outcome, stderr)
	}
}
