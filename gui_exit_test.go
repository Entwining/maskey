package main

import (
	"slices"
	"sync"
	"testing"

	"github.com/Entwining/maskey/internal/store"
)

// exits stands in for MyGo's close and quit: each asks the window's handler
// first, as the native events do, and closes the window unless it prevents
// that. quit reports whether the app quit.
func (h *harness) exits() (quit *bool) {
	quit = new(bool)
	h.g.closeWindow = func() {
		if !h.g.beforeExit(closeExit) {
			h.g.close()
		}
	}
	h.g.quitApp = func() {
		if !h.g.beforeExit(quitExit) && !h.g.beforeExit(closeExit) {
			h.g.close()
			*quit = true
		}
	}
	return quit
}

// The prompt's Save before the window closes, or the app quits, starts
// nothing once the window has closed; quitting then quits the app, which
// another window, such as the updater's, would otherwise keep running.
func TestSaveBeforeClosing(t *testing.T) {
	for _, leaving := range []exit{closeExit, quitExit} {
		t.Run(map[exit]string{closeExit: "closing the window", quitExit: "quitting"}[leaving], func(t *testing.T) {
			h := newHarness(t)
			quit := h.exits()
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			var mu sync.Mutex
			var ops []string
			h.backend.Before = func(op, _ string) error {
				mu.Lock()
				defer mu.Unlock()
				ops = append(ops, op)
				return nil
			}
			if !h.g.beforeExit(leaving) {
				t.Fatal("leaving a changed form asked nothing")
			}
			h.settle()
			h.enter() // Save
			mu.Lock()
			started := slices.Clone(ops)
			mu.Unlock()
			if v, _ := h.value("app/URL"); v != "synthetic-url-new" || !h.g.closed || !slices.Equal(started, []string{"edit"}) {
				t.Fatalf("stored %q; closed %v; backend operations %q", v, h.g.closed, started)
			}
			if *quit != (leaving == quitExit) {
				t.Fatalf("quit %v", *quit)
			}
		})
	}
}

// Closing the window or quitting while a write runs waits for the write,
// whose outcome ending the process would leave unrecorded and unreported,
// and then goes ahead. A close asked for after a quit still quits.
func TestExitWaitsForWrite(t *testing.T) {
	writes := []struct {
		name    string
		start   func(h *harness) (release func())
		applied func(h *harness) bool
	}{
		{"Save", func(h *harness) func() {
			h.choose("URL", "Edit")
			h.typeValue("synthetic-url-new")
			started, release := h.pause("edit")
			h.clickWithoutSettling("Save")
			h.waitFor(started)
			return release
		}, func(h *harness) bool { v, _ := h.value("app/URL"); return v == "synthetic-url-new" }},
		{"Delete", func(h *harness) func() {
			h.choose("URL", "Delete…")
			started, release := h.pause("delete")
			h.answer("Delete")
			h.waitFor(started)
			return release
		}, func(h *harness) bool { _, ok := h.value("app/URL"); return !ok }},
		{"Program access", func(h *harness) func() {
			h.click("URL")
			started, release := h.pause("grant")
			h.clickWithoutSettling("Program access")
			h.waitFor(started)
			return release
		}, func(h *harness) bool { return h.attributes("app/URL").ProgramAccess }},
	}
	for _, w := range writes {
		for _, leaving := range []exit{closeExit, quitExit} {
			t.Run(w.name+map[exit]string{closeExit: ", closing", quitExit: ", quitting"}[leaving], func(t *testing.T) {
				h := newHarness(t)
				quit := h.exits()
				release := w.start(h)
				if !h.g.beforeExit(leaving) || !h.g.beforeExit(closeExit) || h.g.closed {
					t.Fatalf("the exit went ahead during the write; closed %v", h.g.closed)
				}
				release()
				h.settle()
				if !w.applied(h) || !h.g.closed || *quit != (leaving == quitExit) {
					t.Fatalf("applied %v, closed %v, quit %v", w.applied(h), h.g.closed, *quit)
				}
			})
		}
	}
}

// With the window closed and another, such as the updater's, still open,
// Install Command Line Tool… in the menu bar starts nothing, and quitting
// does not wait for it.
func TestQuitAfterWindowClosed(t *testing.T) {
	h := newHarness(t)
	quit := h.exits()
	h.g.closeWindow()
	h.g.onMenu(h.g.installCLI)(nil, nil)
	(<-h.posted)()
	h.g.quitApp()
	if !*quit {
		t.Fatal("the quit waits for an installation that never started")
	}
}

// A write that fails during a held quit cancels it: the window stays to
// show the failure, and answering the alert does not quit either.
func TestFailedWriteCancelsExit(t *testing.T) {
	h := newHarness(t)
	quit := h.exits()
	h.choose("URL", "Delete…")
	started, release := h.pause("delete")
	held := h.backend.Before
	h.backend.Before = func(op, account string) error {
		_ = held(op, account)
		if op == "delete" {
			return &store.Error{Kind: store.KindUncertain, Message: "synthetic"}
		}
		return nil
	}
	h.answer("Delete")
	h.waitFor(started)
	h.g.beforeExit(quitExit)
	release()
	h.settle()
	if title := h.alert().message; title != "Couldn’t Confirm That “URL” Was Deleted" || h.g.closed {
		t.Fatalf("alert %q, closed %v", title, h.g.closed)
	}
	h.answer("OK")
	if h.g.closed || *quit {
		t.Fatalf("after the alert: closed %v, quit %v", h.g.closed, *quit)
	}
}

// An answer to Delete… that arrives while another write runs deletes
// nothing: the question was asked before that write started.
func TestDeleteAnswerDuringWrite(t *testing.T) {
	h := newHarness(t)
	h.choose("URL", "Edit")
	h.typeValue("synthetic-url-new")
	h.choose("PASSWORD", "Delete…")
	started, release := h.pause("edit")
	h.clickWithoutSettling("Save")
	h.waitFor(started)
	h.answer("Delete")
	release()
	h.settle()
	v, _ := h.value("app/URL")
	if _, kept := h.value("db/PASSWORD"); !kept || v != "synthetic-url-new" {
		t.Fatalf("PASSWORD kept %v, URL %q", kept, v)
	}
}
