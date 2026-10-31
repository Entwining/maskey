//go:build darwin && arm64

package owner

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"

	"github.com/Entwining/maskey/internal/store"
)

// Clock is the time since boot, sleep included: CLOCK_MONOTONIC_RAW keeps
// counting while the Mac sleeps, unlike the clock Go's time.Since reads. It
// is false when the clock cannot be read.
func Clock() (time.Duration, bool) {
	var now unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &now) != nil {
		return 0, false
	}
	return time.Duration(now.Nano()), true
}

const (
	policyDeviceOwnerAuthentication = 2
	laErrorDomain                   = "com.apple.LocalAuthentication"
	laErrorAuthenticationFailed     = -1
	laErrorUserCancel               = -2
	laErrorSystemCancel             = -4
	laErrorAppCancel                = -9
)

var loadFramework = sync.OnceValue(func() error {
	_, err := purego.Dlopen("/System/Library/Frameworks/LocalAuthentication.framework/LocalAuthentication", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	return err
})

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

// failure is an NSError from LocalAuthentication, read while it exists.
type failure struct {
	domain string
	code   int64
}

func failureOf(err objc.ID) *failure {
	if err == 0 {
		return nil
	}
	return &failure{objc.Send[string](err.Send(sel("domain")), sel("UTF8String")), objc.Send[int64](err, sel("code"))}
}

// NativeAuthenticate is Authenticate with this Mac's LocalAuthentication,
// which shows its own dialog: Maskey never sees a password.
func NativeAuthenticate(ctx context.Context, reason string) error {
	if loadFramework() != nil {
		return unavailable()
	}
	// An autorelease pool must be drained on the thread that created it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := class("NSAutoreleasePool").Send(sel("new"))
	defer pool.Send(sel("drain"))
	la := class("LAContext").Send(sel("alloc")).Send(sel("init"))
	if la == 0 {
		return unavailable()
	}
	defer la.Send(sel("release"))
	if !objc.Send[bool](la, sel("canEvaluatePolicy:error:"), int64(policyDeviceOwnerAuthentication), unsafe.Pointer(nil)) {
		return unavailable()
	}
	// The reply runs on LocalAuthentication's own queue.
	done := make(chan *failure, 1)
	reply := objc.NewBlock(func(_ objc.Block, success uintptr, err objc.ID) {
		var f *failure
		if success&0xff == 0 {
			if f = failureOf(err); f == nil {
				f = &failure{}
			}
		}
		done <- f
	})
	text := class("NSString").Send(sel("stringWithUTF8String:"), reason)
	la.Send(sel("evaluatePolicy:localizedReason:reply:"), int64(policyDeviceOwnerAuthentication), text, reply)
	select {
	case f := <-done:
		reply.Release()
		return authError(f)
	case <-ctx.Done():
	}
	// Invalidating ends the dialog, and the reply follows with
	// LAErrorAppCancel. A reply that never comes keeps its block alive.
	la.Send(sel("invalidate"))
	select {
	case <-done:
		reply.Release()
	case <-time.After(5 * time.Second):
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &store.Error{Kind: store.KindCancelled, Message: "no authentication within two minutes"}
	}
	return &store.Error{Kind: store.KindCancelled, Message: "cancelled"}
}

func authError(f *failure) error {
	if f == nil {
		return nil
	}
	if f.domain == laErrorDomain {
		switch f.code {
		case laErrorUserCancel, laErrorSystemCancel, laErrorAppCancel:
			return &store.Error{Kind: store.KindCancelled, Message: "the owner did not authenticate"}
		case laErrorAuthenticationFailed:
			return &store.Error{Kind: store.KindCancelled, Message: "the owner's authentication failed"}
		}
	}
	return unavailable()
}

func unavailable() error {
	return &store.Error{Kind: store.KindPrerequisite, Message: "this Mac cannot ask its owner to authenticate"}
}
