package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store/storetest"
)

// pointerDrag is a press on a divider's handle that moves at the panes'
// height, ten DIPs or less at a time, as a hand does.
type pointerDrag struct {
	h *harness
	x float32
}

// pressDivider presses the handle of the divider whose line starts at x,
// over the pane after the line, where the handle is above the pane.
func (h *harness) pressDivider(x float32) *pointerDrag {
	d := &pointerDrag{h, x + 3}
	h.tt.Press(d.x, 300)
	return d
}

func (d *pointerDrag) by(dx float32) {
	n := int(max(dx, -dx)+9) / 10
	for k := 1; k <= n; k++ {
		d.h.tt.Move(d.x+dx*float32(k)/float32(n), 300)
	}
	d.x += dx
}

func (d *pointerDrag) release() { d.h.tt.Release(d.x, 300) }

// dragDivider drags the divider whose line starts at x by dx and lets the
// sidebar finish sliding.
func (h *harness) dragDivider(x, dx float32) {
	d := h.pressDivider(x)
	d.by(dx)
	d.release()
	h.tt.Advance(time.Second)
}

// expectPanes checks the widths of the sidebar, unless hidden, and the
// list, and that the list follows the sidebar.
func (h *harness) expectPanes(when string, sidebar, list float32) {
	h.t.Helper()
	l := h.find("Entries")
	x := float32(0)
	if sidebar > 0 {
		s := h.find("Namespaces")
		x = sidebar + 1
		if !near(s.W, sidebar) {
			h.t.Fatalf("%s: the sidebar at %v is not %v wide", when, s, sidebar)
		}
	} else if _, shown := h.tt.Find("Namespaces"); shown {
		h.t.Fatalf("%s: the sidebar shows", when)
	}
	if !near(l.X, x) || !near(l.W, list) {
		h.t.Fatalf("%s: the list at %v is not at %v, %v wide", when, l, x, list)
	}
}

// A divider follows the pointer, and dragged past either end of its range,
// stops there and waits: it moves again only once the pointer comes back
// to it. The list's divider stops at its maximum in a wide window, and
// short of leaving the detail less than its minimum in a narrow one.
func TestDividerWaitsForPointer(t *testing.T) {
	listLine := float32(sidebarDefaultWidth + 1 + listDefaultWidth)
	for _, c := range []struct {
		name    string
		window  int
		sidebar bool
		// The divider goes past limit by past, which is negative below it.
		limit, past float32
	}{
		{"sidebar above its maximum", 980, true, sidebarMaxWidth, 60},
		{"list below its minimum", 980, false, listMinWidth, -60},
		{"list above its maximum", 1400, false, listMaxWidth, 60},
		{"list above the room", 980, false, 980 - (sidebarDefaultWidth + 1) - 1 - detailMinWidth, 60},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetSize(c.window, 600)
			h.tt.Frame()
			line, width := listLine, float32(listDefaultWidth)
			expect := func(when string, w float32) {
				t.Helper()
				if c.sidebar {
					h.expectPanes(when, w, listDefaultWidth)
				} else {
					h.expectPanes(when, sidebarDefaultWidth, w)
				}
			}
			if c.sidebar {
				line, width = sidebarDefaultWidth, sidebarDefaultWidth
			}
			d := h.pressDivider(line)
			d.by(c.limit + c.past - width)
			expect("dragged past the limit", c.limit)
			// Closing the window saves the width the pane has.
			if l := h.g.layout(); c.sidebar && l.SidebarWidth != c.limit || !c.sidebar && l.ListWidth != c.limit {
				t.Fatalf("held past the limit, the window would save %+v", l)
			}
			d.by(-c.past / 2)
			expect("back by half as far", c.limit)
			d.by(-c.past)
			expect("back by more", c.limit-c.past/2)
			d.release()
			h.tt.Advance(time.Second)
			expect("released", c.limit-c.past/2)
		})
	}
}

// Narrowing the window narrows the list, then the sidebar, to leave the
// detail its minimum width, and the panes stay narrowed as it widens again.
// A frame before the window has a size narrows nothing.
func TestNarrowWindowKeepsDetail(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(0, 0)
	h.tt.Frame()
	h.tt.SetSize(1400, 600)
	h.tt.Frame()
	h.dragDivider(sidebarDefaultWidth, 100)
	h.dragDivider(sidebarDefaultWidth+100+1+listDefaultWidth, 120)
	h.expectPanes("widened", sidebarDefaultWidth+100, listDefaultWidth+120)
	h.tt.SetSize(windowMinWidth, 600)
	h.expectPanes("at the minimum width", sidebarMinWidth, listMinWidth)
	h.tt.SetSize(1400, 600)
	h.tt.Frame()
	h.expectPanes("widened again", sidebarMinWidth, listMinWidth)
}

// Tab reaches the sidebar's divider, and the arrows move it ten DIPs at a
// time between the sidebar's minimum and maximum widths, never hiding it.
func TestDividerKeys(t *testing.T) {
	h := newHarness(t)
	for i := 0; !h.tt.Focused("Sidebar Divider"); i++ {
		if i == 10 {
			t.Fatal("Tab never reached the sidebar's divider")
		}
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
	}
	press := func(key ui.Key, times int) {
		for range times {
			h.tt.Key(0, key)
			h.tt.Advance(time.Second)
		}
	}
	press(ui.KeyRight, 1)
	h.expectPanes("after Right", sidebarDefaultWidth+10, listDefaultWidth)
	press(ui.KeyRight, 20)
	h.expectPanes("after Right 21 times", sidebarMaxWidth, listDefaultWidth)
	press(ui.KeyLeft, 30)
	h.expectPanes("after Left 30 times", sidebarMinWidth, listDefaultWidth)
	if h.g.sidebarHidden || h.g.sidebarItem.Label != "Hide Sidebar" {
		t.Fatalf("the keys hid the sidebar: the menu item reads %q", h.g.sidebarItem.Label)
	}
}

// With the sidebar hidden in a window, the list at its minimum width cuts
// "Search Results" short on its one line rather than letting it reach New
// Entry or New Entry leave the list.
func TestNarrowListTitleTruncates(t *testing.T) {
	h := newHarness(t)
	h.tt.SetTitleBar(trafficLights)
	h.typeInto("Search entries", "a")
	whole := h.find("Search Results")
	h.clickSidebarButton("Hide Sidebar")
	h.dragDivider(listDefaultWidth, listMinWidth-listDefaultWidth)
	h.expectPanes("narrowed", 0, listMinWidth)
	title, add, list := h.find("Search Results"), h.find("New Entry"), h.find("Entries")
	if title.W >= textWidth("Search Results", 0, 700) || !near(title.H, whole.H) {
		t.Fatalf("the title at %v is not cut short on its line, as at %v", title, whole)
	}
	if title.X+title.W+8 > add.X+0.5 || add.X+add.W > list.X+list.W+0.5 {
		t.Fatalf("the title at %v reaches New Entry at %v, or New Entry leaves the list at %v", title, add, list)
	}
}

// The window keeps its layout in a file it reads as it opens and writes as
// it closes. Without the file, or with a damaged one, it takes the default
// layout, and a field the file lacks keeps its default; opened with the
// sidebar hidden, it shows none, without sliding.
func TestLayoutRemembered(t *testing.T) {
	path := filepath.Join(t.TempDir(), layoutFile)
	defaults := layout{SidebarWidth: sidebarDefaultWidth, ListWidth: listDefaultWidth}
	if l := loadLayout(path); l != defaults {
		t.Fatalf("without a file, the layout is %+v", l)
	}
	for _, damaged := range []string{"{", `{"sidebarHidden":true,"sidebarWidth":"wide"}`} {
		if err := os.WriteFile(path, []byte(damaged), 0o644); err != nil {
			t.Fatal(err)
		}
		if l := loadLayout(path); l != defaults {
			t.Fatalf("from %s, the layout is %+v", damaged, l)
		}
	}
	if err := os.WriteFile(path, []byte(`{"sidebarHidden":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if l, want := loadLayout(path), (layout{SidebarHidden: true, SidebarWidth: sidebarDefaultWidth, ListWidth: listDefaultWidth}); l != want {
		t.Fatalf("from a file without widths, the layout is %+v", l)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	backend := storetest.New()
	backend.Put("db/PASSWORD", []byte("synthetic-db-0a7f"))
	h := newHarnessAt(t, backend, path)
	h.dragDivider(sidebarDefaultWidth, 40)
	h.dragDivider(sidebarDefaultWidth+40+1+listDefaultWidth, 60)
	h.clickSidebarButton("Hide Sidebar")
	h.g.close()
	saved := layout{SidebarHidden: true, SidebarWidth: sidebarDefaultWidth + 40, ListWidth: listDefaultWidth + 60}
	if l := loadLayout(path); l != saved {
		t.Fatalf("the window saved %+v", l)
	}

	h = newHarnessAt(t, backend, path)
	h.expectPanes("opened", 0, saved.ListWidth)
	if _, ok := h.tt.Find("Show Sidebar"); !ok || h.g.sidebarItem.Label != "Show Sidebar" || h.tt.FramePending() {
		t.Fatalf("opened hidden, the button shows %v, the menu item reads %q, frames pending %v", ok, h.g.sidebarItem.Label, h.tt.FramePending())
	}
	h.clickSidebarButton("Show Sidebar")
	h.expectPanes("shown", saved.SidebarWidth, saved.ListWidth)
}

// With the sidebar hidden, the list may take the sidebar's room. Shown
// again, the sidebar comes back as wide as it was, and the list narrows to
// leave the detail its minimum width.
func TestHiddenSidebarRoom(t *testing.T) {
	h := newHarness(t)
	h.clickSidebarButton("Hide Sidebar")
	h.dragDivider(listDefaultWidth, listMaxWidth-listDefaultWidth)
	h.expectPanes("widened", 0, listMaxWidth)
	h.clickSidebarButton("Show Sidebar")
	h.expectPanes("shown", sidebarDefaultWidth, 980-(sidebarDefaultWidth+1)-1-detailMinWidth)
}

// While its handle is held, the sidebar hides as the drag goes below half
// its minimum width, and shows again, at its minimum width, only as the
// drag comes back above five eighths of it. Where the pointer is decides,
// however far past the window's edge it went. Either slides in less than
// 200 ms, the button's slide being longer, and a drag back across the
// threshold turns the sidebar from where it is.
func TestSidebarDragHidesAndShows(t *testing.T) {
	h := newHarness(t)
	d := h.pressDivider(sidebarDefaultWidth)
	at := float32(sidebarDefaultWidth)
	for _, step := range []struct {
		to, sidebar float32
		slides      bool
	}{
		{100, sidebarMinWidth, false},
		{79, 0, true},
		{99, 0, false},
		{101, sidebarMinWidth, true},
		{170, 170, false},
		{120, sidebarMinWidth, false},
		{-150, 0, true},
		{99, 0, false},
		{101, sidebarMinWidth, true},
	} {
		list := h.find("Entries")
		d.by(step.to - at)
		at = step.to
		when := fmt.Sprintf("dragged to %v", at)
		if step.slides {
			if l := h.find("Entries"); !h.tt.FramePending() || !near(l.X, list.X) {
				t.Fatalf("%s: frames pending %v, the list went from %v to %v at once", when, h.tt.FramePending(), list, l)
			}
			h.tt.Advance(200 * time.Millisecond)
		}
		h.expectPanes(when, step.sidebar, listDefaultWidth)
		label := "Hide Sidebar"
		if step.sidebar == 0 {
			label = "Show Sidebar"
		}
		if h.tt.FramePending() || h.g.sidebarItem.Label != label {
			t.Fatalf("%s: frames pending %v, the menu item reads %q", when, h.tt.FramePending(), h.g.sidebarItem.Label)
		}
	}
	d.by(79 - at)
	h.tt.Advance(75 * time.Millisecond)
	h.tt.Frame()
	list := h.find("Entries")
	if list.X < 1 || list.X > sidebarMinWidth {
		t.Fatalf("75 ms into hiding, the list at %v is not on its way", list)
	}
	d.by(101 - 79)
	if l := h.find("Entries"); !near(l.X, list.X) {
		t.Fatalf("dragged back while hiding, the list went from %v to %v at once", list, l)
	}
	h.tt.Advance(200 * time.Millisecond)
	h.expectPanes("dragged back while hiding", sidebarMinWidth, listDefaultWidth)
	d.release()
	h.tt.Advance(time.Second)
	h.expectPanes("released", sidebarMinWidth, listDefaultWidth)
}

// The sidebar's divider takes a drag while the sidebar slides in, which sets
// the width it slides to.
func TestSidebarDividerDragWhileSliding(t *testing.T) {
	h := newHarness(t)
	h.clickSidebarButton("Hide Sidebar")
	h.click("Show Sidebar")
	h.tt.Advance(100 * time.Millisecond)
	s := h.find("Namespaces")
	h.dragDivider(s.X+s.W, 40)
	h.expectPanes("dragged while sliding in", sidebarDefaultWidth+40, listDefaultWidth)
}

// Dragging either divider leaves the focus where it was: the list keeps it,
// and with it the menu bar's commands for the chosen entry.
func TestDividerDragKeepsFocus(t *testing.T) {
	h := newHarness(t)
	h.click("URL")
	h.focusList()
	for _, d := range []struct {
		name string
		x    float32
	}{{"sidebar", sidebarDefaultWidth}, {"list", sidebarDefaultWidth + 30 + 1 + listDefaultWidth}} {
		h.dragDivider(d.x, 30)
		if !h.tt.Focused("Entries") || !h.g.entryItems.edit.IsEnabled() {
			t.Fatalf("after dragging the %s divider, the list is focused %v, Edit Entry enabled %v", d.name, h.tt.Focused("Entries"), h.g.entryItems.edit.IsEnabled())
		}
	}
}
