package cli

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

// protection is what set can do for a value that holds a private key.
type protection int

const (
	unprotected protection = iota // no private key, or one without a passphrase
	removable                     // an OpenSSH or legacy PEM key, whose passphrase mk can remove
	unremovable                   // a PKCS#8 key, whose encryption only OpenSSL decrypts
)

// privateKey finds value's private key, the first PEM block whose type ends
// in PRIVATE KEY, wherever it is, as after its certificate, and the offsets
// of the block's lines.
func privateKey(value []byte) (block *pem.Block, start, end int) {
	for rest := value; ; {
		if block, rest = pem.Decode(rest); block == nil {
			return nil, 0, 0
		}
		if strings.HasSuffix(block.Type, "PRIVATE KEY") {
			end = len(value) - len(rest)
			// pem.Decode takes the last BEGIN line before the END line.
			start = bytes.LastIndex(value[:end], []byte("\n-----BEGIN ")) + 1
			return block, start, end
		}
	}
}

// protectionOf classifies value. A PKCS#8 ENCRYPTED PRIVATE KEY block is
// known by its type alone: neither the standard library nor x/crypto
// decrypts PKCS#8 (golang/go#8860).
func protectionOf(value []byte) protection {
	block, start, end := privateKey(value)
	switch {
	case block == nil:
		return unprotected
	case block.Type == "ENCRYPTED PRIVATE KEY":
		return unremovable
	case x509.IsEncryptedPEMBlock(block): //nolint:staticcheck // the legacy encryption is what mk removes
		return removable
	}
	_, err := ssh.ParseRawPrivateKey(value[start:end])
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return removable
	}
	return unprotected
}

// withoutPassphrase is value with its private key stored without the
// passphrase, in the container it came in; what surrounds the key, such as
// its certificate, stays as it was. A wrong passphrase is
// x509.IncorrectPasswordError.
func withoutPassphrase(value, passphrase []byte) ([]byte, error) {
	block, start, end := privateKey(value)
	plain := &pem.Block{Type: block.Type}
	if x509.IsEncryptedPEMBlock(block) { //nolint:staticcheck // the legacy encryption is what mk removes
		der, err := x509.DecryptPEMBlock(block, passphrase) //nolint:staticcheck // as above
		if err != nil {
			return nil, err
		}
		// The padding check lets about one wrong passphrase in 256 through;
		// what such a passphrase decrypts is not one DER SEQUENCE.
		var key asn1.RawValue
		if rest, err := asn1.Unmarshal(der, &key); err != nil || len(rest) > 0 || key.Class != asn1.ClassUniversal || key.Tag != asn1.TagSequence {
			return nil, x509.IncorrectPasswordError
		}
		plain.Bytes = der
	} else {
		key, err := ssh.ParseRawPrivateKeyWithPassphrase(value[start:end], passphrase)
		if err != nil {
			return nil, err
		}
		// The parser returns the key alone, so its comment cannot be kept.
		if plain, err = ssh.MarshalPrivateKey(key, ""); err != nil {
			return nil, err
		}
	}
	return slices.Concat(value[:start], pem.EncodeToMemory(plain), value[end:]), nil
}
