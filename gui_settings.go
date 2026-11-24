package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/agent"
	"github.com/Entwining/maskey/internal/stores"
)

// regions are Bitwarden's clouds, in the order the Settings window offers
// them.
var regions = []struct{ id, label string }{{"us", "United States"}, {"eu", "European Union"}}

// settings is the Settings window's state: the connection being added, and
// the change to this Mac's stores that runs.
type settings struct {
	// redraw redraws the Settings window, ask shows an alert on it, and
	// close and focus close it and bring it forward; all are nil while it
	// is closed.
	redraw       func()
	ask          func(alert) (int, error)
	close, focus func()
	// The connection to add. token is the access token, which leaves this
	// state as the connection starts.
	name, project, token string
	region               int
	// busy names the store being changed, or is adding while a connection
	// is made, and "" while nothing runs.
	busy string
	err  string
	// adding is whether the dialog that connects a store is open.
	adding bool
}

const adding = "+"

const addStoreWidth = 440

// changeStores runs a change to this Mac's stores off the UI thread, then
// shows its failure, or reloads the stores for both windows. A close or
// quit that waited for the change then goes ahead, unless it failed.
func (g *gui) changeStores(busy string, change func() error, done func()) {
	s := &g.settings
	s.busy, s.err = busy, ""
	g.start(func() func() {
		err := change()
		return func() {
			defer g.resumeExit()
			s.busy = ""
			if err != nil {
				s.err = sentence(err)
				g.exit = noExit
			} else {
				if done != nil {
					done()
				}
				g.refresh()
			}
			if s.redraw != nil {
				s.redraw()
			}
		}
	})
}

// connect adds the Bitwarden project the form describes. The service signs
// in with the token before it keeps anything.
func (g *gui) connect() {
	s := &g.settings
	addition := stores.Addition{ID: strings.TrimSpace(s.name), Type: stores.Bitwarden, Region: regions[s.region].id,
		Project: strings.TrimSpace(s.project), Token: []byte(strings.TrimSpace(s.token))}
	s.token = ""
	g.changeStores(adding, func() error {
		_, err := g.service.AddStore(g.ctx, addition)
		return err
	}, func() { s.name, s.project, s.adding = "", "", false })
}

// removeStore asks before it forgets a store, whose entries stay in
// Bitwarden.
func (g *gui) removeStore(id string) {
	s := &g.settings
	question := alert{message: fmt.Sprintf("Remove the store “%s” from this Mac?", id),
		detail:  "Its access token and its entries’ program access are removed from this Mac. The entries stay in Bitwarden.",
		buttons: []alertButton{{title: "Remove", destructive: true}, {title: "Cancel", isDefault: true, isCancel: true}}}
	// Closing the Settings window clears s.ask on the UI thread.
	ask, target := s.ask, g.target(id)
	g.start(func() func() {
		button, err := ask(question)
		return func() {
			// The question was about the connection the row showed.
			if err == nil && button == 0 && g.target(id) == target {
				g.changeStores(id, func() error { return g.service.ChangeStore(g.ctx, agent.OpRemoveStore, id) }, nil)
			}
		}
	})
}

// settingsView is the Settings window: this Mac's stores, and the dialog
// that connects a Bitwarden Secrets Manager project.
func (g *gui) settingsView(c *ui.Context) {
	c.SetTheme(theme(c.Theme()))
	t := c.Theme()
	s := &g.settings
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(t.Space(5)).Gap(t.Space(2.5)).Children(func() {
			rows := make([]func(), 0, len(g.stores.Stores)+1)
			for _, connection := range g.stores.All() {
				rows = append(rows, func() { g.storeRow(c, connection) })
			}
			card(c.Key("stores"), rows...)
			if s.err != "" && !s.adding {
				ui.Text(c, s.err).TextColor(t.Danger)
			}
			if ui.Button(c.Key("add"), "Add Bitwarden Store…").AlignSelf(ui.End).Margin(t.Space(1), 0, 0, 0).Disabled(s.busy != "").Clicked() {
				s.adding, s.err = true, ""
			}
		})
	})
	g.addStore(c)
}

// addStore is the dialog that connects a Bitwarden Secrets Manager project.
// It stays open while the connection is made, and after a failure.
func (g *gui) addStore(c *ui.Context) {
	t := c.Theme()
	s := &g.settings
	open := s.adding
	ui.Modal(c.Key("add-store"), &open, func() {
		ui.Column(c).Width(addStoreWidth).Gap(t.Space(3)).Children(func() {
			ui.Text(c, "Add a Bitwarden Store").FontSize(t.FontSize + 2).Bold()
			ui.Text(c, "Paste the access token of a machine account that can read and write one Secrets Manager project. It stays in this Mac’s Keychain.").TextColor(t.TextMuted).FontSize(t.FontSize - 1)
			field := func(label string, control func()) { ui.Field(c, label, control).TextColor(t.TextMuted) }
			input := func(e ui.Element) ui.Element {
				return e.Padding(inputPadding(t)).Background(t.Background).TextColor(t.Text)
			}
			ui.Form(c.Key("connect"), func() {
				field("Access Token", func() { input(ui.TextInput(c.Key("token"), &s.token)).Password().Font(monospace) })
				field("Name", func() { input(ui.TextInput(c.Key("name"), &s.name)).Placeholder(stores.Bitwarden) })
				field("Region", func() {
					ui.Segmented(c.Key("region"), &s.region, regions[0].label, regions[1].label).Label("Region").FillEqually()
				})
				field("Project", func() { input(ui.TextInput(c.Key("project"), &s.project)).Placeholder("Optional") })
			}).Gap(t.Space(2)).Margin(t.Space(1), 0).Disabled(s.busy != "")
			if s.err != "" {
				ui.Text(c, s.err).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).AlignSelf(ui.End).Children(func() {
				if s.busy == adding {
					ui.Spinner(c).Size(14, 14).Label("Connecting")
				}
				if ui.Button(c.Key("cancel"), "Cancel").Disabled(s.busy != "").Clicked() {
					open = false
				}
				if ui.PrimaryButton(c.Key("connect"), "Add").Disabled(s.busy != "" || strings.TrimSpace(s.token) == "").Clicked() {
					g.connect()
				}
			})
		})
	})
	// A connection that runs is not left: its answer needs the dialog.
	if !open && s.adding && s.busy == "" {
		s.adding, s.token, s.err = false, "", ""
	}
}

// storeRow is one store: its name and what it connects to, and a menu of
// the changes the owner can make to it.
func (g *gui) storeRow(c *ui.Context, connection stores.Connection) {
	t := c.Theme()
	s := &g.settings
	id := connection.ID
	isDefault, remote := id == g.stores.Default, connection.Type == stores.Bitwarden
	about := "iCloud Keychain"
	if remote {
		about = fmt.Sprintf("Bitwarden Secrets Manager · %s · %s", strings.ToUpper(connection.Region), connection.ProjectName)
	}
	ui.Row(c.Key(id)).Gap(8).AlignItems(ui.Center).Padding(t.Space(2), t.Space(3.5)).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, id).SingleLine()
			ui.Text(c, about).TextColor(t.TextMuted).FontSize(t.FontSize - 1).SingleLine()
		})
		if isDefault {
			ui.Text(c, "Default").TextColor(t.TextMuted).FontSize(t.FontSize - 1).Tooltip("Commands use this store unless --store names another")
		}
		if isDefault && !remote {
			return
		}
		change := func(op string) {
			g.changeStores(id, func() error { return g.service.ChangeStore(g.ctx, op, id) }, nil)
		}
		iconButton(c.Key("actions"), ellipsisIcon, "Actions for "+id, 24, s.busy == id, s.busy != "").TextColor(t.TextMuted).Menu(func(m *ui.Menu) {
			if !isDefault && m.Item("Make Default").Chosen() {
				change(agent.OpDefaultStore)
			}
			if !remote {
				return
			}
			if m.Item("Sync Now").Chosen() {
				change(agent.OpSyncStore)
			}
			m.Separator()
			if m.Item("Remove…").Danger().Chosen() {
				g.removeStore(id)
			}
		})
	})
}
