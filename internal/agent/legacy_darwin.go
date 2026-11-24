//go:build darwin && arm64

package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// legacyLabel is the job of the Keychain service, which LegacyArg describes.
const legacyLabel = store.Service + ".keychain"

func legacyPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", legacyLabel+".plist")
}

// legacyItem is the row of the earlier service's configuration: absent,
// kept when it is not an earlier Maskey's, or "" when retireLegacy would
// remove it with its job and files.
func legacyItem(home string) ServiceItem {
	item := ServiceItem{Item: legacyPlistPath(home)}
	switch data, info, err := readPlist(item.Item); {
	case err != nil || info != nil && !sameJob(data, legacyLabel, LegacyArg):
		item.Status, item.Detail = "kept", "not an earlier Maskey's"
	case info == nil:
		item.Status = "absent"
	}
	return item
}

// retireLegacy removes the earlier service: its files first and its job
// last, because stopping the job ends the caller when launchd started it
// with LegacyArg. A job that does not stop gets its configuration back,
// which is what shows the next attempt that the job is an earlier Maskey's.
func retireLegacy(ctx context.Context, home string, control func(context.Context, ...string) error) ServiceItem {
	item := legacyItem(home)
	if item.Status != "" {
		return item
	}
	data, info, _ := readPlist(item.Item)
	item.Status = "removed"
	fail := func(err error) {
		if err != nil && item.Status != "failed" {
			item.Status, item.Detail = "failed", err.Error()
		}
	}
	dir := filepath.Join(home, "Library", "Application Support", store.Service)
	files := []string{item.Item, item.Item + ".lock", filepath.Join(dir, "keychain.sock"), filepath.Join(dir, "keychain.lock")}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(item.Item), "."+legacyLabel+".backup-*"))
	for _, file := range append(files, backups...) {
		if err := os.Remove(file); !os.IsNotExist(err) {
			fail(err)
		}
	}
	target := serviceDomain + "/" + legacyLabel
	loaded, err := jobLoaded(ctx, target, control)
	if loaded {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout+10*time.Second)
		defer cancel()
		err = stopJob(ctx, target, control)
	}
	if err != nil && info != nil {
		_ = os.WriteFile(item.Item, data, info.Mode().Perm())
	}
	fail(err)
	return item
}
