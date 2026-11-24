package main

import (
	"context"
	"sync"
	"testing"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/store/storetest"
	"github.com/Entwining/maskey/internal/stores"
)

// savedCopy is a remote store's backend that keeps a copy of its listing on
// this Mac: asked for the copy it answers at once, and otherwise only after
// release.
type savedCopy struct {
	*storetest.Backend
	copy    []store.Item
	asked   chan struct{}
	release chan struct{}
}

func (b *savedCopy) Items(ctx context.Context) ([]store.Item, error) {
	if store.Saved(ctx) {
		if b.copy == nil {
			return nil, &store.Error{Kind: store.KindNotFound, Message: "no saved copy"}
		}
		return b.copy, nil
	}
	close(b.asked)
	<-b.release
	return b.Backend.Items(ctx)
}

// A remote store shows the copy saved on this Mac while its remote is still
// asked, then what the remote answers; without a copy it shows nothing, and
// no error, until then.
func TestRemoteStoreShowsItsSavedCopyFirst(t *testing.T) {
	for _, copied := range []bool{true, false} {
		h := newHarness(t)
		r := h.addRemote()
		b := &savedCopy{Backend: r.backend, asked: make(chan struct{}), release: make(chan struct{})}
		t.Cleanup(sync.OnceFunc(func() { close(b.release) }))
		if copied {
			b.copy = []store.Item{{Account: "deploy/SAVED_KEY"}}
		}
		h.g.remoteStore = func(stores.Connection) storeAccess { return storeAccess{store: store.New(b, new(storetest.Recorder))} }
		h.g.refresh()
		h.applyPostedUntil(b.asked)
		h.tt.Frame()
		if h.g.has(work("deploy", "SAVED_KEY")) != copied || h.g.has(work("deploy", "SSH_HOST")) || h.g.listed["work"].err != "" {
			t.Fatalf("copy %v, while the remote is asked: %+v", copied, h.g.listed["work"])
		}
		b.release <- struct{}{}
		h.settle()
		if h.g.has(work("deploy", "SAVED_KEY")) || !h.g.has(work("deploy", "SSH_HOST")) {
			t.Fatalf("copy %v, after the remote answered: %+v", copied, h.g.listed["work"])
		}
	}
}
