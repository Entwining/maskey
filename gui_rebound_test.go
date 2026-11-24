package main

import (
	"context"
	"slices"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// rebind gives the window's Mac the store work connected to the project
// first, whose entries addRemote made, and returns what connects the name
// to the project second instead, with that project's entries and the
// projects the window opened the store for, in order.
func (h *harness) rebind() (r *remote, second *storetest.Backend, connectAgain func(), opened *[]string) {
	h.t.Helper()
	r = h.addRemote()
	r.config.Stores[0].Project = "first"
	second = storetest.New()
	second.Put("app/API_TOKEN", []byte("synthetic-second-0a6f"))
	opened = new([]string)
	h.g.remoteStore = func(connection stores.Connection) storeAccess {
		*opened = append(*opened, connection.Project)
		backend := r.backend
		if connection.Project == "second" {
			backend = second
		}
		return storeAccess{store.New(backend, new(storetest.Recorder)), func(ctx context.Context, ref store.Ref, granted bool) error {
			return backend.SetProgramAccess(ctx, ref.String(), "", granted)
		}}
	}
	h.reload()
	return r, second, func() {
		h.t.Helper()
		r.config.Stores = []stores.Connection{{ID: "work", Type: stores.Bitwarden, Region: "eu", Project: "second", ProjectName: "Other"}}
		h.reload()
	}, opened
}

// A store removed and connected again under its name is another store: the
// window keeps nothing it held of the first, neither the entry chosen, nor
// a value shown, nor the form open on one.
func TestStoreConnectedAgainLeavesNothingHeld(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inside func(h *harness)
	}{
		{"an entry shown", func(h *harness) {
			h.clickScope(scope{"work", "app"})
			h.clickRow(work("app", "API_TOKEN"))
			h.click("Show Value")
		}},
		{"an entry being edited", func(h *harness) { h.chooseRow(work("app", "API_TOKEN"), "Edit") }},
		{"a new entry for it", func(h *harness) {
			h.clickScope(scope{"work", "deploy"})
			h.click("New Entry")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, _, connectAgain, opened := h.rebind()
			tc.inside(h)
			connectAgain()
			if h.g.scope != (scope{}) || h.g.entry != (entryID{}) || h.g.form.open || h.g.revealed != "" {
				t.Fatalf("scope %v, entry %v, form open %v, a value shown %v", h.g.scope, h.g.entry, h.g.form.open, h.g.revealed != "")
			}
			// The window shows the project connected now, and opens it.
			if !h.g.has(work("app", "API_TOKEN")) || h.g.has(work("deploy", "SSH_HOST")) {
				t.Fatalf("the store lists %v", h.g.listed["work"].refs)
			}
			if last := (*opened)[len(*opened)-1]; last != "second" || !slices.Contains(*opened, "first") {
				t.Fatalf("the window opened the store for %q", *opened)
			}
		})
	}
}

// The answer to Delete… is for the entry of the connection it was asked
// of: once the store's name means another project, the entry of the same
// name there is not deleted.
func TestDeleteAnswersForTheConnectionAsked(t *testing.T) {
	h := newHarness(t)
	r, second, connectAgain, _ := h.rebind()
	h.chooseRow(work("app", "API_TOKEN"), "Delete…")
	connectAgain()
	h.answer("Delete")
	_, inFirst := r.value("app/API_TOKEN")
	values, _ := second.Read(context.Background(), []string{"app/API_TOKEN"})
	if _, inSecond := values["app/API_TOKEN"]; !inFirst || !inSecond || len(h.openAlerts()) != 0 {
		t.Fatalf("in the first project %v, in the second %v, alerts %d", inFirst, inSecond, len(h.openAlerts()))
	}
}

// A store the window no longer knows is still opened under its name, which
// the service refuses, not under none, which would mean iCloud Keychain.
func TestOpenNamesTheStore(t *testing.T) {
	h := newHarness(t)
	var got stores.Connection
	h.g.remoteStore = func(connection stores.Connection) storeAccess {
		got = connection
		return storeAccess{}
	}
	h.g.open("gone")
	if got.ID != "gone" || got.Target() == (stores.Connection{ID: stores.ICloud, Type: stores.ICloud}).Target() {
		t.Fatalf("opened %+v", got)
	}
}

// A change to program access that ends after the store's name came to mean
// another project marks nothing in what the window shows of that project.
func TestAccessChangeEndsAfterTheStoreWasConnectedAgain(t *testing.T) {
	h := newHarness(t)
	r, second, _, _ := h.rebind()
	h.clickRow(work("app", "API_TOKEN"))
	started, granted, listed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.backend.Before = func(op, _ string) error {
		if op == "grant" {
			close(started)
			<-granted
		}
		return nil
	}
	h.clickWithoutSettling("Program access")
	h.waitFor(started)
	// The window has read the stores again, and not yet the project's
	// entries, when the change ends.
	second.Before = func(op, _ string) error {
		if op == "items" {
			<-listed
		}
		return nil
	}
	r.config.Stores = []stores.Connection{{ID: "work", Type: stores.Bitwarden, Region: "eu", Project: "second", ProjectName: "Other"}}
	h.g.refresh()
	h.applyPostedWhile(func() bool { return h.g.stores.Stores[0].Project != "second" })
	close(granted)
	h.applyPostedWhile(func() bool { return h.g.write != nil })
	close(listed)
	h.settle()
	if h.g.attributes(work("app", "API_TOKEN")).ProgramAccess || len(h.openAlerts()) != 0 {
		t.Fatalf("the second project's entry shows program access %v, alerts %d", h.g.attributes(work("app", "API_TOKEN")).ProgramAccess, len(h.openAlerts()))
	}
}
