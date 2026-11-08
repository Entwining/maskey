package main

import (
	"errors"
	"testing"
	"time"
)

type fakeClipboard struct {
	text   string
	change int
	err    error
}

func (f *fakeClipboard) write(value []byte) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.replace(string(value))
	return f.change, nil
}

func (f *fakeClipboard) clearIf(change int) {
	if change == f.change {
		f.replace("")
	}
}

// replace puts text on the clipboard, as a copy in any app does.
func (f *fakeClipboard) replace(text string) { f.text, f.change = text, f.change+1 }

type scheduledClear struct {
	delay time.Duration
	run   func()
}

// A copy's clear removes that copy, and leaves alone whatever was copied
// after it, in another app or in the window.
func TestCopyClearsOnlyItself(t *testing.T) {
	h := newHarness(t)
	h.choose("API_TOKEN", "Copy Value")
	if len(h.clears) != 1 || h.clears[0].delay != clipboardClearDelay {
		t.Fatalf("clears asked for: %d", len(h.clears))
	}
	h.clears[0].run()
	if h.clipboard.text != "" {
		t.Fatal("the copy stayed on the clipboard past its delay")
	}

	h.choose("API_TOKEN", "Copy Value")
	h.clipboard.replace("synthetic-other-app")
	h.clears[1].run()
	if h.clipboard.text != "synthetic-other-app" {
		t.Fatalf("the clear removed another app's copy, leaving %q", h.clipboard.text)
	}

	h.choose("API_TOKEN", "Copy Value")
	h.choose("URL", "Copy Value")
	h.clears[2].run()
	if h.clipboard.text != urlValue {
		t.Fatalf("the earlier copy's clear removed the later copy, leaving %q", h.clipboard.text)
	}
}

// Quitting clears the latest copy, and before any copy changes nothing.
func TestQuitClearsTheLatestCopy(t *testing.T) {
	h := newHarness(t)
	h.g.clearCopy()
	if h.clipboard.change != 0 {
		t.Fatal("quitting before any copy changed the clipboard")
	}
	h.choose("API_TOKEN", "Copy Value")
	h.choose("URL", "Copy Value")
	h.g.clearCopy()
	if h.clipboard.text != "" {
		t.Fatalf("quitting left %q on the clipboard", h.clipboard.text)
	}
}

// A copy that fails says so, and neither confirms the copy nor clears the
// clipboard later.
func TestFailedCopy(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	h.clipboard.err = errors.New("synthetic clipboard failure")
	h.click("Copy Value")
	if a := h.alert(); a.message != "Couldn’t Copy “API_TOKEN”" || a.detail != "Synthetic clipboard failure." {
		t.Fatalf("alert %q: %q", a.message, a.detail)
	}
	if _, ok := h.tt.Find("Copied"); ok || h.g.copied {
		t.Fatal("the window confirmed a copy that failed")
	}
	if len(h.clears) != 0 {
		t.Fatalf("a failed copy asked for %d clears", len(h.clears))
	}
	h.answer("OK")
}
