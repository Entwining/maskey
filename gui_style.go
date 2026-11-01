package main

import (
	"embed"
	"hash/fnv"
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store"
)

// icons holds Lucide's SVG files as its release 1.64.0 publishes them,
// beside the license they come with.
//
//go:embed icons/*.svg
var icons embed.FS

func icon(name string) *ui.SVG {
	data, err := icons.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(data)
}

var (
	eyeIcon       = icon("eye")
	eyeOffIcon    = icon("eye-off")
	copyIcon      = icon("copy")
	infoIcon      = icon("info")
	checkIcon     = icon("check")
	plusIcon      = icon("plus")
	folderKeyIcon = icon("folder-key")
	keyIcon       = icon("key-round")
	layersIcon    = icon("layers")
	panelLeftIcon = icon("panel-left")
	searchIcon    = icon("search")
)

// kinds are the kinds of value, as a person chooses among them.
var kinds = []kindChoice{
	{store.ValueToken, "API Key", keyIcon},
	{store.ValuePassword, "Password", icon("rectangle-ellipsis")},
	{store.ValuePEM, "Private Key", icon("file-key")},
	{store.ValueURL, "URL", icon("link")},
	{store.ValueIdentifier, "Identifier", icon("id-card")},
	{store.ValueSetting, "Other", icon("ellipsis")},
}

type kindChoice struct {
	kind  store.ValueKind
	label string
	icon  *ui.SVG
}

func choiceOf(kind store.ValueKind) kindChoice {
	i := slices.IndexFunc(kinds, func(k kindChoice) bool { return k.kind == kind })
	return kinds[i]
}

func kindLabel(kind store.ValueKind) string { return choiceOf(kind).label }

func kindOf(label string) store.ValueKind {
	i := slices.IndexFunc(kinds, func(k kindChoice) bool { return k.label == label })
	return kinds[i].kind
}

// tileColors give white icons at least 3:1 contrast in either appearance.
// A namespace's name picks its tile's color, so the color is the same on
// every Mac without being stored.
var tileColors = []ui.Color{
	ui.Hex("#FF3B30"), ui.Hex("#D97F00"), ui.Hex("#2DAA4C"), ui.Hex("#2CA2B8"), ui.Hex("#007AFF"),
	ui.Hex("#5856D6"), ui.Hex("#AF52DE"), ui.Hex("#FF2D55"), ui.Hex("#A2845E"),
}

// entryTile shows an entry as a rounded square, as Passwords shows a
// website: its color tells the namespace, and its icon the kind of value.
// Before a namespace is given, it takes the accent color.
func entryTile(c *ui.Context, namespace string, kind store.ValueKind, size float32) ui.Element {
	t := c.Theme()
	tile := ui.Box(c).Size(size, size).Shrink(0).Radius(size * 0.225).Center()
	if namespace == "" {
		tile.Background(t.Accent).TextColor(t.AccentText)
	} else {
		hash := fnv.New32a()
		hash.Write([]byte(namespace))
		tile.Background(tileColors[hash.Sum32()%uint32(len(tileColors))]).TextColor(ui.RGB(255, 255, 255))
	}
	return tile.Children(func() {
		ui.Icon(c, choiceOf(kind).icon).Size(size*0.6, size*0.6)
	})
}

// theme is MyGo's theme with, in dark mode, the neutral grays of macOS's own
// windows, which MyGo's update window also uses, in place of its blue-tinted
// zinc, which looks black beside the sidebar's material.
func theme(base *ui.Theme) *ui.Theme {
	t := *base
	if t.Dark {
		t.Background, t.Surface = ui.Hex("#1e1e1e"), ui.Hex("#2a2a2a")
		t.SurfaceHover, t.SurfacePressed = ui.Hex("#2e2e2e"), ui.Hex("#393939")
		t.Border = ui.Hex("#363636")
		t.Text, t.TextMuted = ui.Hex("#f5f5f7"), ui.Hex("#98989d")
	}
	return &t
}

// neutral is a gray that, made translucent, tints whatever is under it, as
// the hover and press states of controls over both the material and the
// content do.
var neutral = ui.RGBA(127, 127, 127, 1)

// iconButton is a borderless button showing an icon, or a spinner while its
// action runs; both fit the same bounds.
func iconButton(c *ui.Context, svg *ui.SVG, label string, size float32, busy, disabled bool) ui.Element {
	b := ui.ButtonBase(c).Size(size, size).Shrink(0).Radius(size/4 + 1).Center().Label(label).Tooltip(label).Disabled(busy || disabled)
	if b.Pressed() {
		b.Background(neutral.Alpha(0.22))
	} else if b.Hovered() {
		b.Background(neutral.Alpha(0.13))
	}
	return b.Children(func() {
		if busy {
			ui.Spinner(c).Size(size-10, size-10).Label(label + " in progress")
		} else {
			ui.Icon(c, svg).Size(size*0.6, size*0.6)
		}
	})
}

// toolbar is the strip at the top of a pane, as high as the sidebar's room
// for the window controls, with the line under it, so that the panes' rows
// start at one height.
func toolbar(c *ui.Context, inset float32, content func()) {
	t := c.Theme()
	ui.Row(c).Height(titleBarHeight-1).Shrink(0).AlignItems(ui.Center).Gap(8).Padding(0, 12, 0, toolbarStart(t, inset)).DragWindow().Children(content)
	ui.Box(c).Height(1).Shrink(0).Background(t.Border)
}

// toolbarStart is where a toolbar's content starts: after inset, the room
// what floats over its left edge takes.
func toolbarStart(t *ui.Theme, inset float32) float32 { return max(t.Space(4.5), inset+8) }

func emptyState(c *ui.Context, svg *ui.SVG, title, message, action string, run func()) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Gap(8).Padding(24).Children(func() {
		ui.Icon(c, svg).Size(32, 32).TextColor(t.TextMuted).Margin(0, 0, 4, 0)
		ui.Text(c, title).FontSize(t.FontSize + 2).Bold()
		ui.Text(c, message).TextColor(t.TextMuted).MaxWidth(340).TextAlign(ui.Center)
		if action != "" && ui.PrimaryButton(c, action).Margin(8, 0, 0, 0).Clicked() {
			run()
		}
	})
}

// heading is the detail's title beside the entry's tile.
func heading(c *ui.Context, title, font, namespace string, kind store.ValueKind) {
	t := c.Theme()
	ui.Row(c).Gap(t.Space(3.5)).AlignItems(ui.Center).Children(func() {
		entryTile(c, namespace, kind, 44)
		ui.Text(c, title).Font(font).FontSize(t.FontSize + 5).Bold().SingleLine().MinWidth(0)
	})
}

// card holds a form's fields on a tinted panel, with a line between each.
func card(c *ui.Context, fields ...func()) ui.Element {
	t := c.Theme()
	return ui.Form(c, func() {
		for i, field := range fields {
			if i > 0 {
				ui.Box(c).Height(1).Margin(0, t.Space(3.5)).Background(neutral.Alpha(0.18))
			}
			field()
		}
	}).Gap(0).Radius(10).Background(t.Surface)
}

// cardField is a field of a card; its label is muted, as in Passwords.
func cardField(c *ui.Context, label string, control func()) ui.Element {
	t := c.Theme()
	return ui.Field(c, label, control).Padding(t.Space(1.5), t.Space(3.5)).TextColor(t.TextMuted)
}

// inputPadding is the room the form's controls leave around their text
// inside their 1-point border. The detail shows each value with that room
// and the border's, so that Edit frames the values without moving them.
func inputPadding(t *ui.Theme) (vertical, horizontal float32) {
	return t.Space(1.5), t.Space(2.5)
}
