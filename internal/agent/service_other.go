//go:build !darwin || !arm64

package agent

import (
	"context"
	"errors"

	"github.com/Entwining/maskey/internal/activity"
	"github.com/Entwining/maskey/internal/store"
)

func Serve(context.Context, store.Backend, activity.Recorder, func(context.Context) error, store.Approve) error {
	return errors.New("the Keychain service requires macOS on Apple Silicon")
}

func Install(context.Context, string) error {
	return errors.New("the Keychain service requires macOS on Apple Silicon")
}
