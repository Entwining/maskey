package bitwarden

import (
	"context"
	"errors"

	"github.com/Entwining/maskey/internal/store"
)

// Turn lets one request at a time make the exchanges with Bitwarden that it
// guards. Unlike a mutex, it is waited for only as long as the request's
// context allows, so that a request whose client gave up, or whose deadline
// passed behind a slow exchange, ends instead of holding its goroutine and
// connection.
type Turn struct{ held chan struct{} }

func NewTurn() *Turn { return &Turn{held: make(chan struct{}, 1)} }

// Take waits for the turn until ctx ends. A request that did not get it has
// done nothing, and its error has the kind of a request to Bitwarden that
// timed out or was cancelled.
func (t *Turn) Take(ctx context.Context) error {
	_, err := t.TakeUnless(ctx, nil)
	return err
}

// TakeUnless is Take that also stops waiting, without the turn or an error,
// once done is closed.
func (t *Turn) TakeUnless(ctx context.Context, done <-chan struct{}) (took bool, err error) {
	select {
	case t.held <- struct{}{}:
		return true, nil
	case <-done:
		return false, nil
	case <-ctx.Done():
		kind := store.KindNetwork
		if errors.Is(ctx.Err(), context.Canceled) {
			kind = store.KindCancelled
		}
		return false, &store.Error{Kind: kind, Message: "the store was busy with an earlier request; nothing was done"}
	}
}

// Release ends the turn that Take gave.
func (t *Turn) Release() { <-t.held }
