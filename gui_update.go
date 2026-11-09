package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// updateReminder is how long a newer version waits before the update window
// shows on its own, as Sparkle waits a week before it presents an update it
// installed in the background.
const updateReminder = 7 * 24 * time.Hour

// showUpdate names version, the newer one the updater found, in the update
// menu item and under the sidebar; "" restores the item. An update installed
// without asking runs only once the app quits, so without them a window kept
// open shows no sign of it.
func (g *gui) showUpdate(version string) {
	switch {
	case version == "":
		g.updateItem.SetLabel(g.checkLabel)
	case version != g.update:
		// Wall time: the monotonic clock stops while the Mac sleeps.
		g.updateSince = time.Now().Round(0)
		g.updateItem.SetLabel("Update to Maskey " + version + "…")
	}
	g.update = version
}

// remindUpdate shows the update window once for each version, as the app
// comes to the front after the version has waited updateReminder. A failed
// check clears the version until a later check finds it again.
func (g *gui) remindUpdate() {
	if g.update == "" || g.reminded == g.update || time.Since(g.updateSince) < updateReminder {
		return
	}
	g.reminded = g.update
	g.checkForUpdates()
}

func (g *gui) updateButton(c *ui.Context) {
	t := c.Theme()
	label := "Update to " + g.update
	b := ui.ButtonBase(c.Key("update")).Margin(t.Space(2)).Padding(t.Space(1.5), t.Space(2.5)).Gap(t.Space(2)).Radius(6).Label(label)
	// A tint of the accent, since the accent itself fills the selected row.
	switch {
	case b.Pressed():
		b.Background(t.Accent.Alpha(0.44))
	case b.Hovered():
		b.Background(t.Accent.Alpha(0.32))
	default:
		b.Background(t.Accent.Alpha(0.2))
	}
	if b.Children(func() {
		ui.Box(c).Size(8, 8).Shrink(0).Radius(4).Background(t.Accent)
		ui.Text(c, label).FontSize(t.FontSize - 1).SingleLine().MinWidth(0)
	}).Clicked() {
		g.checkForUpdates()
	}
}
