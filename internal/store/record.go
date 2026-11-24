package store

import (
	"context"

	"github.com/Entwining/maskey/internal/activity"
)

// Operation is one operation on entries, as the activity record keeps it:
// Account for an operation on one entry, To when an edit renames it, and
// Accounts for a read.
type Operation struct {
	Op       string
	Account  string
	To       string
	Accounts []string
}

type contextKey int

const (
	fallbackKey contextKey = iota
	replacingKey
	savedKey
)

// WithFallback marks the operations made with ctx as tried here first: one
// that ends unavailable reached no entry and is performed elsewhere, where
// its outcome is recorded.
func WithFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, fallbackKey, true)
}

// WithReplacing marks an add made with ctx as the first half of a
// replacement: when the entry exists, an update follows, which is the one
// outcome to record.
func WithReplacing(ctx context.Context) context.Context {
	return context.WithValue(ctx, replacingKey, true)
}

// Replacing reports whether ctx is WithReplacing's.
func Replacing(ctx context.Context) bool {
	replacing, _ := ctx.Value(replacingKey).(bool)
	return replacing
}

// WithSaved marks a listing made with ctx as one to answer from the copy a
// remote store keeps on this Mac, without asking the remote, so that a
// window has entries to show while the current ones are read. A store with
// no copy answers KindNotFound.
func WithSaved(ctx context.Context) context.Context {
	return context.WithValue(ctx, savedKey, true)
}

// Saved reports whether ctx is WithSaved's.
func Saved(ctx context.Context) bool {
	saved, _ := ctx.Value(savedKey).(bool)
	return saved
}

// RecordFailure enters in the activity record an operation that ended with
// err before it reached a backend.
func RecordFailure(ctx context.Context, recorder activity.Recorder, op Operation, err error) {
	record(ctx, recorder, op, nil, err)
}

// record enters op and how it ended in the activity record; found holds the
// accounts a read returned.
func record(ctx context.Context, recorder activity.Recorder, op Operation, found func(account string) bool, err error) {
	outcome := "ok"
	if err != nil {
		outcome = string(KindOf(err))
	}
	fallback, _ := ctx.Value(fallbackKey).(bool)
	switch {
	case fallback && IsKind(err, KindUnavailable):
		return
	case op.Op == "add" && IsKind(err, KindExists) && Replacing(ctx):
		return
	case op.Op == "read":
		records := make([]activity.Record, len(op.Accounts))
		for i, account := range op.Accounts {
			outcome := outcome
			if err == nil && !found(account) {
				outcome = string(KindNotFound)
			}
			records[i] = activity.Record{Op: op.Op, Entry: account, Result: outcome}
		}
		recorder.Record(ctx, records...)
		return
	}
	r := activity.Record{Op: op.Op, Entry: op.Account, Result: outcome}
	if op.Op == "edit" && op.To != op.Account {
		r.To = op.To
	}
	recorder.Record(ctx, r)
}

// Recorded returns backend with each operation that reads or changes an
// entry entered in the activity record as it ends. The process that
// performs an operation wraps its backend so; a backend that forwards the
// operation to another process is not wrapped, since that process records
// it.
func Recorded(backend Backend, recorder activity.Recorder) Backend {
	return &recorded{backend, recorder}
}

// recorded names its backend instead of embedding it, so that a new Backend
// method does not compile until it is decided here whether it is recorded.
type recorded struct {
	backend  Backend
	recorder activity.Recorder
}

// Items is not recorded: it lists entries without their values.
func (b *recorded) Items(ctx context.Context) ([]Item, error) { return b.backend.Items(ctx) }

func (b *recorded) Read(ctx context.Context, accounts []string) (map[string]Stored, error) {
	values, err := b.backend.Read(ctx, accounts)
	record(ctx, b.recorder, Operation{Op: "read", Accounts: accounts}, func(account string) bool {
		_, ok := values[account]
		return ok
	}, err)
	return values, err
}

func (b *recorded) Get(ctx context.Context, account string) ([]byte, error) {
	value, err := b.backend.Get(ctx, account)
	record(ctx, b.recorder, Operation{Op: "get", Account: account}, nil, err)
	return value, err
}

func (b *recorded) Add(ctx context.Context, account string, value []byte, details Details) error {
	err := b.backend.Add(ctx, account, value, details)
	record(ctx, b.recorder, Operation{Op: "add", Account: account}, nil, err)
	return err
}

func (b *recorded) Update(ctx context.Context, account string, value []byte) error {
	err := b.backend.Update(ctx, account, value)
	record(ctx, b.recorder, Operation{Op: "update", Account: account}, nil, err)
	return err
}

func (b *recorded) Edit(ctx context.Context, account, to string, value []byte, details *Details) error {
	err := b.backend.Edit(ctx, account, to, value, details)
	record(ctx, b.recorder, Operation{Op: "edit", Account: account, To: to}, nil, err)
	return err
}

func (b *recorded) SetProgramAccess(ctx context.Context, account string, granted bool) error {
	err := b.backend.SetProgramAccess(ctx, account, granted)
	op := "revoke"
	if granted {
		op = "grant"
	}
	record(ctx, b.recorder, Operation{Op: op, Account: account}, nil, err)
	return err
}

func (b *recorded) Delete(ctx context.Context, account string) error {
	err := b.backend.Delete(ctx, account)
	record(ctx, b.recorder, Operation{Op: "delete", Account: account}, nil, err)
	return err
}
