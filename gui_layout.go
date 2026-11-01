package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/egoist/mygo/ui"
)

// layout is what the window remembers of its panes from one launch to the
// next, in layoutFile in MyGo's user data directory, beside its
// window-state.json.
type layout struct {
	SidebarHidden bool    `json:"sidebarHidden"`
	SidebarWidth  float32 `json:"sidebarWidth"`
	ListWidth     float32 `json:"listWidth"`
}

const layoutFile = "layout.json"

// loadLayout reads the layout saved at path. A missing or damaged file
// gives the default layout, and the next save replaces it; widths out of
// range are fitted as the window lays out.
func loadLayout(path string) layout {
	l := layout{SidebarWidth: sidebarDefaultWidth, ListWidth: listDefaultWidth}
	saved := l
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &saved) != nil {
		return l
	}
	return saved
}

func (l layout) save(path string) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (g *gui) layout() layout {
	return layout{SidebarHidden: g.sidebarHidden, SidebarWidth: g.sidebarWidth, ListWidth: g.listWidth}
}

// fitPanes fits the panes' widths into their ranges and, in a window room
// wide, leaves the detail its minimum width, narrowing the list first and
// then the sidebar. The fitted widths are kept, so that the panes stay
// narrowed when the window widens again, as AppKit's split views do.
func (g *gui) fitPanes(room float32) {
	// A window without a size yet would narrow both to their minimums.
	if room <= 0 {
		return
	}
	g.sidebarWidth = min(max(g.sidebarWidth, sidebarMinWidth), sidebarMaxWidth)
	var side float32
	if !g.sidebarHidden {
		side = g.sidebarWidth + 1
	}
	g.listWidth = max(min(g.listWidth, listMaxWidth, room-side-1-detailMinWidth), listMinWidth)
	if !g.sidebarHidden {
		g.sidebarWidth = min(g.sidebarWidth, room-1-g.listWidth-1-detailMinWidth)
	}
}

// panes lays out the sidebar, the list, and the detail, with a divider
// between each that the user drags. One value slides the sidebar, with its
// divider, out to the left as it hides; the list keeps its width, and the
// detail takes the room.
func (g *gui) panes(c *ui.Context, vibrant bool) {
	t := c.Theme()
	room, _ := c.Size()
	g.fitPanes(room)
	target := float32(1)
	if g.sidebarHidden {
		target = 0
	}
	row := ui.Row(c).Fill().AlignItems(ui.Stretch)
	// A drag hides and shows the sidebar in a shorter slide than the
	// button's, so that the sidebar keeps up with the hand; a snap, as
	// AppKit's split view does it, felt abrupt. The drag changes the
	// target after this call, and the slide takes its duration from the
	// next pass, which sees the handle held.
	held := ui.Local(row, "held", func() bool { return false })
	slide := 250 * time.Millisecond
	if *held {
		slide = 150 * time.Millisecond
	}
	// Whether the focus was in the sidebar when it was last built. A hidden
	// sidebar is not built, which would leave the focus nowhere, so the
	// focus goes to the list, as an NSView that hides hands the focus on.
	focused := ui.Local(row, "focused", func() bool { return false })
	shown := row.Animate("sidebar", target, slide)
	w := (g.sidebarWidth + 1) * shown
	// The sidebar's button rides the sidebar's edge, inside the padding the
	// toolbars keep on their right, and stops where the list's toolbar
	// starts after the window controls.
	buttonX := max(w-1-12-sidebarButtonSize, toolbarStart(t, c.TitleBar().Left))
	row.Children(func() {
		// Once hidden, the sidebar is gone: it takes neither the focus nor
		// keys.
		ui.Box(c).Width(max(w-1, 0)).Shrink(0).Children(func() {
			if w > 0 {
				side := ui.Column(c).Width(g.sidebarWidth).Grow(1).Margin(0, 0, 0, w-1-g.sidebarWidth)
				if !vibrant {
					side.Background(t.Surface)
				}
				side.Children(func() { g.sidebar(c, vibrant) })
				*focused = side.FocusWithin()
			}
		})
		sidebarLine := ui.Box(c).Width(min(w, 1)).Shrink(0)
		ui.Column(c).Width(g.listWidth).Shrink(0).Background(t.Background).Children(func() { g.listPane(c, buttonX+sidebarButtonSize-w) })
		listLine := ui.Box(c).Width(1).Shrink(0)
		ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() { g.detail(c) })

		// The handles are absolute children of the row, which MyGo puts
		// above the row's other children, so that they take the pointer
		// over the panes on both sides of their line; inside the line, the
		// pane after it would cover them. They stay while the sidebar
		// slides or is hidden, so that a press one of them holds goes on
		// and ends as usual. The sidebar's handle takes presses while it
		// slides in, since MyGo gives a press to the element the last
		// frame put under the pointer.
		// The widths the dividers ask for may be past the panes' ranges;
		// fitPanes fits them once the dividers are done, so that nothing
		// built later in the frame, or saved as the window closes, sees
		// them unfitted.
		g.sidebarWidth, *held = divider(c, sidebarLine, "Sidebar Divider", !g.sidebarHidden, w-1, g.sidebarWidth, sidebarMinWidth, sidebarMaxWidth)
		// A drag hides the sidebar below half its minimum width and shows
		// it again above five eighths of it, as AppKit's split view does
		// with a collapsible pane; between the two the sidebar stays as it
		// is, so that it does not flicker at one point.
		if *held && (g.sidebarHidden && g.sidebarWidth > sidebarMinWidth*5/8 || !g.sidebarHidden && g.sidebarWidth < sidebarMinWidth/2) {
			g.toggleSidebar()
		}
		g.listWidth, _ = divider(c, listLine, "List Divider", true, w+g.listWidth, g.listWidth, listMinWidth, listMaxWidth)
		g.fitPanes(room)
	})
	if g.sidebarHidden && *focused {
		*focused = false
		g.list.Focus(c)
	}
	g.sidebarButton(c, buttonX)
}

// divider makes line, at x, the divider after a pane size wide, from lo to
// hi. Once it has the focus, from Tab, the arrows move it; a handle over
// it, above the panes on both sides, takes the pointer's drags and leaves
// the focus where it is. It reports where the divider is to go, which the
// pane's width follows from lo to hi: while the handle is held, where the
// pointer's drag has put it since the press, also past lo or hi, so that
// there the divider waits for the pointer to come back, as AppKit's do;
// otherwise size, moved by a key's step. Inactive, the line takes neither
// the focus nor keys, and the handle lets the pointer through, unless the
// handle holds a press, which goes on.
func divider(c *ui.Context, line ui.Element, label string, active bool, x, size, lo, hi float32) (at float32, held bool) {
	t := c.Theme()
	grip, step := t.Space(1.5), t.Space(2.5)
	line.Background(t.Border)
	handle := ui.Box(c).Absolute().Top(0).Bottom(0).Left(x - (grip-1)/2).Width(grip)
	pos := ui.Local(handle, "pos", func() float32 { return size })
	drag, _, held := handle.Dragged()
	if held {
		*pos += drag
	} else {
		*pos = size
	}
	if !active && !held {
		handle.PassThrough()
		return size, false
	}
	handle.Cursor(ui.CursorResizeEW).KeepFocus()
	if held || handle.Hovered() {
		line.Background(t.Accent)
	}
	// The ring goes around the handle: the line is too thin to show one.
	line.Focusable().FocusRing(false).Role(ui.RoleSplitter).Label(label).Range(float64(lo), float64(hi), float64(size)).Step(float64(step))
	if line.Shortcut(0, ui.KeyLeft) {
		*pos -= step
	}
	if line.Shortcut(0, ui.KeyRight) {
		*pos += step
	}
	if line.FocusVisible() {
		handle.Draw(func(p *ui.Painter, r ui.Rect) { p.FocusRing(r, [4]float32{}) })
	}
	return *pos, held
}
