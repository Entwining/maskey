package main

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
	"github.com/Entwining/maskey/internal/stores"
)

// sidebar lists its first item and then each store's namespaces under the
// store's ID, as Mail lists each account's mailboxes. The ID heads them
// whenever the sidebar shows several stores, also while only one has
// entries, so that a title does not change as entries come and go. With one
// store to show, this Mac's only one or the one chosen of several, there is
// none to tell apart, so its section is just Namespaces.
func (g *gui) sidebar(c *ui.Context, vibrant bool) {
	t := c.Theme()
	ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
	// Nothing is chosen while a search shows every namespace's entries;
	// choosing an item ends the search.
	chosen := g.scope.item()
	if g.query != "" {
		chosen = ""
	}
	count := func(n int) func() {
		return func() { ui.Text(c, strconv.Itoa(n)).FontSize(t.FontSize - 2).Opacity(0.55) }
	}
	list := ui.Sidebar(c.Key("namespaces"), &chosen, func() {
		first := func() ui.Element {
			return ui.SidebarItem(c.Key(allNamespaces), allNamespaces, layersIcon, cmp.Or(g.filter, "All Entries")).Children(count(g.count()))
		}
		if g.severalStores() {
			g.storeChoice(c, first, chosen == allNamespaces)
		} else {
			first()
		}
		several := g.severalStores() && g.filter == ""
		for _, connection := range g.stores.All() {
			id, title := connection.ID, connection.ID
			s := g.listed[id]
			// A store has a section once it has something to show: its
			// namespaces, or that it could not be listed.
			failed := g.severalStores() && s.err != ""
			if !g.shown(id) || len(s.namespaces) == 0 && !failed {
				continue
			}
			if !several {
				title = "Namespaces"
			}
			ui.SidebarSection(c.Key("store:"+id), title, nil, func() {
				for _, n := range s.namespaces {
					item := scope{id, n.Name}.item()
					ui.SidebarItem(c.Key(item), item, folderKeyIcon, n.Name).Children(count(n.Entries))
				}
				// Not an item: it takes neither a click nor the keys.
				if failed {
					ui.Text(c, "Couldn’t "+s.attempt()).FontSize(t.FontSize-1).TextColor(t.TextMuted).SingleLine().Padding(t.Space(1), t.Space(2))
				}
			})
		}
	}).Bind(&g.sidebarList).Grow(1).Label("Namespaces")
	if vibrant {
		list.Background(ui.Transparent)
	}
	// Until the open form is left, chosen, rebuilt from g.scope, stays.
	if list.Changed() {
		to := scopeOf(chosen)
		g.leave(func() { g.showScope(to) })
	}
	if g.update != "" {
		g.updateButton(c)
	}
}

// storeChoice is the sidebar's first item, which first builds and chosen
// says is chosen, with the menu at its end that chooses what the window
// shows: every store, or one alone. The item names the choice and shows all
// of its entries, as All Entries does on a Mac with one store. The menu's
// button lies over the item's end rather than inside the item, whose
// content assistive technology reads only as the item's name, so that the
// button is an element of its own there. It is as high as the item and as
// wide, so that it is easy to hit.
func (g *gui) storeChoice(c *ui.Context, first func() ui.Element, chosen bool) {
	t := c.Theme()
	size := 16 + 2*t.Space(1.5) // a sidebar item's height, as in entryRow
	ui.Box(c).Shrink(0).Children(func() {
		// The item's count ends before the button.
		first().PaddingEnd(size + t.Space(1))
		b := ui.ButtonBase(c.Key("stores")).Absolute().Top(0).End(0).Size(size, size).Radius(t.Radius).Label("Stores Shown").Tooltip("Stores Shown")
		// The glyph takes the item's color, which the item does not pass
		// on to an element outside it, and on the accent, the hover and
		// press backgrounds lighten as a gray would not.
		tint := neutral
		if chosen && g.sidebarList.Focused(c) {
			b.TextColor(t.AccentText)
			tint = t.AccentText
		}
		if b.Pressed() {
			b.Background(tint.Alpha(0.22))
		} else if b.Hovered() {
			b.Background(tint.Alpha(0.13))
		}
		b.Children(func() { ui.Icon(c, chevronsIcon).Size(12, 12).Opacity(0.55) })
		b.Menu(func(m *ui.Menu) {
			if m.Item("All Stores").Radio(g.filter == "").Chosen() {
				g.leave(func() { g.showStore("") })
			}
			m.Separator()
			for _, connection := range g.stores.All() {
				if id := connection.ID; m.Item(id).Radio(g.filter == id).Chosen() {
					g.leave(func() { g.showStore(id) })
				}
			}
		})
	})
}

// listPane is the list and its toolbar, whose content starts after inset,
// the room the window controls and the sidebar's button take over its left
// edge.
func (g *gui) listPane(c *ui.Context, inset float32) {
	t := c.Theme()
	rows, total := g.rows(), g.count()
	// With no entry to show, the pane waits for the stores still loading
	// before it says there is none.
	loaded := g.storesRead
	var failed []string
	for _, connection := range g.stores.All() {
		if !g.shown(connection.ID) {
			continue
		}
		s := g.listed[connection.ID]
		loaded = loaded && s.loaded
		if s.err != "" {
			failed = append(failed, connection.ID)
		}
	}
	loaded = loaded || total > 0 || g.storesErr != ""
	toolbar(c, inset, func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			title, subtitle := cmp.Or(g.filter, "All Entries"), entriesLabel(total)
			if g.query != "" {
				title = "Search Results"
				found := 0
				for _, row := range rows {
					if !isHeading(row) {
						found++
					}
				}
				subtitle = entriesLabel(found) + " in all namespaces"
				if g.filter != "" {
					subtitle += " of " + g.filter
				}
			} else if s := g.listed[g.scope.store]; s != nil {
				if i := slices.IndexFunc(s.namespaces, func(n store.Namespace) bool { return n.Name == g.scope.namespace }); i >= 0 {
					title, subtitle = g.scope.namespace, entriesLabel(s.namespaces[i].Entries)
					if g.severalStores() {
						subtitle += " in " + g.scope.store
					}
				}
			}
			if loaded {
				ui.Text(c, title).FontSize(t.FontSize).Bold().SingleLine()
				ui.Text(c, subtitle).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
			}
		})
		if iconButton(c.Key("add"), plusIcon, "New Entry", 28, false, !g.storesRead).TextColor(t.TextMuted).Clicked() {
			g.openNew()
		}
	})
	banner := func(key, message string) {
		ui.Row(c.Key(key)).Gap(12).Padding(8, 16).AlignItems(ui.Center).Background(t.Danger.Alpha(0.1)).Children(func() {
			ui.Text(c, message).FontSize(t.FontSize - 1).Grow(1).MinWidth(0)
			if ui.Button(c, "Try Again").Clicked() {
				g.refresh()
			}
		})
	}
	if total > 0 {
		if g.storesErr != "" {
			banner("stores", "Couldn’t reload: "+g.storesErr)
		}
		for _, id := range failed {
			s, named := g.listed[id], ""
			if g.severalStores() {
				named = " " + id
			}
			banner("store:"+id, "Couldn’t "+s.attempt()+named+": "+s.err)
		}
	}
	switch {
	case total > 0:
		g.entries(c, rows)
	case !loaded || g.storesErr != "":
		g.storesUnread(c)
	case len(failed) > 1:
		details := make([]string, len(failed))
		for i, id := range failed {
			details[i] = id + ": " + g.listed[id].err
		}
		emptyState(c, keyIcon, "Can’t Read the Stores", strings.Join(details, "\n"), "Try Again", g.refresh)
	case len(failed) == 1:
		title := "Can’t Read the Keychain"
		if failed[0] != stores.ICloud {
			title = "Can’t Read the Store"
		}
		emptyState(c, keyIcon, title, g.listed[failed[0]].err, "Try Again", g.refresh)
	default:
		emptyState(c, keyIcon, "No Entries", "Add an API key or token here, then hand it to a command with mk run.", "New Entry", g.openNew)
	}
}

// storesUnread stands in for what needs this Mac's stores: it waits while
// they load, and says why reading them failed.
func (g *gui) storesUnread(c *ui.Context) {
	if g.storesErr != "" {
		emptyState(c, keyIcon, "Can’t Read This Mac’s Stores", g.storesErr, "Try Again", g.refresh)
		return
	}
	ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(16, 16).Label("Loading") })
}

// sidebarButton floats over the toolbars at x, level with the window
// controls. It is one element whichever pane it is over, so that it moves
// with the sidebar and keeps the focus as the sidebar hides and shows.
func (g *gui) sidebarButton(c *ui.Context, x float32) {
	b := iconButton(c.Key("sidebar"), panelLeftIcon, g.sidebarLabel(), sidebarButtonSize, false, false).TextColor(c.Theme().TextMuted)
	if b.Absolute().Left(x).Top((titleBarHeight - 1 - sidebarButtonSize) / 2).Clicked() {
		g.toggleSidebar()
	}
}

func (g *gui) sidebarLabel() string {
	if g.sidebarHidden {
		return "Show Sidebar"
	}
	return "Hide Sidebar"
}

func (g *gui) toggleSidebar() {
	g.sidebarHidden = !g.sidebarHidden
	g.sidebarItem.SetLabel(g.sidebarLabel())
}

func entriesLabel(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return strconv.Itoa(n) + " entries"
}

func (g *gui) entries(c *ui.Context, rows []entryID) {
	t := c.Theme()
	// The list's keys act on the chosen entry, so a search hiding it unchooses
	// it, except the open form's, which the form keeps chosen.
	g.row = slices.Index(rows, g.entry)
	if !g.form.open && g.entry != (entryID{}) && g.row < 0 {
		g.entry = entryID{}
		g.selectionChanged()
	}
	row := g.row
	if len(rows) == 0 {
		if g.query != "" {
			emptyState(c, searchIcon, "No Results", "No entry or namespace matches “"+g.query+"”.", "", nil)
		}
		return
	}
	g.list.Key = func(i int) any { return rows[i] }
	g.list.Label = func(i int) string {
		if isHeading(rows[i]) {
			return rows[i].store
		}
		return rows[i].ref.Name
	}
	// The list neither chooses a heading nor stops the keys on one.
	g.list.Header = func(i int) bool { return isHeading(rows[i]) }
	g.list.Selected = &g.row
	// A held key acts once: its repeats would show and hide the value, and
	// read it, over and over.
	pressed := func(action func()) func() {
		return func() {
			if !c.ShortcutRepeated() {
				action()
			}
		}
	}
	g.list.OnShortcut(c, 0, ui.KeySpace, pressed(g.revealChosen))
	g.list.OnShortcut(c, ui.Cmd, ui.KeyC, pressed(g.copyChosen))
	g.list.OnShortcut(c, ui.Cmd, ui.KeyBackspace, pressed(g.deleteChosen))
	// Rows name their namespace wherever the list mixes namespaces.
	mixed := g.query != "" || g.scope == (scope{})
	// The padding matches the sidebar's, so the first rows of both start at
	// the same height.
	list := ui.List(c.Key("entries"), &g.list, len(rows)).Grow(1).Gap(t.Space(0.5)).Padding(t.Space(2.5)).Label("Entries")
	list.Rows(func(row ui.ListRow) {
		if id := rows[row.Index]; isHeading(id) {
			storeHeading(row.Context, id.store, row.Index > 0)
		} else {
			g.entryRow(row.Context, id, row.Selected(), row.ListFocused(), mixed)
		}
	})
	if list.Submitted() {
		g.editChosen()
	}
	if g.row != row && g.row >= 0 && g.row < len(rows) {
		id := rows[g.row]
		g.leave(func() { g.choose(id) })
		// Until the form is left, the next pass puts the highlight back.
		c.Invalidate()
	}
	// The list follows an existing row when rows move. Choose a newly
	// written entry only after one frame has built the list containing it.
	if i := slices.Index(rows, g.pick); i >= 0 {
		g.entry, g.pick = g.pick, entryID{}
		g.selectionChanged()
		g.list.ScrollIntoView(i)
		c.Invalidate()
	}
}

// storeHeading heads a store's entries in a list of several stores', as
// the sidebar's section title heads the store's namespaces, with the same
// text and the same room above every heading after the first. The list
// keeps the heading of the entries at its top in view, over the rows that
// scroll under it, which its background hides.
func storeHeading(c *ui.Context, id string, after bool) {
	t := c.Theme()
	top := t.Space(1)
	if after {
		top += t.Space(3)
	}
	ui.Row(c).AlignItems(ui.Center).Padding(top, t.Space(2), t.Space(1), t.Space(2)).Background(t.Background).Children(func() {
		ui.Text(c, id).FontSize(t.FontSize - 1).FontWeight(600).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
	})
}

// entryRow shows one entry as the sidebar shows a namespace, with the same
// height, horizontal padding and highlight.
func (g *gui) entryRow(c *ui.Context, id entryID, selected, focused, mixed bool) {
	t := c.Theme()
	ref := id.ref
	// A sidebar item's 16-point icon sets its height.
	row := ui.Row(c).MinHeight(16+2*t.Space(1.5)).Padding(0, t.Space(2)).Gap(8).Radius(t.Radius).AlignItems(ui.Center)
	text, muted := t.Text, t.TextMuted
	switch {
	case selected && focused:
		row.Background(t.Accent)
		text, muted = t.AccentText, t.AccentText.Alpha(0.7)
	case selected:
		row.Background(t.SurfacePressed)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
	row.TextColor(text).ContextMenu(func(m *ui.Menu) { g.rowMenu(m, id) })
	deleting := g.writing(id) && g.write.action == deleteWrite
	if deleting {
		row.Opacity(0.5)
	}
	row.Children(func() {
		entryTile(c, ref.Namespace, g.kind(id), 16)
		// Monospace glyphs look as large as the sidebar's text a point smaller.
		ui.Text(c, ref.Name).Font(monospace).FontSize(t.FontSize - 1).SingleLine().Grow(1).MinWidth(0)
		switch {
		case deleting:
			ui.Spinner(c).Size(12, 12).Label("Delete in progress")
		case mixed:
			ui.Text(c, ref.Namespace).FontSize(t.FontSize - 1).TextColor(muted).SingleLine().MaxWidth(g.listWidth / 2)
		}
	})
}

// rowMenu is the row's context menu. Like other context menus on macOS, it
// shows no keys; the menu bar's Edit and View menus do.
func (g *gui) rowMenu(m *ui.Menu, id entryID) {
	show := "Show Value"
	if g.showing(id) {
		show = "Hide Value"
	}
	if m.Item(show).Disabled(!g.canReveal(id)).Chosen() {
		g.choose(id)
		g.toggleReveal()
	}
	// Copying and deleting another entry leave the open form, and so the
	// entry chosen, as they are.
	if m.Item("Copy Value").Disabled(!g.canCopy(id)).Chosen() {
		if !g.form.open {
			g.choose(id)
		}
		g.copy(id)
	}
	m.Separator()
	if m.Item("Edit").Disabled(g.writing(id)).Chosen() {
		g.openEdit(id)
	}
	m.Separator()
	if m.Item("Delete…").Disabled(g.write != nil).Chosen() {
		if !g.form.open {
			g.choose(id)
		}
		g.askDelete(id)
	}
}

func (g *gui) revealChosen() {
	if g.entry != (entryID{}) && g.canReveal(g.entry) {
		g.toggleReveal()
	}
}

func (g *gui) copyChosen() {
	if g.entry != (entryID{}) && g.canCopy(g.entry) {
		g.copy(g.entry)
	}
}

func (g *gui) editChosen() { g.openEdit(g.entry) }

func (g *gui) deleteChosen() {
	if g.entry != (entryID{}) && g.write == nil {
		g.askDelete(g.entry)
	}
}

// entryItems are the menu bar's commands for the chosen entry, in the Edit
// and View menus. They act as the list's keys do, on the chosen entry while
// the list has the keyboard focus; Edit ▸ Copy copies its value there, as
// Command-C does.
type entryItems struct{ reveal, edit, delete *mygo.MenuItem }

func newEntryItems(g *gui) entryItems {
	item := func(label, accelerator string, run func()) *mygo.MenuItem {
		return &mygo.MenuItem{Label: label, Accelerator: accelerator, Disabled: true, Click: g.onMenu(run)}
	}
	return entryItems{
		reveal: item("Show Value", "CmdOrCtrl+Y", g.revealChosen),
		edit:   item("Edit Entry", "CmdOrCtrl+L", g.editChosen),
		delete: item("Delete…", "", g.deleteChosen),
	}
}

// updateEntryItems enables the entry's commands that apply now. The menu
// bar takes a disabled item's key from the focused text field too, so
// Delete… has its key only while enabled: a text field takes
// Command-Backspace to delete to the line's start.
func (g *gui) updateEntryItems(listFocused bool) {
	id, m := g.entry, g.entryItems
	chosen := listFocused && id != (entryID{})
	show := "Show Value"
	if g.showing(id) {
		show = "Hide Value"
	}
	if m.reveal.Label != show {
		m.reveal.SetLabel(show)
	}
	enableItem(m.reveal, chosen && g.canReveal(id), "")
	enableItem(m.edit, chosen && g.canEdit(id), "")
	enableItem(m.delete, chosen && g.write == nil, "CmdOrCtrl+Backspace")
}

// enableItem enables or disables item, and gives it its key, unless "",
// only while it is enabled.
func enableItem(item *mygo.MenuItem, enabled bool, key string) {
	if item.IsEnabled() == enabled {
		return
	}
	item.SetEnabled(enabled)
	if key == "" {
		return
	}
	if !enabled {
		key = ""
	}
	item.SetAccelerator(key)
}
