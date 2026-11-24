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

// retireLegacy removes the earlier service for an install, which that
// service's own job may be running: its files first and its job last,
// because stopping the job ends the caller when launchd started it with
// LegacyArg. A job that does not stop gets its configuration back, which is
// what shows the next attempt that the job is an earlier Maskey's.
func retireLegacy(ctx context.Context, home string, control func(context.Context, ...string) error) ServiceItem {
	item := legacyItem(home)
	if item.Status != "" {
		return item
	}
	data, info, _ := readPlist(item.Item)
	item.Status = "removed"
	if err := removeLegacyFiles(home); err != nil {
		item.Status, item.Detail = "failed", err.Error()
	}
	if err := stopLegacy(ctx, control); err != nil {
		if info != nil {
			_ = os.WriteFile(item.Item, data, info.Mode().Perm())
		}
		if item.Status != "failed" {
			item.Status, item.Detail = "failed", err.Error()
		}
	}
	return item
}

// removeLegacy removes the earlier service for an uninstall: its job first,
// and its files only once the job has stopped, since a job that still runs
// needs them and keeps its configuration, as retireLegacy says.
func removeLegacy(ctx context.Context, home string, control func(context.Context, ...string) error) ServiceItem {
	item := legacyItem(home)
	if item.Status != "" {
		return item
	}
	if err := stopLegacy(ctx, control); err != nil {
		item.Status, item.Detail, item.MayRun = "failed", "cannot stop the Keychain service of an earlier Maskey: "+err.Error(), true
		return item
	}
	item.Status = "removed"
	if err := removeLegacyFiles(home); err != nil {
		item.Status, item.Detail = "failed", err.Error()
	}
	return item
}

// stopLegacy stops the earlier service's job, if launchd has it.
func stopLegacy(ctx context.Context, control func(context.Context, ...string) error) error {
	target := serviceDomain + "/" + legacyLabel
	loaded, err := jobLoaded(ctx, target, control)
	if !loaded {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout+10*time.Second)
	defer cancel()
	return stopJob(ctx, target, control)
}

// removeLegacyFiles removes what the earlier service kept under home, and
// returns the first failure.
func removeLegacyFiles(home string) error {
	plist := legacyPlistPath(home)
	dir := filepath.Join(home, "Library", "Application Support", store.Service)
	files := []string{plist, plist + ".lock", filepath.Join(dir, "keychain.sock"), filepath.Join(dir, "keychain.lock")}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(plist), "."+legacyLabel+".backup-*"))
	var failure error
	for _, file := range append(files, backups...) {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) && failure == nil {
			failure = err
		}
	}
	return failure
}
