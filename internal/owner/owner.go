// Package owner asks the Mac's owner to approve changes to program access,
// and keeps an approval for a few minutes so that several changes need one
// dialog.
package owner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// Authenticate asks the owner to authenticate, as LocalAuthentication's
// deviceOwnerAuthentication policy does: with the login password, Touch ID,
// or an Apple Watch. reason completes macOS's sentence "<app> is trying to
// <reason>." It fails with KindCancelled when the owner does not
// authenticate and KindPrerequisite when this Mac cannot ask.
type Authenticate func(ctx context.Context, reason string) error

// Lifetime is how long an approval lasts from the authentication that
// gave it. Using it does not extend it.
const Lifetime = 5 * time.Minute

// DialogWait bounds the wait for another dialog to end and for the owner to
// answer this one.
const DialogWait = 2 * time.Minute

// How an approval was obtained, as the activity record states it.
const (
	Dialog = "dialog"
	Reused = "reused"
)

// Approval is the owner's approval of changes to program access, held in
// memory only. A process keeps one for every change it makes.
type Approval struct {
	authenticate Authenticate
	scope        string
	// clock keeps counting while the Mac sleeps, so that an approval does
	// not outlast its lifetime across sleep. Without a clock, no approval
	// is reused.
	clock func() (time.Duration, bool)
	// gate lets one dialog show at a time; requests waiting for it share
	// the approval the dialog gives.
	gate chan struct{}

	mu sync.Mutex
	// until is when the approval ends; at zero, before any, none holds.
	until time.Duration
}

// NewApproval returns an approval for the changes scope names, which
// completes the dialog's sentence "It will not ask again for <scope> in the
// next 5 minutes."
func NewApproval(authenticate Authenticate, clock func() (time.Duration, bool), scope string) *Approval {
	return &Approval{authenticate: authenticate, clock: clock, scope: scope, gate: make(chan struct{}, 1)}
}

// Approve returns Reused when an approval holds, and otherwise asks the
// owner, giving reason, and returns Dialog once the owner authenticates.
func (a *Approval) Approve(ctx context.Context, reason string) (string, error) {
	if a.holds() {
		return Reused, nil
	}
	ctx, cancel := context.WithTimeout(ctx, DialogWait)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", &store.Error{Kind: store.KindCancelled, Message: "no authentication within two minutes"}
		}
		return "", &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
	}
	defer func() { <-a.gate }()
	if a.holds() {
		return Reused, nil
	}
	if err := a.authenticate(ctx, reason+". It will not ask again for "+a.scope+" in the next 5 minutes"); err != nil {
		return "", err
	}
	if now, ok := a.clock(); ok {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.until = now + Lifetime
	}
	return Dialog, nil
}

func (a *Approval) holds() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now, ok := a.clock()
	return ok && now < a.until
}
