package cli

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

func TestStoreTokenPreflight(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"invalid region", []string{"add", "bitwarden", "--region", "other"}, 2, "the region is us or eu"},
		{"invalid name", []string{"add", "bitwarden", "--name", "Work"}, 2, "a store name starts with a lowercase ASCII letter or digit"},
		{"taken name", []string{"add", "bitwarden", "--name", "work"}, 4, "a store with that name exists"},
		{"taken implicit name", []string{"add", "bitwarden"}, 4, "a store with that name exists"},
		{"built-in name", []string{"add", "bitwarden", "--name", "icloud"}, 4, "a store with that name exists"},
		{"built-in store", []string{"token", "icloud"}, 2, "iCloud Keychain has no access token"},
		{"missing store", []string{"token", "unknown-name"}, 3, "this Mac has no store with that name"},
		{"empty name", []string{"token", ""}, 3, "this Mac has no store with that name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.connect()
			h.config.Stores = append(h.config.Stores, stores.Connection{ID: stores.Bitwarden, Type: stores.Bitwarden})
			for _, flags := range [][]string{nil, {"--stdin"}} {
				in := stdinHolding(t, "synthetic-token\n")
				args := append(append([]string{"store"}, test.args...), flags...)
				outcome, stdout, stderr := h.runOn(in, args...)
				offset, err := in.Seek(0, io.SeekCurrent)
				if outcome.Code != test.code || !strings.Contains(stderr, test.message) || stdout != "" ||
					strings.Contains(stderr, "unknown-name") || err != nil || offset != 0 || h.terminalOpens != 0 ||
					h.added.Token != nil || len(h.storeChanges) != 0 {
					t.Errorf("%q: %+v %q %q, stdin offset %d, %d terminal opens", args, outcome, stdout, stderr, offset, h.terminalOpens)
				}
			}
		})
	}
}

func TestStoreTokenNeedsTerminalOrStdinFlag(t *testing.T) {
	for _, args := range [][]string{{"store", "add", "bitwarden"}, {"store", "token", "work"}} {
		t.Run(args[1], func(t *testing.T) {
			h := newHarness(t)
			h.connect()
			outcome, stdout, stderr := h.run(t, "", args...)
			want := "mk: error[usage]: standard input is not a terminal and store " + args[1] + " needs --stdin\n" +
				"mk: hint: have the owner run this command in their terminal or pipe the access token with --stdin\n"
			if outcome.Code != 2 || stdout != "" || stderr != want || h.terminalOpens != 0 {
				t.Fatalf("%+v %q %q, %d terminal opens", outcome, stdout, stderr, h.terminalOpens)
			}
		})
	}
}

func TestStoreTokenInputErrors(t *testing.T) {
	for _, test := range []struct {
		args      []string
		unchanged string
		cancelled string
	}{
		{[]string{"store", "add", "bitwarden", "--stdin"}, "no store was added", "cancelled before the store was added"},
		{[]string{"store", "token", "work", "--stdin"}, "the access token was not replaced", "cancelled before the access token was replaced"},
	} {
		t.Run(test.args[1], func(t *testing.T) {
			h := newHarness(t)
			h.connect()
			t.Run("read failure", func(t *testing.T) {
				in := stdinHolding(t, "")
				in.Close()
				outcome, stdout, stderr := h.runOn(in, test.args...)
				want := "mk: error[io]: cannot read the access token\nmk: hint: check the input source; " + test.unchanged + "\n"
				if outcome.Code != 12 || stdout != "" || stderr != want || h.added.Token != nil || len(h.storeChanges) != 0 {
					t.Fatalf("%+v %q %q", outcome, stdout, stderr)
				}
			})
			t.Run("cancelled", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				var stdout, stderr bytes.Buffer
				outcome := Run(ctx, test.args, h.env(stdinHolding(t, "synthetic-token\r\n"), &stdout, &stderr))
				if outcome.Code != 130 || stdout.Len() != 0 || stderr.String() != "mk: error[cancelled]: "+test.cancelled+"\n" || h.added.Token != nil || len(h.storeChanges) != 0 {
					t.Fatalf("%+v %q %q", outcome, stdout.String(), stderr.String())
				}
			})
		})
	}
}

func TestStoreTokenPrompt(t *testing.T) {
	h := newHarness(t)
	h.connect()
	outcome, _, stderr, onTerminal := h.runAsking(t, "synthetic-token\n", "store", "token", "work")
	if outcome.Code != 0 || stderr != "" || !strings.Contains(onTerminal, "New access token for work (input hidden): ") ||
		!slices.Equal(h.storeChanges, []string{`token work target="bitwarden/eu/org-1/p-1" synthetic-token`}) {
		t.Fatalf("%+v %q, token prompt %q, changes %v", outcome, stderr, onTerminal, h.storeChanges)
	}
	outcome, _, stderr, onTerminal = h.runAsking(t, "x\n", "store", "token", "synthetic-wrong-operand")
	if outcome.Code != 3 || h.terminalOpens != 0 || strings.Contains(onTerminal+stderr, "synthetic-wrong-operand") {
		t.Fatalf("%+v %q, token prompt %q, %d terminal opens", outcome, stderr, onTerminal, h.terminalOpens)
	}
	h.storeErr = &store.Error{Kind: store.KindConflict, Message: "the store was connected to another project since this began; nothing was done"}
	outcome, stdout, stderr, _ := h.runAsking(t, "synthetic-token\n", "store", "token", "work")
	if outcome.Code != 5 || stdout != "" || !strings.HasSuffix(stderr, "mk: hint: run mk store list to check the store, then retry\n") {
		t.Fatalf("stale connection: %+v %q %q", outcome, stdout, stderr)
	}
}

func TestAddingStoreRecoveries(t *testing.T) {
	for _, test := range []struct {
		kind store.Kind
		code int
		hint string
	}{
		{store.KindNotFound, 3, "pass a project this machine account can read with --project"},
		{store.KindConflict, 5, "pass the project ID with --project"},
		{store.KindUnauthenticated, 14, "check the region and access token, then run the same command again"},
		{store.KindExists, 4, "choose another store name with --name"},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			h := newHarness(t)
			h.storeErr = &store.Error{Kind: test.kind, Message: "synthetic store add failure"}
			outcome, stdout, stderr := h.run(t, "synthetic-token\n", "store", "add", "bitwarden", "--region", "eu",
				"--name", "team", "--project", "Deploy", "--stdin", "--json")
			body := decode(t, stderr)["error"].(map[string]any)
			if outcome.Code != test.code || stdout != "" || body["kind"] != string(test.kind) ||
				body["exit_code"] != float64(test.code) || body["recovery"] != test.hint {
				t.Fatalf("%+v %q %s", outcome, stdout, stderr)
			}
		})
	}
}
