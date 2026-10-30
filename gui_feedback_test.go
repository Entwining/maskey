package main

import (
	"testing"
	"time"
)

func TestSubmittingFormKeepsCapturedDraft(t *testing.T) {
	for _, tc := range []struct {
		action    string
		operation string
		account   string
	}{
		{action: "Add", operation: "add", account: "app/NEW_KEY"},
		{action: "Save", operation: "edit", account: "app/URL"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newHarness(t)
			if tc.action == "Add" {
				h.newInApp("NEW_KEY")
			} else {
				h.choose("URL", "Edit")
			}
			const value = "synthetic-submitted"
			h.typeValue(value)
			started, release := h.pause(tc.operation)
			h.clickWithoutSettling(tc.action)
			h.waitFor(started)
			h.typeValue("synthetic-new-draft")
			draft := h.g.form.value
			release()
			h.settle()
			if draft != value {
				t.Fatalf("input changed while its submission was pending: %q", draft)
			}
			if got, ok := h.value(tc.account); !ok || got != value {
				t.Fatalf("submitted value changed: stored %q, present %v", got, ok)
			}
		})
	}
}

// A write that completes after its form was closed and another opened
// leaves the new form as it is, and the new form shows no progress for it.
func TestCompletedWritePreservesReopenedForm(t *testing.T) {
	for _, tc := range []struct {
		action, operation string
		first, second     string
		open              func(h *harness, entry string)
		state             func(g *gui) (open bool, entry string)
	}{
		{
			action: "Add", operation: "add", first: "FIRST", second: "SECOND",
			open: func(h *harness, name string) {
				h.clickWithoutSettling("New Entry")
				h.typeName(name)
			},
			state: func(g *gui) (bool, string) { return g.form.open, g.form.name },
		},
		{
			// The first save is still pending, so the second editor loads
			// its value without settling.
			action: "Save", operation: "edit", first: "URL", second: "API_TOKEN",
			open: func(h *harness, name string) {
				h.chooseWithoutSettling(name, "Edit")
				h.tt.Frame()
				h.applyPostedWhile(func() bool { return h.g.form.loading })
			},
			state: func(g *gui) (bool, string) { return g.form.open, g.form.from.Name },
		},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newHarness(t)
			h.click("app")
			tc.open(h, tc.first)
			h.typeValue("synthetic-first")
			started, release := h.pause(tc.operation)
			h.clickWithoutSettling(tc.action)
			h.waitFor(started)
			h.clickWithoutSettling("Close")
			tc.open(h, tc.second)
			h.typeValue("synthetic-second")
			if open, entry := tc.state(h.g); !open || entry != tc.second || h.g.form.value != "synthetic-second" {
				t.Fatal("the second form was not opened and filled")
			}
			if _, ok := h.tt.Find(tc.action + " in progress"); ok {
				t.Fatal("the reopened form shows progress for the first write")
			}
			release()
			h.settle()
			if open, entry := tc.state(h.g); !open || entry != tc.second || h.g.form.value != "synthetic-second" {
				t.Fatalf("the first write changed the reopened form: open %v, entry %q", open, entry)
			}
		})
	}
}

func TestActionProgressStaysInItsButton(t *testing.T) {
	cases := []struct {
		label     string
		operation string
		prepare   func(*harness)
	}{
		{label: "Show Value", operation: "read"},
		{label: "Copy Value", operation: "read"},
		{label: "Add", operation: "add", prepare: func(h *harness) {
			h.newInApp("NEW_KEY")
			h.typeValue("synthetic-created")
		}},
		{label: "Save", operation: "edit", prepare: func(h *harness) {
			h.choose("URL", "Edit")
			h.typeValue("synthetic-replaced")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			h := newHarness(t)
			h.click("URL")
			if tc.prepare != nil {
				tc.prepare(h)
			}
			idle, ok := h.tt.Find(tc.label)
			if !ok {
				t.Fatal("the action is absent")
			}
			started, release := h.pause(tc.operation)
			h.clickWithoutSettling(tc.label)
			h.waitFor(started)
			h.tt.Frame()
			busy, ok := h.tt.Find(tc.label)
			if !ok || busy != idle {
				t.Fatalf("the pending button moved: before %v, after %v, present %v", idle, busy, ok)
			}
			spinner, ok := h.tt.Find(tc.label + " in progress")
			if !ok || spinner.W <= 0 || spinner.H <= 0 || spinner.X < busy.X || spinner.Y < busy.Y || spinner.X+spinner.W > busy.X+busy.W || spinner.Y+spinner.H > busy.Y+busy.H {
				t.Fatalf("the spinner is not inside its button: button %v, spinner %v, present %v", busy, spinner, ok)
			}
			for _, other := range cases {
				if other.label != tc.label {
					if _, ok := h.tt.Find(other.label + " in progress"); ok {
						t.Fatalf("%s also shows progress", other.label)
					}
				}
			}
			release()
			h.settle()
			if _, ok := h.tt.Find(tc.label + " in progress"); ok {
				t.Fatal("the spinner remained after the operation finished")
			}
		})
	}
}

func TestCopyFeedbackExpiresAndFollowsSelection(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	idle, ok := h.tt.Find("Copy Value")
	if !ok {
		t.Fatal("the copy action is absent")
	}
	h.click("Copy Value")
	if got := h.tt.Clipboard(); got != tokenValue || h.tt.HasText(tokenValue) {
		t.Fatalf("clipboard %q; the value shows %v", got, h.tt.HasText(tokenValue))
	}
	copied, ok := h.tt.Find("Copied")
	if !ok || copied != idle {
		t.Fatalf("the button did not acknowledge the copy in place: before %v, after %v, present %v", idle, copied, ok)
	}
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("Copied"); !ok {
		t.Fatal("the success feedback expired before two seconds")
	}
	h.tt.Advance(time.Second + time.Millisecond)
	if _, ok := h.tt.Find("Copied"); ok {
		t.Fatal("the success feedback remained after two seconds")
	}
	if restored, ok := h.tt.Find("Copy Value"); !ok || restored != idle {
		t.Fatalf("the restored button moved: before %v, after %v", idle, restored)
	}
	h.click("Copy Value")
	h.click("URL")
	if _, ok := h.tt.Find("Copied"); ok {
		t.Fatal("copy feedback for the previous entry remained")
	}
}

// The value editor refuses a CRLF paste, which it would change to LF, and
// keeps a pasted LF value byte for byte.
func TestValueEditorPaste(t *testing.T) {
	const value = "synthetic-first\nsynthetic-second\n"
	for _, action := range []string{"Add", "Save"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			account, err := "app/URL", &h.g.form.err
			if action == "Add" {
				h.newInApp("NEW_KEY")
				account = "app/NEW_KEY"
			} else {
				h.choose("URL", "Edit")
			}
			h.typeValue("synthetic-draft")
			paste := func(text string) {
				h.tt.Command("selectAll")
				h.tt.SetClipboard(text)
				h.tt.Command("paste")
			}
			paste("synthetic-first\r\nsynthetic-second")
			if h.g.form.value != "synthetic-draft" || err.field != "value" || !h.tt.HasText(err.message) {
				t.Fatalf("after a CRLF paste: draft %q, error %+v", h.g.form.value, *err)
			}
			paste(value)
			h.click(action)
			if got, ok := h.value(account); !ok || got != value {
				t.Fatalf("the pasted LF value changed: stored %q, present %v", got, ok)
			}
		})
	}
}
