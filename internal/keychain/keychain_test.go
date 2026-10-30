package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// fakeInterpreter returns a Keychain whose helper is a shell script, so the
// protocol handling runs without Keychain access.
func fakeInterpreter(t *testing.T, script string) *Keychain {
	t.Helper()
	path := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &Keychain{service: "test.service", interpreter: path}
}

func TestHelperRunsIsolatedUnderAppleInterpreter(t *testing.T) {
	if _, err := os.Stat(Interpreter); err != nil {
		t.Skip("no Apple interpreter")
	}
	// A module in the working directory must not replace the helper's
	// imports, PYTHON* variables must not reach it, and DEVELOPER_DIR must
	// not redirect /usr/bin/python3 to another interpreter.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "json.py"), []byte("raise SystemExit(7)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("PYTHONPATH", dir)
	t.Setenv("PYTHONSTARTUP", filepath.Join(dir, "json.py"))
	t.Setenv("DEVELOPER_DIR", dir)
	if err := New("test.service").Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestHelperRejectsOtherInterpreters(t *testing.T) {
	var other string
	for _, candidate := range []string{"/opt/homebrew/bin/python3", "/usr/local/bin/python3"} {
		if _, err := os.Stat(candidate); err == nil {
			other = candidate
			break
		}
	}
	if other == "" {
		t.Skip("no non-Apple python3 installed")
	}
	k := &Keychain{service: "test.service", interpreter: other}
	err := k.Check(context.Background())
	if store.KindOf(err) != store.KindPrerequisite {
		t.Fatalf("Check under %s: %v, want a prerequisite error", other, err)
	}
}

func TestValueTravelsOnStdinOnly(t *testing.T) {
	dir := t.TempDir()
	k := fakeInterpreter(t, `printf '%s\n' "$@" > `+dir+`/argv
cat > `+dir+`/stdin
echo '{"ready": true}'
echo '{}'`)
	value := []byte("synthetic-value-7f3a")
	if err := k.Add(context.Background(), "ns/NAME", value, store.Details{}); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if bytes.Contains(argv, value) || bytes.Contains(argv, []byte(base64.StdEncoding.EncodeToString(value))) {
		t.Error("the value appears in the helper's arguments")
	}
	if !bytes.Contains(stdin, []byte(base64.StdEncoding.EncodeToString(value))) {
		t.Errorf("stdin %q lacks the encoded value", stdin)
	}
	if !bytes.HasPrefix(argv, []byte("-I\n-S\n-c\n")) {
		t.Errorf("helper arguments %q, want isolated mode", argv)
	}
}

// The helper writes the modification date in seconds since the Unix epoch,
// and leaves out details an item has none of.
func TestItems(t *testing.T) {
	k := fakeInterpreter(t, `cat >/dev/null
echo '{"ready": true}'
echo '{"items": [{"account": "ns/A", "modified": 1793365620.25, "kind": "token", "notes": "rotate in May"}, {"account": "ns/B"}]}'`)
	items, err := k.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Item{
		{Account: "ns/A", Attributes: store.Attributes{Modified: time.Unix(1793365620, 250e6), Details: store.Details{Kind: store.ValueToken, Notes: "rotate in May"}}},
		{Account: "ns/B"},
	}
	if len(items) != len(want) {
		t.Fatalf("items %v, want %v", items, want)
	}
	for i := range want {
		if items[i].Account != want[i].Account || !items[i].Modified.Equal(want[i].Modified) || items[i].Details != want[i].Details {
			t.Errorf("item %d is %+v, want %+v", i, items[i], want[i])
		}
	}
}

func TestHelperFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		op     string
		want   store.Kind
	}{
		{"no ready line", "exit 1", "update", store.KindPrerequisite},
		{"host rejected", `echo '{"error": {"stage": "host", "message": "synthetic host refusal"}}'; exit 1`, "update", store.KindPrerequisite},
		{"write without result", `echo '{"ready": true}'; kill -9 $$`, "update", store.KindUncertain},
		{"edit without result", `echo '{"ready": true}'; kill -9 $$`, "edit", store.KindUncertain},
		{"read without result", `echo '{"ready": true}'; kill -9 $$`, "read", store.KindKeychain},
		{"duplicate", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25299}}'`, "update", store.KindExists},
		{"locked", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25308}}'`, "read", store.KindLocked},
		{"sandboxed", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25291}}'`, "read", store.KindUnavailable},
		{"bad request", `echo '{"ready": true}'; echo '{"error": {"stage": "request", "message": "KeyError"}}'`, "update", store.KindKeychain},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := fakeInterpreter(t, "cat >/dev/null\n"+test.script)
			var err error
			switch test.op {
			case "update":
				err = k.Update(context.Background(), "ns/NAME", []byte("v"))
			case "edit":
				err = k.Edit(context.Background(), "ns/NAME", "ns/OTHER", nil, nil)
			default:
				_, err = k.Read(context.Background(), []string{"ns/NAME"})
			}
			if got := store.KindOf(err); got != test.want {
				t.Fatalf("error %v (kind %q), want kind %q", err, got, test.want)
			}
			// The helper's own reason replaces the advice to install the tools.
			if test.name == "host rejected" && !strings.Contains(err.Error(), "synthetic host refusal") {
				t.Fatalf("error %v does not give the host's reason", err)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	deleteEntry := func(k *Keychain, ctx context.Context) error { return k.Delete(ctx, "ns/NAME") }
	for _, test := range []struct {
		name   string
		result string // what the helper prints after its ready line
		call   func(*Keychain, context.Context) error
		want   store.Kind
	}{
		{"write", "", deleteEntry, store.KindUncertain},
		{"read", "", func(k *Keychain, ctx context.Context) error { _, err := k.Items(ctx); return err }, store.KindCancelled},
		// Cancelled after the helper printed the result, as it exits.
		{"finished write", `echo '{"error": {"stage": "keychain", "os_status": -25300}}'`, deleteEntry, store.KindNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := filepath.Join(t.TempDir(), "started")
			k := fakeInterpreter(t, `cat >/dev/null; echo '{"ready": true}'; `+test.result+`
touch `+started+`; exec sleep 10`)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				for {
					if _, err := os.Stat(started); err == nil {
						cancel()
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			start := time.Now()
			err := test.call(k, ctx)
			if got := store.KindOf(err); got != test.want {
				t.Fatalf("error %v, want kind %q", err, test.want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("cancellation took %v", elapsed)
			}
		})
	}
}
