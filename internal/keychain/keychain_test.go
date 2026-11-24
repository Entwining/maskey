package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	return &Keychain{service: "test.service", interpreter: path, timeout: Timeout}
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
	k := &Keychain{service: "test.service", interpreter: other, timeout: Timeout}
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
echo '{"items": [{"account": "ns/A", "modified": 1793365620.25, "program_access": true, "kind": "token", "notes": "rotate in May"}, {"account": "ns/B"}]}'`)
	items, err := k.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Item{
		{Account: "ns/A", Attributes: store.Attributes{Modified: time.Unix(1793365620, 250e6), ProgramAccess: true, Details: store.Details{Kind: store.ValueToken, Notes: "rotate in May"}}},
		{Account: "ns/B"},
	}
	if len(items) != len(want) {
		t.Fatalf("items %v, want %v", items, want)
	}
	for i := range want {
		if items[i].Account != want[i].Account || !items[i].Modified.Equal(want[i].Modified) || items[i].ProgramAccess != want[i].ProgramAccess || items[i].Details != want[i].Details {
			t.Errorf("item %d is %+v, want %+v", i, items[i], want[i])
		}
	}
}

func TestGet(t *testing.T) {
	value := []byte("synthetic-value\n")
	k := fakeInterpreter(t, `cat >/dev/null
echo '{"ready": true}'
echo '{"value": "`+base64.StdEncoding.EncodeToString(value)+`"}'`)
	got, err := k.Get(context.Background(), "ns/A")
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("Get: %q, %v", got, err)
	}
}

func TestRead(t *testing.T) {
	value := []byte("synthetic-value\n")
	k := fakeInterpreter(t, `cat >/dev/null
echo '{"ready": true}'
echo '{"values": {"ns/A": {"value": "`+base64.StdEncoding.EncodeToString(value)+`", "kind": "ssh"}, "ns/B": {"value": "dg=="}}}'`)
	got, err := k.Read(context.Background(), []string{"ns/A", "ns/B", "ns/C"})
	want := map[string]store.Stored{"ns/A": {Value: value, Kind: store.ValueSSH}, "ns/B": {Value: []byte("v")}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Read: %+v, %v", got, err)
	}
}

// The helper writes and reads program access with Core Foundation and
// Security replaced by Python objects, so no Keychain call runs. Only a
// CFData holding a JSON object whose program_access member is true grants
// it; any other content of kSecAttrGeneric, as another tool or a later
// version may write, does not. Only grant and revoke write it, and get
// returns no value without it. read returns each value with the kind of
// the item it came from.
func TestHelperProgramAccess(t *testing.T) {
	if _, err := os.Stat(Interpreter); err != nil {
		t.Skip("no Apple interpreter")
	}
	script := `import sys, types
helper = {"__name__": "helper"}
exec(sys.stdin.read(), helper)

import ctypes
# The item SecItemCopyMatching finds, which its result points to.
found = {}

class Fake(helper["Keychain"]):
    def __init__(self):
        self.owned = []
        lookup = lambda d, key: (found if isinstance(d, ctypes.c_void_p) else d).get(key)
        self.cf = types.SimpleNamespace(CFDictionaryGetValue=lookup, CFGetTypeID=type, CFDataGetTypeID=lambda: bytes)
        self.sec = types.SimpleNamespace(SecItemUpdate=self.update, SecItemCopyMatching=self.match)
    def match(self, query, result):
        result._obj.value = 1
        return 0
    def update(self, query, changes):
        self.changes = changes
        return 0
    def constant(self, name, library=None): return name
    def string(self, text): return text
    def text(self, cf_string): return cf_string
    def data(self, cf_data): return cf_data
    def value_data(self, value): return bytes(value)
    def dictionary(self, pairs): return dict(pairs)
    def query(self, service, account=None, extra=()): return account

def change(op, **fields):
    keychain = Fake()
    helper["handle"](keychain, dict(op=op, service="s", account="ns/A", to="ns/A", **fields))
    return keychain.changes

def granted(marks):
    return Fake().item({"kSecAttrAccount": "ns/A", helper["MARKS"]: marks})["program_access"]

def get(marks):
    found.update({"kSecAttrAccount": "ns/A", helper["MARKS"]: marks, "kSecValueData": b"v"})
    try:
        return helper["handle"](Fake(), dict(op="get", service="s", account="ns/A"))[1]["value"]
    except helper["NotGranted"]:
        return None

for op in ("grant", "revoke"):
    print(granted(change(op)[helper["MARKS"]]))
print(helper["MARKS"] in change("edit", details={}, program_access=True))
print(get(b'{"program_access":true}'), get(b'{"program_access":false}'))
print(granted('{"program_access":true}'))
for marks in sys.argv[1:]:
    print(granted(bytes.fromhex(marks)))
found.clear()
found.update({"kSecAttrAccount": "ns/A", "kSecAttrDescription": "ssh", "kSecValueData": b"v"})
print(helper["handle"](Fake(), dict(op="read", service="s", accounts=["ns/A"]))[1])`
	marks := []string{`{"program_access":true}`, `{"program_access":true,"other":1}`, `{"program_access":false}`, `{"program_access":"true"}`, `{}`, `[true]`, `true`, `{"program_access":tru`, "\xff", ""}
	args := []string{"-I", "-S", "-c", script}
	for _, m := range marks {
		args = append(args, hex.EncodeToString([]byte(m)))
	}
	cmd := exec.Command(Interpreter, args...)
	cmd.Stdin = strings.NewReader(helperSource)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// Granted, revoked, untouched by an edit that names it, a value only
	// with it, not granted by a string, the marks above in order, then a
	// read.
	if want := "True\nFalse\nFalse\ndg== None\nFalse\nTrue\nTrue\n" + strings.Repeat("False\n", len(marks)-2) + "{'values': {'ns/A': {'value': 'dg==', 'kind': 'ssh'}}}\n"; string(output) != want {
		t.Fatalf("program access read as\n%s", output)
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
		{"grant without result", `echo '{"ready": true}'; kill -9 $$`, "grant", store.KindUncertain},
		{"read without result", `echo '{"ready": true}'; kill -9 $$`, "read", store.KindKeychain},
		{"duplicate", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25299}}'`, "update", store.KindExists},
		{"locked", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25308}}'`, "read", store.KindLocked},
		{"sandboxed", `echo '{"ready": true}'; echo '{"error": {"stage": "keychain", "os_status": -25291}}'`, "read", store.KindUnavailable},
		{"bad request", `echo '{"ready": true}'; echo '{"error": {"stage": "request", "message": "KeyError"}}'`, "update", store.KindKeychain},
		{"not granted", `echo '{"ready": true}'; echo '{"error": {"stage": "not_granted"}}'`, "get", store.KindNotGranted},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := fakeInterpreter(t, "cat >/dev/null\n"+test.script)
			var err error
			switch test.op {
			case "update":
				err = k.Update(context.Background(), "ns/NAME", []byte("v"))
			case "edit":
				err = k.Edit(context.Background(), "ns/NAME", "ns/OTHER", nil, nil)
			case "grant":
				err = k.SetProgramAccess(context.Background(), "ns/NAME", "", true)
			case "get":
				_, err = k.Get(context.Background(), "ns/NAME")
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

// A helper that does not finish in time ends its operation as a failure,
// not a cancellation: a write may have happened, and a read did not.
func TestHelperTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
		call   func(*Keychain) error
		want   store.Kind
	}{
		{"before ready", "", func(k *Keychain) error { return k.Delete(context.Background(), "ns/NAME") }, store.KindPrerequisite},
		{"write", `echo '{"ready": true}'; `, func(k *Keychain) error { return k.Delete(context.Background(), "ns/NAME") }, store.KindUncertain},
		{"read", `echo '{"ready": true}'; `, func(k *Keychain) error { _, err := k.Get(context.Background(), "ns/NAME"); return err }, store.KindKeychain},
	} {
		t.Run(test.name, func(t *testing.T) {
			k := fakeInterpreter(t, "cat >/dev/null; "+test.script+"exec sleep 10")
			k.timeout = time.Second
			start := time.Now()
			err := test.call(k)
			if store.KindOf(err) != test.want || !strings.Contains(err.Error(), "stopped after 1s") {
				t.Fatalf("error %v, want kind %q", err, test.want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("the helper ran %v", elapsed)
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

// An item that stays on this Mac is selected in the data protection
// Keychain and never syncs; a set replaces it or, when there is none,
// creates it readable only on this Mac. The value travels as the entries' do.
func TestHelperLocalItems(t *testing.T) {
	if _, err := os.Stat(Interpreter); err != nil {
		t.Skip("no Apple interpreter")
	}
	script := `import sys, types, ctypes
helper = {"__name__": "helper"}
exec(sys.stdin.read(), helper)
calls = []

class Fake(helper["Keychain"]):
    def __init__(self, existing):
        self.owned = []
        self.existing = existing
        self.cf = types.SimpleNamespace()
        self.sec = types.SimpleNamespace(SecItemUpdate=self.update, SecItemAdd=self.add, SecItemDelete=self.delete, SecItemCopyMatching=self.match)
    def update(self, query, changes):
        calls.append(("update", query, changes))
        return 0 if self.existing else helper["ERR_SEC_ITEM_NOT_FOUND"]
    def add(self, attributes, result):
        calls.append(("add", attributes))
        return 0
    def delete(self, query):
        calls.append(("delete", query))
        return 0
    def match(self, query, result):
        calls.append(("match", query))
        result._obj.value = 1
        return 0
    def constant(self, name, library=None): return name
    def string(self, text): return text
    def data(self, cf_data): return b"v"
    def value_data(self, value): return bytes(value)
    def dictionary(self, pairs): return dict(pairs)

def show(call):
    return call[0] + " " + " | ".join(",".join(k + "=" + str(d[k]) for k in sorted(d)) for d in call[1:])

for existing in (True, False):
    helper["handle"](Fake(existing), dict(op="local_set", service="s.local", account="token/bitwarden", value="dg=="))
print(helper["handle"](Fake(True), dict(op="local_get", service="s.local", account="token/bitwarden")))
helper["handle"](Fake(True), dict(op="local_delete", service="s.local", account="token/bitwarden"))
for call in calls:
    print(show(call))`
	cmd := exec.Command(Interpreter, "-I", "-S", "-c", script)
	cmd.Stdin = strings.NewReader(helperSource)
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	const item = "kSecAttrAccount=token/bitwarden,kSecAttrService=s.local,kSecAttrSynchronizable=kCFBooleanFalse,kSecClass=kSecClassGenericPassword,kSecUseDataProtectionKeychain=kCFBooleanTrue"
	want := strings.Join([]string{
		"(0, {'value': 'dg=='})",
		"update " + item + " | kSecValueData=b'v'",
		"update " + item + " | kSecValueData=b'v'",
		"add kSecAttrAccessible=kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly," + strings.Replace(item, ",kSecAttrService", ",kSecAttrLabel=Maskey: token/bitwarden,kSecAttrService", 1) + ",kSecValueData=b'v'",
		"match " + strings.Replace(item, ",kSecUse", ",kSecMatchLimit=kSecMatchLimitOne,kSecReturnData=kCFBooleanTrue,kSecUse", 1),
		"delete " + item,
	}, "\n") + "\n"
	if string(output) != want {
		t.Fatalf("local items ran\n%s\nwant\n%s", output, want)
	}
}

func TestLocalItemsUseTheirOwnService(t *testing.T) {
	dir := t.TempDir()
	k := fakeInterpreter(t, `cat > `+dir+`/stdin
echo '{"ready": true}'
echo '{"value": "dg=="}'`)
	if value, err := k.LocalGet(context.Background(), "token/bitwarden"); err != nil || string(value) != "v" {
		t.Fatalf("LocalGet: %q, %v", value, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if !bytes.Contains(stdin, []byte(`"service":"test.service.local"`)) {
		t.Errorf("the request %s names another service", stdin)
	}
}
