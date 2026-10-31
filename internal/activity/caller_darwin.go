//go:build darwin && arm64

package activity

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	// noDevice is NODEV, a process without a controlling terminal.
	noDevice = -1
	procPath = 4 * 1024 // PROC_PIDPATHINFO_MAXSIZE
	// ttyName holds any name devname_r gives: a device in /dev.
	ttyName = 256
)

var libc = sync.OnceValues(func() (*libcAPI, error) {
	handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	api := new(libcAPI)
	purego.RegisterLibFunc(&api.pidPath, handle, "proc_pidpath")
	purego.RegisterLibFunc(&api.devname, handle, "devname_r")
	return api, nil
})

type libcAPI struct {
	pidPath func(pid int32, buffer unsafe.Pointer, size uint32) int32
	// devname is devname_r: the service describes callers from several
	// goroutines, and devname returns a buffer they would share.
	devname func(dev int32, kind uint16, buffer unsafe.Pointer, size int32) unsafe.Pointer
}

func lookup(pid int) (process, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return process{}, false
	}
	p := process{
		parent: int(info.Eproc.Ppid),
		start:  info.Proc.P_starttime.Nano(),
		name:   unix.ByteSliceToString(info.Proc.P_comm[:]),
	}
	if api, err := libc(); err == nil {
		buffer := make([]byte, procPath)
		if n := api.pidPath(int32(pid), unsafe.Pointer(&buffer[0]), procPath); n > 0 {
			p.path = string(buffer[:n])
		}
	}
	return p, true
}

func terminal(pid int) string {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Eproc.Tdev == noDevice {
		return ""
	}
	api, err := libc()
	if err != nil {
		return ""
	}
	buffer := make([]byte, ttyName)
	if api.devname(info.Eproc.Tdev, unix.S_IFCHR, unsafe.Pointer(&buffer[0]), ttyName) == nil {
		return ""
	}
	return unix.ByteSliceToString(buffer)
}
