package store

import (
	"slices"
	"strings"
)

// ValueKind says what an entry's value is, for people telling entries apart,
// and for ValueSSH how mk run hands it to a command. A person chooses it,
// starting from the kind the name suggests; nothing reads a value to
// classify it.
type ValueKind string

const (
	ValueToken      ValueKind = "token"      // an API key or access token
	ValuePassword   ValueKind = "password"   // a password or shared secret
	ValuePEM        ValueKind = "pem"        // a private key a program reads itself, such as a TLS key
	ValueSSH        ValueKind = "ssh"        // an SSH private key, which mk run lends through an SSH agent
	ValueURL        ValueKind = "url"        // a URL or connection string
	ValueIdentifier ValueKind = "identifier" // an account, client or user ID
	ValueSetting    ValueKind = "setting"    // any other setting
)

// ValueKinds lists every kind, in the order a person chooses from.
var ValueKinds = []ValueKind{ValueToken, ValuePassword, ValuePEM, ValueSSH, ValueURL, ValueIdentifier, ValueSetting}

// InferValueKind is the kind a name suggests, from its last words:
// GITHUB_TOKEN is a token and DATABASE_URL a URL.
func InferValueKind(name string) ValueKind {
	words := strings.Split(strings.ToUpper(name), "_")
	last := words[len(words)-1]
	pair := last
	if len(words) > 1 {
		pair = words[len(words)-2] + "_" + last
	}
	switch {
	case pair == "SSH_KEY":
		return ValueSSH
	case pair == "PRIVATE_KEY" || last == "PEM":
		return ValuePEM
	case pair == "CONNECTION_STRING" || slices.Contains([]string{"URL", "URI", "DSN", "ENDPOINT", "WEBHOOK"}, last):
		return ValueURL
	case slices.Contains([]string{"ID", "SID", "USER", "USERNAME", "EMAIL", "ACCOUNT", "LOGIN", "ORG", "TENANT"}, last):
		return ValueIdentifier
	case slices.Contains([]string{"TOKEN", "KEY", "APIKEY", "PAT"}, last):
		return ValueToken
	case slices.Contains(words, "SECRET") || slices.Contains(words, "PASSWORD") || slices.Contains([]string{"PASS", "PASSWD", "PWD", "PASSPHRASE", "PIN"}, last):
		return ValuePassword
	}
	return ValueSetting
}

// ValueKindOf is the kind stored for the entry named name, or the kind the
// name suggests when the entry has none stored or one this version does not
// know.
func ValueKindOf(name string, stored ValueKind) ValueKind {
	if slices.Contains(ValueKinds, stored) {
		return stored
	}
	return InferValueKind(name)
}
