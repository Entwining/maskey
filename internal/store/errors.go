package store

import (
	"errors"
	"fmt"
)

// Kind classifies a failure by what the caller can do about it.
type Kind string

const (
	KindInvalid  Kind = "invalid"   // the input breaks an identifier or value rule
	KindNotFound Kind = "not_found" // the entry or namespace does not exist
	KindExists   Kind = "exists"    // the entry already exists
	KindConflict Kind = "conflict"  // another writer changed the entry, or the selection is ambiguous
	// KindPrerequisite means something mk needs is missing or could not be
	// set up, such as the Command Line Tools that host the Keychain helper.
	KindPrerequisite Kind = "prerequisite"
	// KindLocked means the Keychain refused access because the Mac has not
	// been unlocked since it started.
	KindLocked Kind = "locked"
	// KindUnavailable means this process cannot reach the Keychain, as
	// inside a sandbox that blocks Apple's Keychain daemon.
	KindUnavailable Kind = "unavailable"
	KindKeychain    Kind = "keychain" // the Keychain returned another error
	// KindUncertain means a write may or may not have happened; read the
	// entry again before deciding whether to repeat it.
	KindUncertain Kind = "uncertain"
	KindCancelled Kind = "cancelled"
	// KindNotGranted means the entry lacks program access required by mk get.
	KindNotGranted Kind = "not_granted"
)

// Error is a failure of a store operation. It never carries a value. Ref
// names the entry only when the store found it; otherwise Argument, when
// set, is the 1-based position of the input the failure concerns.
type Error struct {
	Kind     Kind   `json:"kind"`
	Message  string `json:"message"`
	Ref      *Ref   `json:"entry,omitempty"`
	Argument int    `json:"argument,omitempty"`
	OSStatus int32  `json:"os_status,omitempty"`
}

func (e *Error) Error() string {
	message := e.Message
	switch {
	case e.Ref != nil:
		message = e.Ref.String() + ": " + message
	case e.Argument > 0:
		message = fmt.Sprintf("argument %d: %s", e.Argument, message)
	}
	if e.OSStatus != 0 {
		message = fmt.Sprintf("%s (OSStatus %d)", message, e.OSStatus)
	}
	return message
}

// KindOf returns the kind of err, or "" when err is nil or not an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

func IsKind(err error, kind Kind) bool { return err != nil && KindOf(err) == kind }
