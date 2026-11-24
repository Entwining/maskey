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

// settings is the Settings window's state: the connection being added or
// the store whose access token is being replaced, and the change to this
// Mac's stores that runs.
type settings struct {
	// redraw redraws the Settings window, ask shows an alert on it, and
	// close and focus close it and bring it forward; all are nil while it
	// is closed.
	redraw       func()
	ask          func(alert) (int, error)
	close, focus func()
	// The connection to add. token is its access token, or the new one of
	// the connection replacing, which leaves this state as the change
	// starts.
	name, project, token string
	region               int
	// tokenInput numbers the token's text input, which starts anew once the
	// token is cleared: Undo in the old one would type the token again.
	tokenInput int
	// busy names the store being changed, or is adding while a connection
	// is made, and "" while nothing runs.
	busy string
	err  string
	// adding is whether the dialog that connects a store is open, and
	// replacing is the connection whose dialog replaces its access token,
	// as the dialog opened for it; its ID is "" while none is open.
	adding    bool
	replacing stores.Connection
}

// clearToken forgets the access token typed.
func (s *settings) clearToken() {
	s.token = ""
	s.tokenInput++
}

const adding = "+"

const tokenDialogWidth = 440

// changeStores runs a change to this Mac's stores off the UI thread, then
// shows its failure, in the Settings window opened again if it was closed
// meanwhile, or reloads the stores for both windows. A close or quit that
// waited for the change then goes ahead, unless it failed.
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
				if s.redraw == nil {
					g.showSettings()
				}
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
	s.clearToken()
	g.changeStores(adding, func() error {
		_, err := g.service.AddStore(g.ctx, addition)
		return err
	}, func() { s.name, s.project, s.adding = "", "", false })
}

// replaceToken gives the connection replacing the new access token the
// dialog holds. The service replaces the old one only while the store's
// name still means that connection, and once it has signed in with the new
// one and checked that it reaches the store's project.
func (g *gui) replaceToken() {
	s := &g.settings
	connection, token := s.replacing, []byte(strings.TrimSpace(s.token))
	s.clearToken()
	g.changeStores(connection.ID, func() error {
		return g.service.SetStoreToken(g.ctx, connection.ID, connection.Target(), token)
	}, func() { s.replacing = stores.Connection{} })
}

// removeStore asks before it forgets a store, whose entries stay in
// Bitwarden. The answer is about the connection the row showed, which the
// service removes only while the name still means it.
func (g *gui) removeStore(connection stores.Connection) {
	s := &g.settings
	question := alert{message: fmt.Sprintf("Remove the store “%s” from this Mac?", connection.ID),
		detail:  "Its access token and its entries’ program access are removed from this Mac. The entries stay in Bitwarden.",
		buttons: []alertButton{{title: "Remove", destructive: true}, {title: "Cancel", isDefault: true, isCancel: true}}}
	// Closing the Settings window clears s.ask on the UI thread.
	ask := s.ask
	g.start(func() func() {
		button, err := ask(question)
		return func() {
			if err == nil && button == 0 {
				g.changeStores(connection.ID, func() error { return g.service.RemoveStore(g.ctx, connection) }, nil)
			}
		}
	})
}

// settingsView is the Settings window: this Mac's stores, and the dialogs
// that connect a Bitwarden Secrets Manager project and replace a store's
// access token.
func (g *gui) settingsView(c *ui.Context) {
	c.SetTheme(theme(c.Theme()))
	t := c.Theme()
	s := &g.settings
	// Until this Mac's stores are read, the stores the window starts with
	// are not known to be this Mac's.
	if !g.storesRead {
		g.storesUnread(c)
		return
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(t.Space(5)).Gap(t.Space(2.5)).Children(func() {
			rows := make([]func(), 0, len(g.stores.Stores)+1)
			for _, connection := range g.stores.All() {
				rows = append(rows, func() { g.storeRow(c, connection) })
			}
			card(c.Key("stores"), rows...)
			if s.err != "" && !s.adding && s.replacing.ID == "" {
				ui.Text(c, s.err).TextColor(t.Danger)
			}
			if ui.Button(c.Key("add"), "Add Bitwarden Store…").AlignSelf(ui.End).Margin(t.Space(1), 0, 0, 0).Disabled(s.busy != "").Clicked() {
				s.adding, s.err = true, ""
			}
		})
	})
	g.addStore(c)
	g.replaceStoreToken(c)
}

// tokenDialog is a dialog that hands an access token to the Maskey service,
// shown while shown holds: its title, what it does, the fields that fields
// builds, the change's error, and Cancel and the button labeled action,
// which runs act once a token is typed. While the change runs, under a
// spinner labeled working, the dialog takes no input and stays open; once
// left otherwise, it forgets the token and the error, and dismiss closes it.
func (g *gui) tokenDialog(c *ui.Context, key string, shown bool, dismiss func(), title, about string, fields func(), working, action string, act func()) {
	t := c.Theme()
	s := &g.settings
	open := shown
	ui.Modal(c.Key(key), &open, func() {
		ui.Column(c).Width(tokenDialogWidth).Gap(t.Space(3)).Children(func() {
			ui.Text(c, title).FontSize(t.FontSize + 2).Bold()
			ui.Text(c, about).TextColor(t.TextMuted).FontSize(t.FontSize - 1)
			ui.Form(c.Key("fields"), fields).Gap(t.Space(2)).Margin(t.Space(1), 0).Disabled(s.busy != "")
			if s.err != "" {
				ui.Text(c, s.err).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).AlignSelf(ui.End).Children(func() {
				if s.busy != "" {
					ui.Spinner(c).Size(14, 14).Label(working)
				}
				if ui.Button(c.Key("cancel"), "Cancel").Disabled(s.busy != "").Clicked() {
					open = false
				}
				if ui.PrimaryButton(c.Key("act"), action).Disabled(s.busy != "" || strings.TrimSpace(s.token) == "").Clicked() {
					act()
				}
			})
		})
	})
	// A change that runs is not left: its answer needs the dialog.
	if !open && shown && s.busy == "" {
		s.clearToken()
		s.err = ""
		dismiss()
	}
}

// dialogField is a field of a dialog, with a muted label, and dialogInput a
// text input in it.
func dialogField(c *ui.Context, label string, control func()) {
	ui.Field(c, label, control).TextColor(c.Theme().TextMuted)
}

func dialogInput(c *ui.Context, e ui.Element) ui.Element {
	t := c.Theme()
	return e.Padding(inputPadding(t)).Background(t.Background).TextColor(t.Text)
}

// tokenField is the field the access token is pasted in, hidden as typed.
func (g *gui) tokenField(c *ui.Context, label string) {
	dialogField(c, label, func() {
		dialogInput(c, ui.TextInput(c.Key(g.settings.tokenInput), &g.settings.token)).Password().Font(monospace)
	})
}

// addStore is the dialog that connects a Bitwarden Secrets Manager project.
// It stays open after a failure.
func (g *gui) addStore(c *ui.Context) {
	s := &g.settings
	g.tokenDialog(c, "add-store", s.adding, func() { s.adding = false }, "Add a Bitwarden Store",
		"Paste the access token of a machine account that can read and write one Secrets Manager project. It stays in this Mac’s Keychain.",
		func() {
			g.tokenField(c, "Access Token")
			dialogField(c, "Name", func() { dialogInput(c, ui.TextInput(c.Key("name"), &s.name)).Placeholder(stores.Bitwarden) })
			dialogField(c, "Region", func() {
				ui.Segmented(c.Key("region"), &s.region, regions[0].label, regions[1].label).Label("Region").FillEqually()
			})
			dialogField(c, "Project", func() { dialogInput(c, ui.TextInput(c.Key("project"), &s.project)).Placeholder("Optional") })
		}, "Connecting", "Add", g.connect)
}

// replaceStoreToken is the dialog that replaces a store's access token, as
// mk store token does, as when the old one expired or was revoked. It stays
// open after a failure.
func (g *gui) replaceStoreToken(c *ui.Context) {
	s := &g.settings
	g.tokenDialog(c, "replace-token", s.replacing.ID != "", func() { s.replacing = stores.Connection{} }, fmt.Sprintf("Replace the Access Token of “%s”", s.replacing.ID),
		"Paste a new access token for the same Secrets Manager project, as when the old one expired or was revoked. The store keeps its name and its entries’ program access.",
		func() { g.tokenField(c, "New Access Token") }, "Replacing", "Replace", g.replaceToken)
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
			ui.Text(c, "Default").TextColor(t.TextMuted).FontSize(t.FontSize - 1).Tooltip("Used by commands without --store")
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
			if m.Item("Replace Access Token…").Chosen() {
				s.replacing, s.err = connection, ""
			}
			m.Separator()
			if m.Item("Remove…").Danger().Chosen() {
				g.removeStore(connection)
			}
		})
	})
}
