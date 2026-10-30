package main

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
)

// detail shows the chosen entry, or the form, as Passwords shows an item:
// a heading over a card of its fields.
func (g *gui) detail(c *ui.Context) {
	t := c.Theme()
	f := &g.form
	submitting := g.submitting()
	// Two slots of one width hold the buttons in every state, Edit's slot
	// taking Save or Add, the empty one before it Cancel or Close, so that
	// nothing moves as the form opens and closes, and the search field keeps
	// its place and width.
	slot := func() { ui.Box(c).MinWidth(toolbarButtonWidth) }
	toolbar(c, func() {
		ui.Box(c).Grow(1)
		switch {
		case f.open:
			action := "Save"
			if f.from == (store.Ref{}) {
				action = "Add"
			}
			cancel, submit := formButtons(c, action, g.canSubmit(), submitting)
			if cancel {
				g.cancelForm()
			}
			if submit {
				g.submit(nil)
			}
		case g.entry != (store.Ref{}):
			slot()
			if ui.Button(c, "Edit").MinWidth(toolbarButtonWidth).Height(28).Disabled(g.writing(g.entry)).Clicked() {
				g.openEdit(g.entry)
			}
		default:
			slot()
			slot()
		}
		search := ui.SearchField(c.Key("search"), &g.query).Bind(&g.search).Label("Search entries").Width(searchWidth).MinWidth(searchMinWidth).Height(28).
			Padding(0, t.Space(2)).Radius(7).Background(neutral.Alpha(0.12)).Border(1, ui.Transparent).Disabled(len(g.namespaces) == 0)
		if search.Changed() {
			g.moves++
		}
	})
	// Escape in the search field is the field's, which clears the query.
	if f.open && !g.search.FocusWithin(c) && c.Shortcut(0, ui.KeyEscape) {
		g.cancelForm()
	}
	if !f.open && g.entry == (store.Ref{}) {
		if g.loaded && len(g.namespaces) > 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "No Entry Selected").FontSize(t.FontSize + 2).TextColor(t.TextMuted)
			})
		}
		return
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).WidthPercent(100).MaxWidth(620).AlignSelf(ui.Center).Padding(t.Space(6), t.Space(8)).Gap(t.Space(5)).Children(func() {
			if f.open {
				g.formCard(c, submitting)
			} else {
				g.entryCard(c, g.entry)
			}
		})
	})
}

func (g *gui) entryCard(c *ui.Context, ref store.Ref) {
	t := c.Theme()
	kind := g.kind(ref)
	heading(c, ref.Name, monospace, ref.Namespace, kind)
	v, h := inputPadding(t)
	// A value stays on one line, as in Passwords, so that a name never
	// breaks into what reads as two; its tooltip shows what the line cuts.
	text := func(s string, font string) func() {
		return func() {
			ui.Text(c, s).Font(font).TextColor(t.Text).SingleLine().Tooltip(s).Selectable().Padding(v+1, h+1)
		}
	}
	attributes := g.attributes[ref]
	fields := []func(){
		func() { cardField(c, "Name", text(ref.Name, monospace)) },
		func() { cardField(c, "Namespace", text(ref.Namespace, "")) },
		func() { cardField(c, "Kind", text(kindLabel(kind), "")) },
		func() {
			cardField(c, "Value", func() {
				ui.Row(c).Gap(4).AlignItems(ui.Start).Padding(v+1, h+1).Children(func() {
					if g.showing(ref) {
						// The value's own lines stay whole, and scroll where they
						// are wider than the card.
						ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Children(func() {
							ui.Text(c, g.revealed).Font(monospace).TextColor(t.Text).NoWrap().Selectable()
						})
					} else {
						ui.Text(c, hiddenValue).TextColor(t.Text).Grow(1).MinWidth(0).SingleLine()
					}
					show, showIcon := "Show Value", eyeIcon
					if g.showing(ref) {
						show, showIcon = "Hide Value", eyeOffIcon
					}
					if iconButton(c.Key("reveal"), showIcon, show, 22, g.readAction == "reveal", !g.canReveal(ref)).Clicked() {
						g.toggleReveal()
					}
					copyLabel, copySVG := "Copy Value", copyIcon
					if g.copied {
						copyLabel, copySVG = "Copied", checkIcon
					}
					if iconButton(c.Key("copy"), copySVG, copyLabel, 22, g.readAction == "copy", !g.canCopy(ref)).Clicked() {
						g.copy(ref)
					}
				})
			})
		},
	}
	if attributes.Notes != "" {
		fields = append(fields, func() {
			cardField(c, "Notes", func() { ui.Text(c, attributes.Notes).TextColor(t.Text).Selectable().Padding(v+1, h+1) })
		})
	}
	if !attributes.Modified.IsZero() {
		modified := attributes.Modified.Local().Format(time.DateTime)
		fields = append(fields, func() { cardField(c, "Modified", text(modified, "")) })
	}
	card(c, fields...)
}

func (g *gui) formCard(c *ui.Context, submitting bool) {
	t := c.Theme()
	f := &g.form
	if f.from == (store.Ref{}) {
		heading(c, "New Entry", "", f.namespace, kindOf(f.kind))
	} else {
		heading(c, f.from.Name, monospace, f.from.Namespace, kindOf(f.kind))
	}
	input := func(e ui.Element) ui.Element {
		return e.Padding(inputPadding(t)).Background(t.Background).TextColor(t.Text)
	}
	// An error under a field is about what the inputs held when it was
	// raised.
	edited := func(e ui.Element) {
		if e.Changed() && f.err.field != "" {
			f.err = formError{}
		}
	}
	card(c,
		func() {
			cardField(c, "Name", func() {
				name := input(ui.TextInput(c.Key("name"), &f.name)).Bind(&g.nameField).Font(monospace).Placeholder("GITHUB_TOKEN")
				if f.from == (store.Ref{}) {
					name.AutoFocus()
				}
				edited(name)
				// The input takes this pass's typing as it returns, so the
				// kind follows the name here, before the Kind field shows it.
				f.followName()
			}).Error(f.err.on("name"))
		},
		func() {
			cardField(c, "Namespace", func() {
				edited(input(ui.TextInput(c.Key("namespace"), &f.namespace)).Bind(&g.namespaceField).Placeholder("github"))
			}).Error(f.err.on("namespace"))
		},
		func() {
			cardField(c, "Kind", func() {
				labels := make([]string, len(kinds))
				for i, k := range kinds {
					labels[i] = k.label
				}
				input(ui.Select(c.Key("kind"), &f.kind, labels))
			})
		},
		func() {
			cardField(c, "Value", func() {
				switch {
				case f.loading:
					ui.Spinner(c).Size(16, 16).Margin(4, 0).Label("Loading value")
				case f.keepValue:
					ui.Text(c, "This value contains carriage returns, which this editor would change, so it stays as it is. Replace it with mk set --stdin.").TextColor(t.TextMuted)
				case f.from == (store.Ref{}) || f.original != "":
					edited(valueEditor(c, input(ui.TextArea(c.Key("value"), &f.value)), &f.err, &g.valueField, f.from != (store.Ref{})))
				}
			}).Error(f.err.on("value"))
		},
		func() {
			cardField(c, "Notes", func() {
				input(ui.TextArea(c.Key("notes"), &f.notes)).Lines(2, 6).Placeholder("Optional")
			})
		},
	).Disabled(submitting)
	if f.err.field == "" && f.err.message != "" {
		ui.Text(c, f.err.message).TextColor(t.Danger)
	}
	if f.from != (store.Ref{}) {
		if ui.Button(c, "Delete").AlignSelf(ui.End).TextColor(t.Danger).Disabled(g.writeAction != "").Clicked() {
			g.askDelete(f.from)
		}
	}
}

// valueEditor sets up the text area editing a value, which keeps LF line
// endings; it turns CRLF into LF, so it refuses to paste CRLF rather than
// change a value without saying so.
func valueEditor(c *ui.Context, area ui.Element, err *formError, field *ui.Handle, autofocus bool) ui.Element {
	area.Bind(field).Font(monospace).Lines(3, 8).OnPaste(func(text string) bool {
		if strings.Contains(text, "\r\n") {
			*err = formError{"value", "The pasted text has CRLF line endings, which this editor would change to LF. Use mk set --stdin to keep them."}
			return true
		}
		return false
	})
	if autofocus {
		area.AutoFocus()
	}
	return area
}

// formButtons are the form's Cancel and its primary action, whose spinner
// takes the place of its title while the write runs. Cancel becomes Close
// then: closing the form does not stop the write, whose outcome still shows.
func formButtons(c *ui.Context, action string, enabled, submitting bool) (cancel, submit bool) {
	t := c.Theme()
	label := "Cancel"
	if submitting {
		label = "Close"
	}
	cancel = ui.Button(c.Key("cancel"), label).MinWidth(toolbarButtonWidth).Height(28).Clicked()
	submit = ui.PrimaryButton(c.Key("submit"), "").Label(action).MinWidth(toolbarButtonWidth).Height(28).Disabled(!enabled || submitting).Children(func() {
		if submitting {
			ui.Spinner(c).Size(14, 14).TextColor(t.AccentText).Label(action + " in progress")
		} else {
			ui.Text(c, action).SingleLine()
		}
	}).Clicked() || enabled && !submitting && c.Shortcut(ui.Cmd, ui.KeyEnter)
	return cancel, submit
}
