package owner

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// An approval lasts Lifetime from the dialog that gave it, however often it
// is used, and a refused dialog leaves none behind.
func TestApprovalLifetime(t *testing.T) {
	var now time.Duration
	var reasons []string
	refuse := false
	approval := NewApproval(func(_ context.Context, reason string) error {
		reasons = append(reasons, reason)
		if refuse {
			return &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
		}
		return nil
	}, func() (time.Duration, bool) { return now, true }, "changes from mk")
	ctx := context.Background()
	for _, step := range []struct {
		at      time.Duration
		refuse  bool
		want    string
		dialogs int
	}{
		{0, true, "", 1},
		{0, false, Dialog, 2},
		{4 * time.Minute, false, Reused, 2},
		{Lifetime - 1, false, Reused, 2},
		{Lifetime, true, "", 3},
		{Lifetime, false, Dialog, 4},
		{2*Lifetime - 1, false, Reused, 4},
	} {
		now, refuse = step.at, step.refuse
		got, err := approval.Approve(ctx, "grant program access to ns/A")
		if got != step.want || (err != nil) != step.refuse || len(reasons) != step.dialogs {
			t.Fatalf("at %v: %q, %v after %d dialogs; want %q after %d", step.at, got, err, len(reasons), step.want, step.dialogs)
		}
	}
	if want := "grant program access to ns/A. It will not ask again for changes from mk in the next 5 minutes"; reasons[0] != want {
		t.Fatalf("reason %q, want %q", reasons[0], want)
	}
	// While the clock fails, no approval is given or reused.
	dialogs, clockOK := 0, false
	approval = NewApproval(func(context.Context, string) error { dialogs++; return nil }, func() (time.Duration, bool) { return 0, clockOK }, "")
	for i, ok := range []bool{false, true, false} {
		clockOK = ok
		if got, err := approval.Approve(ctx, "grant program access to ns/A"); got != Dialog || err != nil || dialogs != i+1 {
			t.Fatalf("change %d, clock %v: %q, %v after %d dialogs", i+1, ok, got, err, dialogs)
		}
	}
}

// Requests that arrive while a dialog is up wait for it and share the
// approval it gives; one whose context ends first gives up.
func TestApprovalSharesOneDialog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var dialogs atomic.Int32
		answer := make(chan struct{})
		approval := NewApproval(func(context.Context, string) error {
			dialogs.Add(1)
			<-answer
			return nil
		}, func() (time.Duration, bool) { return 0, true }, "")
		ctx := context.Background()
		results := make(chan string, 3)
		var wg sync.WaitGroup
		approve := func() {
			wg.Go(func() {
				auth, _ := approval.Approve(ctx, "grant program access to ns/A")
				results <- auth
			})
		}
		approve()
		synctest.Wait()
		expired, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := approval.Approve(expired, "abandoned"); !store.IsKind(err, store.KindCancelled) {
			t.Fatalf("waiting with an ended context: %v, want cancelled", err)
		}
		approve()
		approve()
		// Both wait at the gate while the dialog is up.
		synctest.Wait()
		close(answer)
		wg.Wait()
		close(results)
		counts := map[string]int{}
		for auth := range results {
			counts[auth]++
		}
		if counts[Dialog] != 1 || counts[Reused] != 2 || dialogs.Load() != 1 {
			t.Fatalf("approvals %v after %d dialogs, want one dialog shared", counts, dialogs.Load())
		}
	})
}
