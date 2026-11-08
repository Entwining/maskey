package main

import (
	"errors"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// generalPasteboard is the clipboard the window's Copy writes a value to,
// through AppKit itself. The value carries nspasteboard.org's concealed
// marker, which asks clipboard managers that read it to treat the value as
// confidential; each manager decides what that means. It is written for
// this Mac only, which keeps it off Universal Clipboard.
//
// MyGo's Clipboard.Write takes MIME formats, which cannot name the marker's
// type, and prepares the pasteboard with no options. Once it can write both,
// write becomes a Clipboard.Write call. clearIf still compares the change
// count: OnRelease also runs on a flush or at shutdown, not only when
// another copy replaces this one.
//
// MyGo tracks its own clipboard by the pasteboard's change count, which
// this write moves, so it drops its claim and its quit-time flush and
// cleanup leave the value alone; its reads, such as a paste in a text
// field, read the pasteboard as they would another app's. write and
// clearIf run on the main thread, where MyGo keeps that state.
type generalPasteboard struct{}

const (
	nsPasteboardContentsCurrentHostOnly = 1 << 0
	plainTextType                       = "public.utf8-plain-text"
	concealedType                       = "org.nspasteboard.ConcealedType"
)

var errClipboardNotWritten = errors.New("the clipboard did not take the value")

func (generalPasteboard) write(value []byte) (int, error) {
	change, err := 0, errClipboardNotWritten
	mygo.RunOnMain(func() {
		pool := class("NSAutoreleasePool").Send(sel("new"))
		defer pool.Send(sel("drain"))
		board := class("NSPasteboard").Send(sel("generalPasteboard"))
		item := class("NSPasteboardItem").Send(sel("alloc")).Send(sel("init")).Send(sel("autorelease"))
		data := class("NSData").Send(sel("dataWithBytes:length:"), unsafe.Pointer(unsafe.SliceData(value)), uint(len(value)))
		runtime.KeepAlive(value)
		item.Send(sel("setData:forType:"), data, nsString(plainTextType))
		// The marker is identified by its type; its data has no meaning.
		item.Send(sel("setData:forType:"), class("NSData").Send(sel("data")), nsString(concealedType))
		board.Send(sel("prepareForNewContentsWithOptions:"), uint(nsPasteboardContentsCurrentHostOnly))
		if objc.Send[bool](board, sel("writeObjects:"), class("NSArray").Send(sel("arrayWithObject:"), item)) {
			change, err = objc.Send[int](board, sel("changeCount")), nil
		}
	})
	return change, err
}

func (generalPasteboard) clearIf(change int) {
	mygo.RunOnMain(func() {
		board := class("NSPasteboard").Send(sel("generalPasteboard"))
		if objc.Send[int](board, sel("changeCount")) == change {
			board.Send(sel("clearContents"))
		}
	})
}
