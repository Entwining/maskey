package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

func TestGrantAndRevoke(t *testing.T) {
	h := newHarness(t)
	h.set(t, "app/A", "app/B", "app/C")
	var reasons []string
	auth := "dialog"
	h.approve = func(_ context.Context, reason string) (string, error) {
		reasons = append(reasons, reason)
		return auth, nil
	}
	if outcome, stdout, stderr := h.run(t, "", "grant", "app/A", "app/B"); outcome.Code != 0 || stdout != "granted app/A\ngranted app/B\n" || stderr != "" {
		t.Fatalf("grant: %+v %q %q", outcome, stdout, stderr)
	}
	// An entry already granted needs no approval and reports no approval.
	_, stdout, _ := h.run(t, "", "grant", "--json", "app/A")
	if want := `{"results":[{"namespace":"app","name":"A","action":"unchanged"}]}` + "\n"; stdout != want || len(reasons) != 1 {
		t.Fatalf("unchanged grant: %q after %d approvals", stdout, len(reasons))
	}
	// A change under an earlier approval says so.
	auth = "reused"
	if outcome, stdout, stderr := h.run(t, "", "revoke", "app/A"); outcome.Code != 0 || stdout != "revoked app/A\n" || stderr != "mk: no dialog: the owner approved a change to program access in the last 5 minutes\n" {
		t.Fatalf("revoke under a reused approval: %+v %q %q", outcome, stdout, stderr)
	}
	_, stdout, stderr := h.run(t, "", "revoke", "--json", "app/B")
	if want := `{"auth":"reused","results":[{"namespace":"app","name":"B","action":"revoked"}]}` + "\n"; stdout != want || stderr != "" {
		t.Fatalf("revoke --json: %q %q", stdout, stderr)
	}
	// A failed write keeps the changes before it, and says how the owner
	// approved them.
	h.backend.Before = func(op, account string) error {
		if op == "grant" && account == "app/B" {
			return &store.Error{Kind: store.KindUncertain, Message: "uncertain"}
		}
		return nil
	}
	outcome, stdout, stderr := h.run(t, "", "grant", "app/A", "app/B", "app/C")
	want := "mk: no dialog: the owner approved a change to program access in the last 5 minutes\nmk: error[uncertain]: app/B: uncertain\nmk: completed: argument 1 app/A (granted)\nmk: not attempted: argument 3\nmk: hint: check program_access in mk list --json before repeating the change\n"
	if outcome.Code != 9 || stdout != "granted app/A\n" || stderr != want {
		t.Fatalf("failed grant: %+v %q %q", outcome, stdout, stderr)
	}
	h.run(t, "", "revoke", "app/A")
	outcome, stdout, stderr = h.run(t, "", "grant", "--json", "app/A", "app/B")
	if body := decode(t, stderr)["error"].(map[string]any); outcome.Code != 9 || stdout != "" || body["auth"] != "reused" || len(body["completed"].([]any)) != 1 {
		t.Fatalf("failed grant --json: %+v %q %q", outcome, stdout, stderr)
	}
	// The failure stands when stdout fails too, since it lists the changes
	// made; on success, a failed stdout warns against repeating them.
	var errOut strings.Builder
	outcome = Run(context.Background(), []string{"grant", "app/A", "app/B"}, h.env(nil, failingWriter{}, &errOut))
	if outcome.Code != 9 || !strings.Contains(errOut.String(), "mk: error[uncertain]: app/B: uncertain\nmk: completed: argument 1 app/A (unchanged)\n") {
		t.Fatalf("failed grant, failed stdout: %+v %q", outcome, errOut.String())
	}
	h.backend.Before = nil
	errOut.Reset()
	outcome = Run(context.Background(), []string{"grant", "app/A"}, h.env(nil, failingWriter{}, &errOut))
	if outcome.Code != 12 || !strings.HasSuffix(errOut.String(), "mk: hint: "+outputHint+"\n") {
		t.Fatalf("failed stdout: %+v %q", outcome, errOut.String())
	}
	// A refused approval changes nothing; without the service, mk says how to
	// install it.
	for _, kind := range []store.Kind{store.KindCancelled, store.KindUnavailable} {
		h.approve = func(context.Context, string) (string, error) {
			return "", &store.Error{Kind: kind, Message: string(kind)}
		}
		outcome, _, stderr := h.run(t, "", "grant", "app/C")
		if errorKind(stderr) != string(kind) || kind == store.KindUnavailable && !strings.Contains(stderr, "mk: hint: run mk setup;") {
			t.Fatalf("%s: %+v %q", kind, outcome, stderr)
		}
	}
	_, listed, _ := h.run(t, "", "list", "--json")
	var granted []string
	for _, entry := range decode(t, listed)["entries"].([]any) {
		if entry := entry.(map[string]any); entry["program_access"] == true {
			granted = append(granted, entry["name"].(string))
		}
	}
	if !slices.Equal(granted, []string{"A"}) {
		t.Fatalf("granted after the changes: %v", granted)
	}
}
