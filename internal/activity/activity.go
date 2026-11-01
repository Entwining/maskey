// Package activity keeps this Mac's record of Maskey's Keychain operations:
// which program got, read, wrote, or changed program access to which entry,
// and with what result. The record never holds a value, anything derived
// from one, notes, arguments, or the environment.
//
// Each record is one JSON line appended to a file per UTC month, which
// Maskey never rewrites. A month's first record removes the files of months
// more than twelve before it.
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Version is the record format. It changes only for a change readers cannot
// ignore; a new field does not change it.
const Version = 1

type Record struct {
	V    int    `json:"v"`
	Time string `json:"time"`
	// Op is get, read, add, update, edit, delete, grant, or revoke.
	Op string `json:"op"`
	// Entry is the <namespace>/<NAME> operated on; for an edit that renames
	// it, the reference before, and To the reference after.
	Entry string `json:"entry"`
	To    string `json:"to,omitempty"`
	// Result is ok or the error kind the operation ended with.
	Result string `json:"result"`
	// Via is what a read was for: run, reveal, copy, or edit.
	Via string `json:"via,omitempty"`
	// Auth is how the owner approved a grant or revoke that succeeded:
	// dialog or reused.
	Auth   string `json:"auth,omitempty"`
	Caller Caller `json:"caller"`
}

// Caller is the program an operation ran for, as the kernel described it
// before the operation.
type Caller struct {
	// Exe is the executable path of the program that started the acting
	// mk, or Maskey's own for its window; empty when the kernel no longer
	// knew it.
	Exe string `json:"exe"`
	// Chain lists the executable paths of Exe's ancestors, nearest first,
	// stopping before launchd; a name stands in for a path the kernel
	// withheld.
	Chain []string `json:"chain"`
	// TTY is the acting process's controlling terminal.
	TTY string `json:"tty,omitempty"`
}

// timeLayout is RFC 3339 in UTC with milliseconds.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

type contextKey int

const (
	viaKey contextKey = iota
	authKey
	callerKey
	failureKey
)

// WithVia tags the reads made with ctx with what they are for.
func WithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey, via)
}

// ViaOf is the tag WithVia gave ctx, if any.
func ViaOf(ctx context.Context) string {
	via, _ := ctx.Value(viaKey).(string)
	return via
}

// WithAuth tags the grants and revokes made with ctx with how the owner
// approved them.
func WithAuth(ctx context.Context, auth string) context.Context {
	return context.WithValue(ctx, authKey, auth)
}

// AuthOf is the approval WithAuth gave ctx, if any.
func AuthOf(ctx context.Context) string {
	auth, _ := ctx.Value(authKey).(string)
	return auth
}

// WithCaller attributes the records made with ctx to c.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey, c)
}

// CallerOf is the caller WithCaller gave ctx, if any.
func CallerOf(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey).(Caller)
	return c
}

// WithFailureReport sends the failures to record the records made with ctx
// to report, in place of the log's own report.
func WithFailureReport(ctx context.Context, report func(error)) context.Context {
	return context.WithValue(ctx, failureKey, report)
}

// Recorder keeps the activity record.
type Recorder interface {
	Record(ctx context.Context, records ...Record)
}

// Log appends records to the files in its directory.
type Log struct {
	dir string
	now func() time.Time

	mu   sync.Mutex
	warn func(error)
	once sync.Once
}

// Dir is where this Mac's record is kept.
func Dir(service string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", service), nil
}

// New returns the log in dir. A failure to record is reported once, to
// warn, which OnFailure can replace.
func New(dir string, warn func(error)) *Log {
	return &Log{dir: dir, now: time.Now, warn: warn}
}

// Writable reports whether this process may write the record's directory,
// or create it in its parent. It asks access(2), which a sandbox that denies
// the writes answers in advance, instead of opening the month's file, which
// would create it and make the next record skip the prune. Any other error
// counts as not writable, the safe guess for a caller that then has the
// record written elsewhere.
func (l *Log) Writable() bool {
	err := unix.Access(l.dir, unix.W_OK)
	if errors.Is(err, unix.ENOENT) {
		err = unix.Access(filepath.Dir(l.dir), unix.W_OK)
	}
	return err == nil
}

// OnFailure makes warn the receiver of the report of a failure to record.
func (l *Log) OnFailure(warn func(error)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warn = warn
}

// Record completes the records of one operation with the time and the
// caller and tags of ctx, and appends them. A failure to record never changes
// the operation's result; it goes to ctx's failure report, or to
// ReportFailure.
func (l *Log) Record(ctx context.Context, records ...Record) {
	now := l.now().UTC()
	caller := CallerOf(ctx)
	if caller.Chain == nil {
		caller.Chain = []string{}
	}
	for _, r := range records {
		r.V, r.Time, r.Caller = Version, now.Format(timeLayout), caller
		if r.Op == "read" {
			r.Via = ViaOf(ctx)
		}
		if (r.Op == "grant" || r.Op == "revoke") && r.Result == "ok" {
			r.Auth = AuthOf(ctx)
		}
		if err := l.append(now, r); err != nil {
			if report, ok := ctx.Value(failureKey).(func(error)); ok {
				report(err)
			} else {
				l.ReportFailure(err)
			}
		}
	}
}

// ReportFailure reports err, a failure to record here or in another process
// for this one, unless one was reported before.
func (l *Log) ReportFailure(err error) {
	l.once.Do(func() {
		l.mu.Lock()
		warn := l.warn
		l.mu.Unlock()
		if warn != nil {
			warn(err)
		}
	})
}

// append writes r and, after a month's first record, prunes. Its error says
// which of the two failed, since only the first loses the record.
func (l *Log) append(now time.Time, r Record) error {
	created, err := l.write(now, r)
	if err != nil {
		return fmt.Errorf("an operation was not recorded: %w", err)
	}
	if created {
		if err := l.prune(now); err != nil {
			return fmt.Errorf("older activity files were not removed: %w", err)
		}
	}
	return nil
}

// write writes r to its month's file in a single write, which O_APPEND
// keeps whole beside other processes' records, and reports whether it
// created the file. The file is opened for each record: the month can
// change between records, and a descriptor kept across a prune would keep
// writing to a removed file.
func (l *Log) write(now time.Time, r Record) (created bool, err error) {
	line, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	line = append(line, '\n')
	if err := os.MkdirAll(l.dir, 0700); err != nil {
		return false, err
	}
	path := filepath.Join(l.dir, fileName(now))
	const flags = unix.O_WRONLY | unix.O_APPEND | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(path, flags|unix.O_EXCL, 0600)
	created = err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, flags, 0600)
	}
	if err != nil {
		return false, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	if err := setAppendOnly(fd); err != nil {
		return false, &os.PathError{Op: "chflags", Path: path, Err: err}
	}
	// os.File.Write would continue a short write in another call, which
	// another process's record could split.
	n, err := unix.Write(fd, line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return false, &os.PathError{Op: "write", Path: path, Err: err}
	}
	return created, nil
}

// keptMonths is how many months before the current one keep their file.
const keptMonths = 12

// prune removes the files of months more than keptMonths before now's.
// Only the process that created the month's file runs it, so it runs about
// once a month.
func (l *Log) prune(now time.Time) error {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return err
	}
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	cutoff := current.AddDate(0, -keptMonths, 0)
	var errs []error
	for _, entry := range entries {
		month, ok := monthOf(entry.Name())
		if !ok || !month.Before(cutoff) {
			continue
		}
		path := filepath.Join(l.dir, entry.Name())
		if err := clearAppendOnly(path); err != nil {
			errs = append(errs, &os.PathError{Op: "chflags", Path: path, Err: err})
			continue
		}
		errs = append(errs, os.Remove(path))
	}
	return errors.Join(errs...)
}

const fileLayout = "activity-2006-01.jsonl"

func fileName(month time.Time) string { return month.Format(fileLayout) }

func monthOf(name string) (time.Time, bool) {
	month, err := time.Parse(fileLayout, name)
	return month, err == nil
}
