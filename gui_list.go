package main

import (
	"slices"
	"strconv"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
)

func (g *gui) sidebar(c *ui.Context, vibrant bool) {
	t := c.Theme()
	ui.Box(c).Height(titleBarHeight).Shrink(0).DragWindow()
	// Nothing is chosen while a search shows every namespace's entries;
	// choosing an item ends the search.
	chosen := g.namespace
	if g.query != "" {
		chosen = ""
	}
	count := func(n int) func() {
		return func() { ui.Text(c, strconv.Itoa(n)).FontSize(t.FontSize - 2).Opacity(0.55) }
	}
	list := ui.Sidebar(c.Key("namespaces"), &chosen, func() {
		ui.SidebarItem(c.Key(allNamespaces), allNamespaces, layersIcon, "All Entries").Children(count(len(g.refs)))
		if len(g.namespaces) > 0 {
			ui.SidebarSection(c.Key("section"), "Namespaces", nil, func() {
				for _, n := range g.namespaces {
					ui.SidebarItem(c.Key(n.Name), n.Name, folderKeyIcon, n.Name).Children(count(n.Entries))
				}
			})
		}
	}).Grow(1).Label("Namespaces").Disabled(g.form.open)
	if vibrant {
		list.Background(ui.Transparent)
	}
	if list.Changed() {
		g.showNamespace(chosen)
	}
}

func (g *gui) listPane(c *ui.Context) {
	t := c.Theme()
	rows := g.rows()
	toolbar(c, func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			title, subtitle := "All Entries", entriesLabel(len(g.refs))
			if g.query != "" {
				title = "Search Results"
				subtitle = entriesLabel(len(rows)) + " in all namespaces"
			} else if i := slices.IndexFunc(g.namespaces, func(n store.Namespace) bool { return n.Name == g.namespace }); i >= 0 {
				title, subtitle = g.namespace, entriesLabel(g.namespaces[i].Entries)
			}
			if g.loaded {
				ui.Text(c, title).FontSize(t.FontSize).Bold().SingleLine()
				ui.Text(c, subtitle).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
			}
		})
		if iconButton(c.Key("add"), plusIcon, "New Entry", 28, false, g.form.open).TextColor(t.TextMuted).Clicked() {
			g.openNew()
		}
	})
	if g.listErr != "" && len(g.namespaces) > 0 {
		ui.Row(c).Gap(12).Padding(8, 16).AlignItems(ui.Center).Background(t.Danger.Alpha(0.1)).Children(func() {
			ui.Text(c, "Couldn’t reload: "+g.listErr).FontSize(t.FontSize - 1).Wrap().Grow(1).MinWidth(0)
			if ui.Button(c, "Try Again").Clicked() {
				g.refresh()
			}
		})
	}
	switch {
	case !g.loaded:
		ui.Column(c).Grow(1).Center().Children(func() { ui.Spinner(c).Size(16, 16).Label("Loading") })
	case len(g.namespaces) == 0 && g.listErr != "":
		emptyState(c, keyIcon, "Can’t Read the Keychain", g.listErr, "Try Again", g.refresh)
	case len(g.namespaces) == 0:
		emptyState(c, keyIcon, "No Entries", "Add an API key or token here, then hand it to a command with mk run.", "New Entry", g.openNew)
	default:
		g.entries(c, rows)
	}
}

func entriesLabel(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return strconv.Itoa(n) + " entries"
}

func (g *gui) entries(c *ui.Context, refs []store.Ref) {
	t := c.Theme()
	g.row = slices.Index(refs, g.entry)
	// The list's keys act on the chosen entry, so a search hiding it unchooses
	// it.
	if g.row < 0 && g.entry != (store.Ref{}) {
		g.entry = store.Ref{}
		g.selectionChanged()
	}
	if len(refs) == 0 {
		if g.query != "" {
			emptyState(c, searchIcon, "No Results", "No entry or namespace matches “"+g.query+"”.", "", nil)
		}
		return
	}
	g.list.Key = func(i int) any { return refs[i] }
	g.list.Label = func(i int) string { return refs[i].Name }
	g.list.Selected = &g.row
	g.list.OnShortcut(c, 0, ui.KeySpace, func() {
		if g.entry != (store.Ref{}) && g.canReveal(g.entry) {
			g.toggleReveal()
		}
	})
	g.list.OnShortcut(c, ui.Cmd, ui.KeyC, func() {
		if g.entry != (store.Ref{}) && g.canCopy(g.entry) {
			g.copy()
		}
	})
	g.list.OnShortcut(c, ui.Cmd, ui.KeyBackspace, func() {
		if g.entry != (store.Ref{}) && g.writeAction == "" {
			g.askDelete(g.entry)
		}
	})
	// Rows name their namespace wherever the list mixes namespaces.
	mixed := g.query != "" || g.namespace == allNamespaces
	// The padding matches the sidebar's, so the first rows of both start at
	// the same height.
	list := ui.List(c.Key("entries"), &g.list, len(refs)).Grow(1).Gap(t.Space(0.5)).Padding(t.Space(2.5)).Label("Entries").Disabled(g.form.open)
	list.Rows(func(row ui.ListRow) {
		g.entryRow(row.Context, refs[row.Index], row.Selected(), row.ListFocused(), mixed)
	})
	if list.Submitted() {
		g.openEdit()
	}
	if g.row >= 0 && g.row < len(refs) && refs[g.row] != g.entry {
		g.entry = refs[g.row]
		g.selectionChanged()
	}
	// The list follows an existing row when rows move. Choose a newly
	// written entry only after one frame has built the list containing it.
	if i := slices.Index(refs, g.pick); i >= 0 {
		g.entry, g.pick = g.pick, store.Ref{}
		g.selectionChanged()
		g.list.ScrollIntoView(i)
		c.Invalidate()
	}
}

// entryRow shows one entry as the sidebar shows a namespace, with the same
// height, horizontal padding and highlight.
func (g *gui) entryRow(c *ui.Context, ref store.Ref, selected, focused, mixed bool) {
	t := c.Theme()
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
	row.TextColor(text).ContextMenu(func(m *ui.Menu) { g.entryMenu(m, ref) })
	deleting := g.writing(ref) && g.writeAction == "delete"
	if deleting {
		row.Opacity(0.5)
	}
	row.Children(func() {
		entryTile(c, ref.Namespace, g.kind(ref), 16)
		// Monospace glyphs look as large as the sidebar's text a point smaller.
		ui.Text(c, ref.Name).Font(monospace).FontSize(t.FontSize - 1).SingleLine().Grow(1).MinWidth(0)
		switch {
		case deleting:
			ui.Spinner(c).Size(12, 12).Label("Delete in progress")
		case mixed:
			ui.Text(c, ref.Namespace).FontSize(t.FontSize - 1).TextColor(muted).SingleLine().MaxWidth(listWidth / 2)
		}
	})
}

// entryMenu is the row's context menu.
func (g *gui) entryMenu(m *ui.Menu, ref store.Ref) {
	show := "Show Value"
	if g.showing(ref) {
		show = "Hide Value"
	}
	if m.Item(show).Shortcut(0, ui.KeySpace).Disabled(!g.canReveal(ref)).Chosen() {
		g.choose(ref)
		g.toggleReveal()
	}
	if m.Item("Copy Value").Shortcut(ui.Cmd, ui.KeyC).Disabled(!g.canCopy(ref)).Chosen() {
		g.choose(ref)
		g.copy()
	}
	m.Separator()
	if m.Item("Edit").Shortcut(0, ui.KeyEnter).Disabled(g.writing(ref)).Chosen() {
		g.choose(ref)
		g.openEdit()
	}
	m.Separator()
	if m.Item("Delete…").Shortcut(ui.Cmd, ui.KeyBackspace).Disabled(g.writeAction != "").Chosen() {
		g.choose(ref)
		g.askDelete(ref)
	}
}
