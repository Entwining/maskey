//go:build !darwin || !arm64

package owner

import (
	"context"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

var start = time.Now()

// Clock is the time since this process started.
func Clock() (time.Duration, bool) { return time.Since(start), true }

// NativeAuthenticate fails: owner authentication requires macOS on Apple
// Silicon.
func NativeAuthenticate(context.Context, string) error {
	return &store.Error{Kind: store.KindPrerequisite, Message: "owner authentication requires macOS on Apple Silicon"}
}
