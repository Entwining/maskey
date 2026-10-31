//go:build !darwin || !arm64

package activity

// Outside macOS on Apple Silicon, append-only file flags are not set;
// writes still use O_APPEND.

func setAppendOnly(int) error { return nil }

func clearAppendOnly(string) error { return nil }
