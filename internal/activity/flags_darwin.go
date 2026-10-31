//go:build darwin && arm64

package activity

import "golang.org/x/sys/unix"

// setAppendOnly protects the record from non-append writes, truncation,
// renaming, and unlinking. The owner can clear the flag.
func setAppendOnly(fd int) error {
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return err
	}
	if info.Flags&unix.UF_APPEND != 0 {
		return nil
	}
	return unix.Fchflags(fd, int(info.Flags|unix.UF_APPEND))
}

// clearAppendOnly clears the append-only flag of the file at path, which
// refuses unlinking it.
func clearAppendOnly(path string) error {
	var info unix.Stat_t
	if err := unix.Lstat(path, &info); err != nil {
		return err
	}
	return unix.Chflags(path, int(info.Flags&^unix.UF_APPEND))
}
