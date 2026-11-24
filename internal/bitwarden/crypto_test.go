package bitwarden

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/bitwarden/bitwardentest"
	"github.com/Entwining/maskey/internal/store"
)

// The vectors in this file are the ones Bitwarden publishes in its SDK's own
// tests (bitwarden/sdk-internal: keys/shareable_key.rs, auth/access_token.rs
// and hazmat/symmetric_encryption/aes256_cbc_hmac_sha256_ae.rs), so they
// check this package against Bitwarden's implementation, not against itself.

// vectorToken is the SDK's sample access token, which no server issued.
const vectorToken = bitwardentest.Token

func vectorKey() *key {
	var k key
	for i := range k {
		k[i] = byte(i)
	}
	return &k
}

func encode(k key) string { return base64.StdEncoding.EncodeToString(k[:]) }

func TestDeriveShareableKeyMatchesBitwarden(t *testing.T) {
	for _, test := range []struct{ secret, info, want string }{
		{"&/$%F1a895g67HlX", "", "4PV6+PcmF2w7YHRatvyMcVQtI7zvCyssv/wFWmzjiH6Iv9altjmDkuBD1aagLVaLezbthbSe+ktR+U6qswxNnQ=="},
		{"67t9b5g67$%Dh89n", "test", "F9jVQmrACGx9VUPjuzfMYDjr726JtL300Y3Yg+VYUnVQtQ1s8oImJ5xtp1KALC9h2nav04++1LDW4iFD+infng=="},
	} {
		if got := encode(deriveShareableKey([]byte(test.secret), "test_key", test.info)); got != test.want {
			t.Errorf("info %q: derived %s, want %s", test.info, got, test.want)
		}
	}
}

func TestParseTokenMatchesBitwarden(t *testing.T) {
	const want = "H9/oIRLtL9nGCQOVDjSMoEbJsjWXSOCb3qeyDt6ckzS3FhyboEDWyTP/CQfbIszNmAVg2ExFganG1FVFGXO/Jg=="
	for _, text := range []string{vectorToken, strings.TrimRight(vectorToken, "="), " " + vectorToken + "\n"} {
		token, err := ParseToken(text)
		if err != nil {
			t.Fatal(err)
		}
		if token.id != "ec2c1d46-6a4b-4751-a310-af9601317f2d" || token.secret != "C2IgxjjLF7qSshsbwe8JGcbM075YXw" || encode(token.key) != want {
			t.Errorf("token parsed as id %s with key %s", token.id, encode(token.key))
		}
	}
}

func TestParseTokenRejectsWithoutQuoting(t *testing.T) {
	head, seed, _ := strings.Cut(vectorToken, ":")
	for name, text := range map[string]string{
		"no key":        head,
		"version":       "1" + vectorToken[1:],
		"four parts":    strings.Replace(vectorToken, ":", ".", 1),
		"two parts":     vectorToken[2:],
		"identifier":    "0.not-a-uuid.secret:" + seed,
		"empty secret":  "0.ec2c1d46-6a4b-4751-a310-af9601317f2d.:" + seed,
		"short key":     head + ":AAAA",
		"key not bytes": head + ":!!!!",
	} {
		_, err := ParseToken(text)
		if !store.IsKind(err, store.KindInvalid) {
			t.Errorf("%s: got %v, want invalid", name, err)
			continue
		}
		for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == ':' }) {
			if len(part) > 4 && strings.Contains(err.Error(), part) {
				t.Errorf("%s: the error quotes the token: %v", name, err)
			}
		}
	}
}

func TestCipherMatchesBitwarden(t *testing.T) {
	iv := []byte{216, 218, 36, 0, 196, 186, 150, 85, 49, 147, 110, 168, 185, 227, 42, 172}
	mac := []byte{
		60, 78, 44, 111, 72, 233, 3, 6, 86, 250, 217, 242, 62, 229, 184, 221, 231, 150, 189, 44,
		99, 189, 220, 55, 196, 194, 101, 60, 102, 195, 149, 130,
	}
	data := []byte{
		234, 77, 16, 15, 189, 82, 36, 188, 182, 88, 64, 67, 145, 94, 30, 178, 36, 235, 130, 67,
		255, 207, 183, 168, 73, 231, 82, 122, 193, 139, 25, 129,
	}
	b64 := base64.StdEncoding.EncodeToString
	text := "2." + b64(iv) + "|" + b64(data) + "|" + b64(mac)
	const plaintext = "Bitwarden SDK test vector"

	got, err := vectorKey().decrypt(text)
	if err != nil || string(got) != plaintext {
		t.Fatalf("decrypted %q, %v", got, err)
	}
	if sealed := vectorKey().encryptWith([16]byte(iv), []byte(plaintext)); sealed != text {
		t.Errorf("encrypted to %s, want %s", sealed, text)
	}
}

func TestEncryptUsesAFreshIV(t *testing.T) {
	k := vectorKey()
	first, second := k.encrypt([]byte("value")), k.encrypt([]byte("value"))
	if first == second {
		t.Fatal("two encryptions of one value are equal")
	}
	for _, plaintext := range [][]byte{{}, []byte("0123456789abcdef"), bytes.Repeat([]byte("\r\n\x00é"), 40)} {
		got, err := k.decrypt(k.encrypt(plaintext))
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Errorf("%d bytes came back as %d, %v", len(plaintext), len(got), err)
		}
	}
}

func TestDecryptRejects(t *testing.T) {
	k := vectorKey()
	sealed := k.encryptWith([16]byte{}, []byte("value"))
	parts := strings.Split(strings.TrimPrefix(sealed, "2."), "|")
	flip := func(field string) string {
		raw, _ := base64.StdEncoding.DecodeString(field)
		raw[0] ^= 1
		return base64.StdEncoding.EncodeToString(raw)
	}
	other := *k
	other[40] ^= 1
	// A ciphertext whose padding is wrong under a MAC that is right.
	block := bytes.Repeat([]byte{7}, 16)
	badPadding := "2." + parts[0] + "|" + base64.StdEncoding.EncodeToString(block) + "|" +
		base64.StdEncoding.EncodeToString(k.mac(make([]byte, 16), block))

	for name, test := range map[string]struct {
		key  *key
		text string
		want error
	}{
		"changed iv":     {k, "2." + flip(parts[0]) + "|" + parts[1] + "|" + parts[2], errCipherMAC},
		"changed data":   {k, "2." + parts[0] + "|" + flip(parts[1]) + "|" + parts[2], errCipherMAC},
		"changed mac":    {k, "2." + parts[0] + "|" + parts[1] + "|" + flip(parts[2]), errCipherMAC},
		"another key":    {&other, sealed, errCipherMAC},
		"no mac":         {k, "2." + parts[0] + "|" + parts[1], errCipherText},
		"unpadded":       {k, "2." + parts[0] + "|" + parts[1][:8] + "|" + parts[2], errCipherText},
		"not base64":     {k, "2.!|" + parts[1] + "|" + parts[2], errCipherText},
		"empty data":     {k, "2." + parts[0] + "||" + parts[2], errCipherText},
		"bad padding":    {k, badPadding, errCipherText},
		"legacy type 0":  {k, "0." + parts[0] + "|" + parts[1], errCipherType},
		"future type 7":  {k, "7." + parts[1], errCipherType},
		"no type":        {k, parts[1], errCipherType},
		"plaintext text": {k, "value", errCipherType},
	} {
		if _, err := test.key.decrypt(test.text); !errors.Is(err, test.want) {
			t.Errorf("%s: got %v, want %v", name, err, test.want)
		}
	}
}
