//go:build darwin && arm64

package agent

import (
	"bytes"
	"net"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var loadPeerAPI = sync.OnceValues(openPeerAPI)

// authenticate checks that the peer runs the same signed executable as this
// process, and returns the peer's pid as the kernel reports it. It asks the
// kernel for the peer's cdhash by audit token: Security's guest lookup also
// reads the peer's process information, which a command sandbox denies for
// every process outside it.
func authenticate(conn *net.UnixConn) (int, error) {
	api, err := loadPeerAPI()
	if err != nil {
		return 0, err
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, errPeerUnavailable
	}
	var token [8]uint32 // Opaque audit_token_t.
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			peerErr = errPeerUnavailable
			return
		}
		if cred.Uid != uint32(os.Geteuid()) {
			peerErr = errPeerMismatch
			return
		}
		size := uint32(unsafe.Sizeof(token))
		if api.getsockopt(int32(fd), unix.SOL_LOCAL, unix.LOCAL_PEERTOKEN, unsafe.Pointer(&token), &size) != 0 || size != uint32(unsafe.Sizeof(token)) {
			peerErr = errPeerUnavailable
		}
	}); err != nil {
		return 0, errPeerUnavailable
	}
	if peerErr != nil {
		return 0, peerErr
	}

	// An audit token's sixth word is the pid.
	pid := int32(token[5])
	var status uint32
	var peerHash [cdhashSize]byte
	if api.csops(pid, csOpsStatus, unsafe.Pointer(&status), unsafe.Sizeof(status), unsafe.Pointer(&token)) != 0 || status&csValid == 0 ||
		api.csops(pid, csOpsCDHash, unsafe.Pointer(&peerHash), cdhashSize, unsafe.Pointer(&token)) != 0 {
		return 0, errPeerUnavailable
	}
	selfHash, err := selfIdentity()
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(peerHash[:], selfHash) {
		return 0, errPeerMismatch
	}
	return int(pid), nil
}

// The kernel's code signing queries, from XNU's sys/codesign.h and
// kern/cs_blobs.h, which no SDK ships.
const (
	csOpsStatus = 0
	csOpsCDHash = 5
	csValid     = 0x1
	cdhashSize  = 20
)

// selfIdentity is the running code's cdhash.
func selfIdentity() ([]byte, error) {
	api, err := loadPeerAPI()
	if err != nil {
		return nil, err
	}
	var self uintptr
	if api.copySelf(0, &self) != 0 {
		return nil, errPeerUnavailable
	}
	defer api.release(self)
	info, err := api.verifiedSigningInfo(self)
	if err != nil {
		return nil, err
	}
	defer api.release(info)
	return api.uniqueBytes(info)
}

// fileIdentity is the cdhash of the validly signed executable at path.
func fileIdentity(path string) ([]byte, error) {
	api, err := loadPeerAPI()
	if err != nil {
		return nil, err
	}
	url := api.urlCreate(0, unsafe.StringData(path), int64(len(path)), 0)
	if url == 0 {
		return nil, errPeerUnavailable
	}
	defer api.release(url)
	var code uintptr
	if api.staticCodeCreate(url, 0, &code) != 0 {
		return nil, errPeerUnavailable
	}
	defer api.release(code)
	if api.staticCheckValidity(code, 0, 0) != 0 {
		return nil, errPeerUnavailable
	}
	var info uintptr
	if api.copySigningInfo(code, 0, &info) != 0 {
		return nil, errPeerUnavailable
	}
	defer api.release(info)
	return api.uniqueBytes(info)
}

func (api *peerAPI) uniqueBytes(info uintptr) ([]byte, error) {
	data := api.dictionaryGetValue(info, api.uniqueKey)
	if data == 0 {
		return nil, errPeerUnavailable
	}
	// The copy outlives the dictionary that owns the bytes.
	return bytes.Clone(unsafe.Slice((*byte)(api.dataBytes(data)), api.dataLength(data))), nil
}

type peerAPI struct {
	getsockopt          func(int32, int32, int32, unsafe.Pointer, *uint32) int32
	csops               func(int32, uint32, unsafe.Pointer, uintptr, unsafe.Pointer) int32
	dictionaryGetValue  func(uintptr, uintptr) uintptr
	release             func(uintptr)
	copySelf            func(uint32, *uintptr) int32
	checkValidity       func(uintptr, uint32, uintptr) int32
	copySigningInfo     func(uintptr, uint32, *uintptr) int32
	urlCreate           func(uintptr, *byte, int64, uint8) uintptr
	staticCodeCreate    func(uintptr, uint32, *uintptr) int32
	staticCheckValidity func(uintptr, uint32, uintptr) int32
	dataBytes           func(uintptr) unsafe.Pointer
	dataLength          func(uintptr) int64
	uniqueKey           uintptr
}

// errSecCSStaticCodeChanged is Security's "the code on disk does not match
// what is running".
const errSecCSStaticCodeChanged = -67034

func (api *peerAPI) verifiedSigningInfo(code uintptr) (uintptr, error) {
	// Signing information alone may come from disk; validate its link to the
	// running code before trusting the identity returned by this same object.
	switch api.checkValidity(code, 0, 0) {
	case 0:
	case errSecCSStaticCodeChanged:
		// The executable this code was started from has been replaced, as by
		// an upgrade of the app in place.
		return 0, errPeerMismatch
	default:
		return 0, errPeerUnavailable
	}
	var info uintptr
	if api.copySigningInfo(code, 0, &info) != 0 {
		return 0, errPeerUnavailable
	}
	return info, nil
}

func openPeerAPI() (*peerAPI, error) {
	var handles [3]uintptr
	for i, path := range []string{
		"/usr/lib/libSystem.B.dylib",
		"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
		"/System/Library/Frameworks/Security.framework/Security",
	} {
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			return nil, errPeerUnavailable
		}
		handles[i] = handle
	}
	api := new(peerAPI)
	var copyMemory func(*uintptr, uintptr, uintptr) uintptr
	for _, binding := range []struct {
		target any
		handle uintptr
		name   string
	}{
		{&api.getsockopt, handles[0], "getsockopt"},
		{&api.csops, handles[0], "csops_audittoken"},
		{&copyMemory, handles[0], "memcpy"},
		{&api.dictionaryGetValue, handles[1], "CFDictionaryGetValue"},
		{&api.release, handles[1], "CFRelease"},
		{&api.copySelf, handles[2], "SecCodeCopySelf"},
		{&api.checkValidity, handles[2], "SecCodeCheckValidity"},
		{&api.copySigningInfo, handles[2], "SecCodeCopySigningInformation"},
		{&api.urlCreate, handles[1], "CFURLCreateFromFileSystemRepresentation"},
		{&api.staticCodeCreate, handles[2], "SecStaticCodeCreateWithPath"},
		{&api.staticCheckValidity, handles[2], "SecStaticCodeCheckValidity"},
		{&api.dataBytes, handles[1], "CFDataGetBytePtr"},
		{&api.dataLength, handles[1], "CFDataGetLength"},
	} {
		address, err := purego.Dlsym(binding.handle, binding.name)
		if err != nil {
			return nil, errPeerUnavailable
		}
		purego.RegisterFunc(binding.target, address)
	}
	uniqueKeySymbol, err := purego.Dlsym(handles[2], "kSecCodeInfoUnique")
	if err != nil {
		return nil, errPeerUnavailable
	}
	copyMemory(&api.uniqueKey, uniqueKeySymbol, unsafe.Sizeof(api.uniqueKey))
	return api, nil
}
