package main

import (
	"errors"
	"slices"

	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// alert is a question or notice that the window shows as a sheet. Return
// chooses its default button, if it has one; Escape and Command-Period
// choose its cancel button, and so does a sheet that ends without a
// button, as when the app quits.
//
// MyGo's Dialog.Message can mark no button destructive, and gives no Escape
// to a default button that is also the cancel button; once it can,
// showSheet becomes a call to it.
type alert struct {
	message, detail string
	buttons         []alertButton
}

type alertButton struct {
	title                            string
	isDefault, isCancel, destructive bool
}

func (a alert) cancelButton() int {
	return slices.IndexFunc(a.buttons, func(b alertButton) bool { return b.isCancel })
}

var errAlertNotShown = errors.New("the app no longer shows alerts")

const (
	nsAlertFirstButtonReturn = 1000
	nsModalResponseAbort     = -1001
	nsEventMaskKeyDown       = 1 << 10
	escapeKeyCode            = 53
	nsShift, nsControl       = 1 << 17, 1 << 18
	nsOption, nsCommand      = 1 << 19, 1 << 20
)

// cancelsAlert reports whether a key, by its key code, modifiers, and
// character, is Escape or Command-Period. As in AppKit's own binding of
// Command-Period, Shift may be down where a layout needs it to type the
// period; the character tells.
func cancelsAlert(keyCode uint16, modifiers uint, character string) bool {
	switch modifiers & (nsShift | nsControl | nsOption | nsCommand) {
	case 0:
		return keyCode == escapeKeyCode
	case nsCommand, nsCommand | nsShift:
		return character == "."
	}
	return false
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func nsString(s string) objc.ID { return class("NSString").Send(sel("stringWithUTF8String:"), s) }

// showSheet shows a as a sheet of win, or while win is hidden, in a window
// of its own, as MyGo's dialogs do, and waits for the index of the button
// chosen. It shows nothing once the window has closed or the app stopped.
func showSheet(win *mygo.Window, a alert) (int, error) {
	answer := make(chan int, 1)
	ran := false
	mygo.RunOnMain(func() {
		ran = true
		if win.IsDestroyed() {
			answer <- a.cancelButton()
			return
		}
		pool := class("NSAutoreleasePool").Send(sel("new"))
		defer pool.Send(sel("drain"))
		app := class("NSApplication").Send(sel("sharedApplication"))
		ns := class("NSAlert").Send(sel("alloc")).Send(sel("init"))
		ns.Send(sel("setMessageText:"), nsString(a.message))
		if a.detail != "" {
			ns.Send(sel("setInformativeText:"), nsString(a.detail))
		}
		// AppKit may leave its default icon, the app's, off a sheet.
		ns.Send(sel("setIcon:"), app.Send(sel("applicationIconImage")))
		// NSAlert gives Return to the first button; here it goes to the
		// default button, if any. AppKit's other keys stay: Command-D on a
		// Don’t Save button, and Escape on one titled Cancel.
		var cancel objc.ID
		for i, b := range a.buttons {
			button := ns.Send(sel("addButtonWithTitle:"), nsString(b.title))
			switch {
			case b.isDefault:
				button.Send(sel("setKeyEquivalent:"), nsString("\r"))
			case i == 0:
				button.Send(sel("setKeyEquivalent:"), nsString(""))
			}
			if b.destructive {
				button.Send(sel("setHasDestructiveAction:"), true)
			}
			if b.isCancel {
				cancel = button
			}
		}
		// A button has one key equivalent, and the default button may be the
		// cancel button as well, so the alert's window passes Escape and
		// Command-Period to the cancel button itself.
		window := ns.Send(sel("window"))
		keys := objc.NewBlock(func(_ objc.Block, event objc.ID) objc.ID {
			if event.Send(sel("window")) != window {
				return event
			}
			character := objc.Send[string](event.Send(sel("charactersIgnoringModifiers")), sel("UTF8String"))
			if !cancelsAlert(objc.Send[uint16](event, sel("keyCode")), objc.Send[uint](event, sel("modifierFlags")), character) {
				return event
			}
			cancel.Send(sel("performClick:"), objc.ID(0))
			return 0
		})
		monitor := class("NSEvent").Send(sel("addLocalMonitorForEventsMatchingMask:handler:"), uint64(nsEventMaskKeyDown), keys).Send(sel("retain"))
		keys.Release()
		finish := func(response int) {
			class("NSEvent").Send(sel("removeMonitor:"), monitor)
			monitor.Send(sel("release"))
			ns.Send(sel("release"))
			button := response - nsAlertFirstButtonReturn
			if button < 0 || button >= len(a.buttons) {
				button = a.cancelButton()
			}
			answer <- button
		}
		if !win.IsVisible() {
			app.Send(sel("activateIgnoringOtherApps:"), true)
			response := objc.Send[int](ns, sel("runModal"))
			finish(response)
			// A quit aborts the alert's modal session and leaves stopping
			// the app until the session has ended, as MyGo's own dialogs
			// do; quitting again stops it.
			if response == nsModalResponseAbort {
				mygo.App.Quit()
			}
			return
		}
		done := objc.NewBlock(func(_ objc.Block, response int) { finish(response) })
		ns.Send(sel("beginSheetModalForWindow:completionHandler:"), objc.ID(win.NativeHandle()), done)
		done.Release()
	})
	if !ran {
		return 0, errAlertNotShown
	}
	return <-answer, nil
}
