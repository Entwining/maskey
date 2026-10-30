//go:build darwin && arm64

package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Entwining/maskey/internal/store"
)

const serviceLabel = store.Service + ".keychain"
const servicePATH = "/usr/bin:/bin"

var (
	errServiceFiles     = errors.New("the Keychain service files could not be prepared safely")
	errServiceRunning   = errors.New("the Keychain service is already running")
	errServiceInstall   = errors.New("the Keychain service could not be installed; its previous configuration, if any, remains in ~/Library/LaunchAgents")
	errServiceCollision = errors.New("the Keychain service configuration belongs to a different job; it was not replaced")
	errJobNotLoaded     = errors.New("the Keychain service is not loaded")
)

func Serve(ctx context.Context, backend store.Backend, check func(context.Context) error) error {
	path, err := socketPath()
	if err != nil {
		return errServiceFiles
	}
	executable, err := os.Executable()
	if err != nil {
		return errServiceFiles
	}
	self, err := selfIdentity()
	if err != nil {
		return err
	}
	// launchd's EnvironmentVariables adds to its environment rather than replacing it.
	os.Clearenv()
	os.Setenv("PATH", servicePATH)
	return serve(ctx, path, backend, check, func() bool {
		installed, err := fileIdentity(executable)
		return err == nil && !bytes.Equal(installed, self)
	})
}

// serve answers authenticated connections until ctx ends, or until a
// connection fails authentication and replaced reports that this process's
// executable is now a different valid build: an upgrade replaced the app, and
// returning lets launchd's KeepAlive start the new build. Only a change to that
// file, not a client, can end the service this way.
func serve(ctx context.Context, path string, backend store.Backend, check func(context.Context) error, replaced func() bool) error {
	dir := filepath.Dir(path)
	if err := privateDir(dir); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(dir, "keychain.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := removeStaleSocket(ctx, path); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return errServiceFiles
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(path)
	defer func() {
		listener.Close()
		if current, err := os.Lstat(path); err == nil && info != nil && os.SameFile(info, current) {
			os.Remove(path)
		}
	}()
	if err != nil || os.Chmod(path, 0600) != nil {
		return errServiceFiles
	}
	ctx, retire := context.WithCancel(ctx)
	defer retire()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	workers := context.WithoutCancel(ctx)
	var pending sync.WaitGroup
	slots := make(chan struct{}, 4)
	var acceptErr error
accept:
	for {
		select {
		case <-ctx.Done():
			break accept
		case slots <- struct{}{}:
		}
		conn, err := listener.AcceptUnix()
		if err != nil {
			<-slots
			if ctx.Err() == nil {
				acceptErr = errServiceFiles
			}
			break
		}
		pending.Go(func() {
			defer func() { <-slots }()
			defer conn.Close()
			if authenticate(conn) == nil {
				serveRequest(workers, conn, backend, check)
			} else if replaced() {
				retire()
			}
		})
	}
	listener.Close()
	// Accepted writes may finish after SIGTERM; immediate cancellation makes
	// their outcome uncertain. Each ends within requestTimeout, and the lock
	// is held until they have.
	pending.Wait()
	return acceptErr
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return errServiceFiles
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !owned(info) || os.Chmod(path, 0700) != nil {
		return errServiceFiles
	}
	return nil
}

func owned(info os.FileInfo) bool { return info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid()) }

func lockFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errServiceFiles
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) || info.Sys().(*syscall.Stat_t).Nlink != 1 || file.Chmod(0600) != nil {
		file.Close()
		return nil, errServiceFiles
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errServiceRunning
		}
		return nil, errServiceFiles
	}
	// Keep the inode: unlinking a held lock lets another instance lock a different file.
	return file, nil
}

func removeStaleSocket(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSocket == 0 || !owned(info) {
		return errServiceFiles
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	if err == nil {
		conn.Close()
		return errServiceRunning
	}
	if !errors.Is(err, unix.ECONNREFUSED) {
		return errServiceFiles
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) || os.Remove(path) != nil {
		return errServiceFiles
	}
	return nil
}

func Install(ctx context.Context, executable string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errServiceInstall
	}
	socket, err := socketPath()
	if err != nil {
		return errServiceInstall
	}
	path := filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	return install(ctx, executable, path, launchctl, func(ctx context.Context) error { return awaitService(ctx, socket) })
}

func install(ctx context.Context, executable, path string, control func(context.Context, ...string) error, ready func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*requestTimeout+10*time.Second)
	defer cancel()
	executable, err := filepath.Abs(executable)
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return errServiceInstall
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errServiceInstall
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errServiceInstall
	}
	info, err = os.Lstat(dir)
	if err != nil || !info.IsDir() || !owned(info) {
		return errServiceInstall
	}
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	old, oldInfo, err := readPlist(path)
	if err != nil || oldInfo != nil && !sameJob(old) {
		return errServiceCollision
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	target := domain + "/" + serviceLabel
	loaded := control(ctx, "print", target)
	if loaded != nil && !errors.Is(loaded, errJobNotLoaded) {
		return errServiceInstall
	}
	// A loaded job without our configuration file was installed by someone
	// else under our label.
	if loaded == nil && oldInfo == nil {
		return errServiceCollision
	}
	stage, err := os.CreateTemp(dir, "."+serviceLabel+".backup-*")
	if err != nil {
		return errServiceInstall
	}
	backup := stage.Name()
	keep := false
	defer func() {
		stage.Close()
		if !keep {
			os.Remove(backup)
		}
	}()
	plist := jobPlist(executable)
	if _, err := stage.Write(plist); err != nil || stage.Close() != nil {
		return errServiceInstall
	}
	if oldInfo == nil {
		if unix.RenameatxNp(unix.AT_FDCWD, backup, unix.AT_FDCWD, path, unix.RENAME_EXCL) != nil {
			return errServiceInstall
		}
	} else {
		if unix.RenameatxNp(unix.AT_FDCWD, backup, unix.AT_FDCWD, path, unix.RENAME_SWAP) != nil {
			return errServiceInstall
		}
		keep = true
		current, currentInfo, err := readPlist(backup)
		if err != nil || currentInfo == nil || !os.SameFile(oldInfo, currentInfo) || !bytes.Equal(old, current) {
			if unix.RenameatxNp(unix.AT_FDCWD, backup, unix.AT_FDCWD, path, unix.RENAME_SWAP) == nil {
				keep = false
			}
			return errServiceInstall
		}
		// A previous configuration that differs, such as one the user edited,
		// stays beside the new one; an identical one has nothing to preserve.
		keep = !bytes.Equal(old, plist)
	}
	if loaded == nil && control(ctx, "bootout", target) != nil {
		return errServiceInstall
	}
	if control(ctx, "bootstrap", domain, path) != nil {
		return errServiceInstall
	}
	return ready(ctx)
}

func readPlist(path string) ([]byte, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info) {
		return nil, nil, errServiceFiles
	}
	data, err := io.ReadAll(file)
	return data, info, err
}

func sameJob(data []byte) bool {
	// The dictionary's key/value order matters; parallel slices lose that association.
	var doc struct {
		XMLName xml.Name `xml:"plist"`
		Dict    struct {
			Items []struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if xml.Unmarshal(data, &doc) != nil || len(doc.Dict.Items)%2 != 0 {
		return false
	}
	label, args := false, false
	for i := 0; i < len(doc.Dict.Items); i += 2 {
		key, value := doc.Dict.Items[i], doc.Dict.Items[i+1]
		if key.XMLName.Local != "key" {
			return false
		}
		switch key.Text {
		case "Label":
			if label || value.XMLName.Local != "string" || value.Text != serviceLabel {
				return false
			}
			label = true
		case "ProgramArguments":
			if args || value.XMLName.Local != "array" || len(value.Strings) != 2 || !filepath.IsAbs(value.Strings[0]) || value.Strings[1] != "--keychain-service" {
				return false
			}
			args = true
		}
	}
	return label && args
}

// jobPlist is the job's configuration. Umask 63 is 077, so what the service
// creates, its socket included, is the user's alone. ExitTimeOut stays above
// requestTimeout, so that launchd lets writes accepted before SIGTERM finish
// instead of killing them.
func jobPlist(executable string) []byte {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(executable))
	return []byte(xml.Header + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + serviceLabel + `</string>
  <key>ProgramArguments</key><array><string>` + escaped.String() + `</string><string>--keychain-service</string></array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>` + servicePATH + `</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>AssociatedBundleIdentifiers</key><string>` + store.Service + `</string>
  <key>ProcessType</key><string>Background</string>
  <key>Umask</key><integer>63</integer>
  <key>ExitTimeOut</key><integer>35</integer>
</dict>
</plist>
`)
}

func launchctl(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	cmd.Env = []string{"PATH=" + servicePATH}
	err := cmd.Run()
	var exit *exec.ExitError
	if args[0] == "print" && errors.As(err, &exit) && exit.ExitCode() == 113 {
		return errJobNotLoaded
	}
	return err
}

func awaitService(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
		if err == nil {
			defer conn.Close()
			if err := authenticate(conn.(*net.UnixConn)); err != nil {
				return identityError(err)
			}
			_, err := exchange(ctx, conn, request{Op: "check"})
			return err
		}
		if !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ECONNREFUSED) {
			return errServiceInstall
		}
		select {
		case <-ctx.Done():
			return errServiceInstall
		case <-time.After(50 * time.Millisecond):
		}
	}
}
