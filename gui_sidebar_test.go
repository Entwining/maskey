package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// trafficLights is room the window controls might take; the window reads
// the real room from AppKit.
var trafficLights = ui.TitleBar{Height: titleBarHeight, Left: 78}

// toggleSidebarFromMenu chooses View ▸ Hide Sidebar or Show Sidebar, which
// posts its command, as Window.Update does to redraw after it, and lets
// the sidebar finish sliding.
func (h *harness) toggleSidebarFromMenu() {
	h.t.Helper()
	item := h.g.sidebarItem
	item.Click(item, nil)
	(<-h.posted)()
	h.settle()
	h.tt.Advance(time.Second)
}

// clickSidebarButton clicks the sidebar's button, labeled label, and lets
// the sidebar finish sliding.
func (h *harness) clickSidebarButton(label string) {
	h.t.Helper()
	h.click(label)
	h.tt.Advance(time.Second)
}

// The sidebar's button and View ▸ Hide Sidebar both hide the sidebar and
// show it again, leaving the namespace it chose and the list it shows as
// they were.
func TestSidebarToggle(t *testing.T) {
	h := newHarness(t)
	h.click("db")
	item := h.g.sidebarItem
	expect := func(when string, hidden bool) {
		t.Helper()
		label := "Hide Sidebar"
		if hidden {
			label = "Show Sidebar"
		}
		if h.g.namespace != "db" || !h.tt.HasText("PASSWORD") || h.tt.HasText("API_TOKEN") {
			t.Fatalf("%s: namespace %q, showing %q", when, h.g.namespace, h.tt.Texts())
		}
		// The list's title is db, so All Entries is the sidebar's.
		if _, shown := h.tt.Find("All Entries"); shown == hidden {
			t.Fatalf("%s: the sidebar shows %v", when, shown)
		}
		if _, ok := h.tt.Find(label); !ok || item.Label != label || item.Accelerator != "Ctrl+Cmd+S" {
			t.Fatalf("%s: no %s button, or the menu item reads %q with key %q", when, label, item.Label, item.Accelerator)
		}
	}
	expect("at first", false)
	h.clickSidebarButton("Hide Sidebar")
	expect("after the button hid it", true)
	h.toggleSidebarFromMenu()
	expect("after the menu showed it", false)
	h.toggleSidebarFromMenu()
	expect("after the menu hid it", true)
	h.clickSidebarButton("Show Sidebar")
	expect("after the button showed it", false)
}

// Hiding and showing the sidebar with Control-Command-S leaves the focus
// where it was: typing in the search field or a form field goes on, and the
// list keeps its keys and the menu bar's commands for the chosen entry.
func TestSidebarToggleKeepsFocus(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		h := newHarness(t)
		h.typeInto("Search entries", "p")
		query := "p"
		for _, when := range []string{"hiding", "showing"} {
			h.toggleSidebarFromMenu()
			h.tt.Type("a")
			h.tt.Frame()
			query += "a"
			if !h.tt.Focused("Search entries") || h.g.query != query {
				t.Fatalf("after %s the sidebar, the search field is focused %v with %q", when, h.tt.Focused("Search entries"), h.g.query)
			}
		}
	})
	t.Run("list", func(t *testing.T) {
		h := newHarness(t)
		h.click("URL")
		h.focusList()
		for _, when := range []string{"hiding", "showing"} {
			h.toggleSidebarFromMenu()
			if !h.tt.Focused("Entries") || !h.g.entryItems.edit.IsEnabled() {
				t.Fatalf("after %s the sidebar, the list is focused %v, Edit Entry enabled %v", when, h.tt.Focused("Entries"), h.g.entryItems.edit.IsEnabled())
			}
		}
	})
	// Hidden, the sidebar cannot keep the focus, which goes to the list,
	// also when the sidebar hides without sliding.
	for _, reduceMotion := range []bool{false, true} {
		t.Run(fmt.Sprintf("sidebar, reduced motion %v", reduceMotion), func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetPreferences(ui.Preferences{ReduceMotion: reduceMotion})
			h.click("URL")
			h.click("All Entries")
			h.toggleSidebarFromMenu()
			if !h.tt.Focused("Entries") || !h.g.entryItems.edit.IsEnabled() {
				t.Fatalf("after hiding the sidebar, the list is focused %v, Edit Entry enabled %v", h.tt.Focused("Entries"), h.g.entryItems.edit.IsEnabled())
			}
			// The list takes the focus once, not whenever it moves on.
			h.typeInto("Search entries", "p")
			if !h.tt.Focused("Search entries") {
				t.Fatal("the search field lost the focus to the list")
			}
		})
	}
	t.Run("form", func(t *testing.T) {
		h := newHarness(t)
		h.choose("URL", "Edit")
		h.typeName("URL")
		name := "URL"
		for _, when := range []string{"hiding", "showing"} {
			h.toggleSidebarFromMenu()
			h.tt.Type("_")
			h.tt.Frame()
			name += "_"
			if h.g.form.name != name {
				t.Fatalf("after %s the sidebar, the name field holds %q", when, h.g.form.name)
			}
		}
	})
}

// textWidth is the width s takes on one line in the theme's font, larger
// points above its size.
func textWidth(s string, larger float32, weight int) float32 {
	var w float32
	ui.NewTester(func(c *ui.Context) {
		w, _ = c.MeasureText(0, ui.Span{Text: s, Size: c.Theme().FontSize + larger, Weight: weight})
	}, 100, 100)
	return w
}

// Without the sidebar, the list takes its place at the window's left edge,
// as wide as beside it, and its toolbar starts after the window controls,
// if any, with the sidebar's button, before a title that fits whole in the
// list's default width.
func TestHiddenSidebarLayout(t *testing.T) {
	for name, bar := range map[string]ui.TitleBar{"window": trafficLights, "full screen": {}} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetTitleBar(bar)
			h.typeInto("Search entries", "a")
			shown := h.find("Entries")
			h.clickSidebarButton("Hide Sidebar")
			button, title, add, list := h.find("Show Sidebar"), h.find("Search Results"), h.find("New Entry"), h.find("Entries")
			if list.X > 0.5 || !near(list.W, shown.W) {
				t.Fatalf("the list at %v is not at the window's left edge, %v wide as beside the sidebar", list, shown.W)
			}
			if button.X < bar.Left {
				t.Fatalf("the button at %v is under the window controls", button)
			}
			if title.W < textWidth("Search Results", 0, 700) {
				t.Fatalf("the title at %v is cut off", title)
			}
			if title.X < button.X+button.W || add.X < title.X+title.W || add.X+add.W > list.X+list.W {
				t.Fatalf("the button at %v, the title at %v, and New Entry at %v overlap or leave the list at %v", button, title, add, list)
			}
		})
	}
}

// Hiding and showing the sidebar slide it, its edge at the list's line,
// and its button without a jump, for longer than 200 ms, then stop asking
// for frames, in the states that Reduce Motion reaches at once. The list
// keeps its width throughout, and its toolbar starts after the button.
func TestSidebarAnimationSettles(t *testing.T) {
	for name, reduce := range map[string]bool{"motion": false, "reduce motion": true} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.tt.SetTitleBar(trafficLights)
			h.tt.SetPreferences(ui.Preferences{ReduceMotion: reduce})
			h.click("db")
			expect := func(when string, hidden bool) {
				t.Helper()
				list, button := h.find("Entries"), h.find(h.g.sidebarLabel())
				x, buttonX := float32(sidebarDefaultWidth+1), float32(sidebarDefaultWidth-12-sidebarButtonSize)
				if hidden {
					x, buttonX = 0, trafficLights.Left+8
				}
				// The list's title is db, so All Entries is the sidebar's.
				_, shown := h.tt.Find("All Entries")
				if h.tt.FramePending() || !near(list.X, x) || !near(list.W, listDefaultWidth) || !near(button.X, buttonX) || shown == hidden {
					t.Fatalf("%s: frames pending %v, the list at %v, the button at %v, the sidebar shown %v", when, h.tt.FramePending(), list, button, shown)
				}
				if sub := h.find("1 entry"); !near(sub.X, max(list.X+18, button.X+button.W+8)) {
					t.Fatalf("%s: the list's subtitle at %v does not start after the button at %v", when, sub, button)
				}
			}
			for _, step := range []struct {
				label  string
				hidden bool
			}{{"Hide Sidebar", true}, {"Show Sidebar", false}} {
				// Frame by frame, the list and the button move a little at a
				// time, and the list never changes width.
				list, button := h.find("Entries"), h.find(step.label)
				h.click(step.label)
				for i := 0; !reduce && i < 20; i++ {
					if (i == 0 || i == 12) && !h.tt.FramePending() {
						t.Fatalf("after %s, nothing moves %d frames later", step.label, i)
					}
					l, b := h.find("Entries"), h.find(h.g.sidebarLabel())
					if s, ok := h.tt.Find("Namespaces"); ok && s.W > 0 && !near(s.X+s.W, l.X-1) {
						t.Fatalf("after %s, the sidebar at %v does not end at the list at %v", step.label, s, l)
					}
					if max(l.X-list.X, list.X-l.X) > 40 || max(b.X-button.X, button.X-b.X) > 40 || !near(l.W, listDefaultWidth) {
						t.Fatalf("after %s, the list went from %v to %v and the button from %v to %v in a frame", step.label, list, l, button, b)
					}
					list, button = l, b
					h.tt.Advance(time.Second / 60)
				}
				expect("after "+step.label, step.hidden)
			}
		})
	}
}

// The sidebar's button is one element wherever it shows, so it keeps the
// keyboard focus as the sidebar hides and shows, and Space presses it again.
func TestSidebarButtonKeepsFocus(t *testing.T) {
	h := newHarness(t)
	h.tt.SetTitleBar(trafficLights)
	h.clickSidebarButton("Hide Sidebar")
	for _, hidden := range []bool{true, false, true} {
		if label := h.g.sidebarLabel(); h.g.sidebarHidden != hidden || !h.tt.Focused(label) {
			t.Fatalf("hidden %v, %s focused %v", h.g.sidebarHidden, label, h.tt.Focused(label))
		}
		h.tt.Key(0, ui.KeySpace)
		h.tt.Advance(time.Second)
	}
}

// Once hidden, the sidebar takes neither the focus, keys, nor the pointer:
// Tab passes it by, keys typed after hiding it while it had the focus leave
// its namespace as it was, and a drag at the window's left edge, where its
// divider was, leaves it hidden.
func TestHiddenSidebarTakesNoKeys(t *testing.T) {
	h := newHarness(t)
	h.click("db")
	if !h.tt.Focused("Namespaces") {
		t.Fatal("choosing db did not focus the sidebar")
	}
	h.toggleSidebarFromMenu()
	h.tt.Key(0, ui.KeyUp)
	h.tt.Frame()
	if h.g.namespace != "db" {
		t.Fatalf("Up chose %q in the hidden sidebar", h.g.namespace)
	}
	for i := 0; i < 8; i++ {
		h.tt.Key(0, ui.KeyTab)
		h.tt.Frame()
		if h.tt.Focused("Namespaces") {
			t.Fatalf("Tab %d focused the hidden sidebar", i+1)
		}
	}
	d := h.pressDivider(-2)
	d.by(10)
	d.release()
	h.tt.Advance(time.Second)
	if !h.g.sidebarHidden {
		t.Fatal("a drag at the window's left edge showed the sidebar")
	}
}
