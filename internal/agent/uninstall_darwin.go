//go:build darwin && arm64

package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Entwining/maskey/internal/store"
)

// ServiceItems lists the Keychain service's job and its files under home,
// with what RemoveService would do with each now. It changes nothing.
func ServiceItems(ctx context.Context, home string) []ServiceItem {
	return serviceItems(ctx, home, launchctl)
}

// RemoveService stops the Keychain service and removes its files under home.
// It decides what to remove only once it holds the lock install takes, so
// neither acts on what the other has just changed.
func RemoveService(ctx context.Context, home string) []ServiceItem {
	return removeService(ctx, home, launchctl)
}

func serviceItems(ctx context.Context, home string, control func(context.Context, ...string) error) []ServiceItem {
	files := serviceFiles(home)
	job := serviceJob(ctx, files[0], control)
	return append(keepWhileLoaded(job, files), existing(plistPath(home)+".lock"))
}

func removeService(ctx context.Context, home string, control func(context.Context, ...string) error) []ServiceItem {
	path := plistPath(home)
	lockItem := existing(path + ".lock")
	err := os.MkdirAll(filepath.Dir(path), 0755)
	var lock *os.File
	if err == nil {
		lock, err = lockFile(lockItem.Item)
	}
	if err != nil {
		detail := err.Error()
		if errors.Is(err, errServiceRunning) {
			detail = "mk setup or the window is installing it"
		}
		job := ServiceItem{Item: "Keychain service", Status: "failed", Detail: "cannot lock the Keychain service's configuration: " + detail}
		return keepWhileLoaded(job, append(serviceFiles(home), lockItem))
	}
	defer lock.Close()
	files := serviceFiles(home)
	job := serviceJob(ctx, files[0], control)
	if job.Status == "" {
		// The stop outlasts ExitTimeOut, after which launchd kills the job.
		ctx, cancel := context.WithTimeout(ctx, requestTimeout+10*time.Second)
		defer cancel()
		job.Status = "removed"
		if err := stopJob(ctx, control); err != nil {
			job.Status, job.Detail = "failed", "cannot stop the Keychain service: "+err.Error()
		}
	}
	items := keepWhileLoaded(job, files)
	for i, item := range items {
		if item.Status == "" {
			items[i].Status = "removed"
			if err := os.RemoveAll(item.Item); err != nil {
				items[i].Status, items[i].Detail = "failed", err.Error()
			}
		}
	}
	// The lock goes last, still held, and also when uninstall created it.
	if err := os.Remove(lockItem.Item); err != nil {
		lockItem.Status, lockItem.Detail = "failed", err.Error()
	} else if lockItem.Status == "" {
		lockItem.Status = "removed"
	}
	return append(items, lockItem)
}

// serviceJob is what uninstall does with the job, given the row of its
// configuration: it stops the job only when that configuration is this
// Maskey's. A job loaded without one is, as install sees it, another's.
func serviceJob(ctx context.Context, plist ServiceItem, control func(context.Context, ...string) error) ServiceItem {
	job := ServiceItem{Item: "Keychain service"}
	loaded, err := jobLoaded(ctx, control)
	switch {
	case err != nil:
		job.Status, job.Detail = "failed", "cannot ask launchd about the Keychain service: "+err.Error()
	case !loaded:
		job.Status = "absent"
	case plist.Status == "absent":
		job.Status, job.Detail = "kept", "loaded without this Maskey's configuration"
	case plist.Status == "kept":
		job.Status, job.Detail = "kept", "its configuration is not this Maskey's"
	}
	return job
}

// serviceFiles are the rows of the files that install and the service
// create under home, besides the install lock: the configuration first,
// which stays when install would refuse to replace it, the configurations
// install kept, and the directory of the service's socket and lock.
func serviceFiles(home string) []ServiceItem {
	path := plistPath(home)
	plist := ServiceItem{Item: path}
	switch data, info, err := readPlist(path); {
	case foreignConfiguration(data, info, err):
		plist.Status, plist.Detail = "kept", "not this Maskey's"
	case info == nil:
		plist.Status = "absent"
	}
	files := []ServiceItem{plist}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), backupPattern))
	for _, backup := range backups {
		files = append(files, ServiceItem{Item: backup})
	}
	return append(files, existing(filepath.Join(home, "Library", "Application Support", store.Service)))
}

// keepWhileLoaded is job followed by files, keeping those that would go
// while a job may still be loaded under the label: one without its
// configuration would make the next install refuse it as another's.
func keepWhileLoaded(job ServiceItem, files []ServiceItem) []ServiceItem {
	if job.Status == "kept" || job.Status == "failed" {
		for i := range files {
			if files[i].Status == "" {
				files[i].Status, files[i].Detail = "kept", "the Keychain service may still be loaded"
			}
		}
	}
	return append([]ServiceItem{job}, files...)
}

// existing is the row of path, absent unless something is there.
func existing(path string) ServiceItem {
	item := ServiceItem{Item: path}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		item.Status = "absent"
	}
	return item
}
