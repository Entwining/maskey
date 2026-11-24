// Package bitwarden is a client for Bitwarden Secrets Manager: it signs a
// machine account in with its access token and reads and writes the secrets
// of the projects that account can reach. Secrets are encrypted and decrypted
// here, with the organization key the sign-in returns; the server stores only
// ciphertext.
package bitwarden

import (
	"encoding/base64"
	"regexp"
	"strings"

	"github.com/Entwining/maskey/internal/store"
)

// Token is a machine account's access token, parsed. It is a credential:
// nothing prints or stores its fields.
type Token struct {
	id     string
	secret string
	key    key
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

// ParseToken reads an access token as Bitwarden issues it,
// "0.<id>.<client secret>:<key seed>". Its errors never quote the text.
func ParseToken(text string) (*Token, error) {
	invalid := func(message string) (*Token, error) {
		return nil, &store.Error{Kind: store.KindInvalid, Message: "the access token " + message}
	}
	head, seed, ok := strings.Cut(strings.TrimSpace(text), ":")
	if !ok {
		return invalid("has no encryption key")
	}
	parts := strings.Split(head, ".")
	switch {
	case len(parts) != 3:
		return invalid("is not in Bitwarden's format")
	case parts[0] != "0":
		return invalid("has a version this Maskey does not support")
	case !uuidPattern.MatchString(parts[1]) || parts[2] == "":
		return invalid("is not in Bitwarden's format")
	}
	// Bitwarden issues the seed with padding and accepts it without.
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(seed, "="))
	if err != nil || len(raw) != 16 {
		return invalid("has a malformed encryption key")
	}
	return &Token{id: strings.ToLower(parts[1]), secret: parts[2], key: deriveShareableKey(raw, "accesstoken", "sm-access-token")}, nil
}
