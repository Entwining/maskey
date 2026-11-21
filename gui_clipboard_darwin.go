package main

import (
	"errors"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// generalPasteboard is the clipboard the window's Copy writes a value to.
// The write is concealed: the value carries nspasteboard.org's concealed
// marker, which asks clipboard managers that read it to treat the value as
// confidential (each manager decides what that means), and it stays on
// this Mac, off Universal Clipboard.
//
// clearIf compares the pasteboard's change count rather than waiting for
// OnRelease, which also runs on a flush, at shutdown, or once the system
// has fetched every representation, not only when another copy replaces
// this one. write reads the count on the main thread right after the
// write, so none of this app's clipboard work comes between them; another
// app's copy landing in that instant would be taken for this one.
type generalPasteboard struct{}

var errClipboardNotWritten = errors.New("the clipboard did not take the value")

func (generalPasteboard) write(value []byte) (int, error) {
	change, err := 0, errClipboardNotWritten
	mygo.RunOnMain(func() {
		err = mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.Text, value))), mygo.ClipboardOptions{Concealed: true})
		if err == nil {
			change = pasteboardChangeCount()
		}
	})
	return change, err
}

func (generalPasteboard) clearIf(change int) {
	mygo.RunOnMain(func() {
		if pasteboardChangeCount() == change {
			mygo.Clipboard.Clear()
		}
	})
}

func pasteboardChangeCount() int {
	return objc.Send[int](class("NSPasteboard").Send(sel("generalPasteboard")), sel("changeCount"))
}
