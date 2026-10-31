//go:build !darwin || !arm64

package activity

// Describing processes requires macOS on Apple Silicon.

func lookup(int) (process, bool) { return process{}, false }

func terminal(int) string { return "" }
