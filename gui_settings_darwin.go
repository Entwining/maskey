package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/stores"
)

// showSettingsWindow opens the Settings window, or brings the open one
// forward.
func showSettingsWindow(g *gui) {
	s := &g.settings
	if s.focus != nil {
		s.focus()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:     "Maskey Settings",
		Width:     560,
		Height:    440,
		MinWidth:  520,
		MinHeight: 440,
		StateKey:  "settings",
		Content:   ui.View(g.settingsView),
	})
	s.redraw = func() { win.Update(func() {}) }
	s.ask = func(a alert) (int, error) { return showSheet(win, a) }
	s.close, s.focus = win.Close, win.Focus
	win.OnClosed(func() {
		s.redraw, s.ask, s.close, s.focus = nil, nil, nil, nil
		s.token, s.adding, s.replacing = "", false, stores.Connection{}
	})
}
