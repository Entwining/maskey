package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/Entwining/maskey/internal/store/storetest"
)

// A name wider than the detail stays on one line, however narrow the
// window, so its field is as tall as a short name's.
func TestLongNameStaysOnOneLine(t *testing.T) {
	long := "SYNTHETIC_" + strings.Repeat("LONG_NAME_", 12) + "TOKEN"
	backend := storetest.New()
	backend.Put("app/URL", []byte(urlValue))
	backend.Put("app/"+long, []byte("synthetic-long-5c1e"))
	h := newHarnessWith(t, backend)
	h.tt.SetSize(windowMinWidth, 600)
	nameHeight := func(name string) float32 {
		t.Helper()
		h.click(name)
		label, _ := h.tt.Find("Name")
		next, _ := h.tt.Find("Namespace")
		return next.Y - label.Y
	}
	if short, long := nameHeight("URL"), nameHeight(long); long != short {
		t.Fatalf("the long name's field is %v tall, a short one's %v", long, short)
	}
}

// A tooltip shows a name only where its line cuts it, as AppKit's expansion
// tooltips do, and Kind, never cut, has none.
func TestTooltipsShowCutText(t *testing.T) {
	long := "SYNTHETIC_" + strings.Repeat("LONG_NAME_", 12) + "TOKEN"
	backend := storetest.New()
	backend.Put("app/URL", []byte(urlValue))
	backend.Put("app/"+long, []byte("synthetic-long-5c1e"))
	h := newHarnessWith(t, backend)
	h.tt.SetSize(windowMinWidth, 600)
	shown := func(text string) int {
		n := 0
		for _, s := range h.tt.Texts() {
			if s == text {
				n++
			}
		}
		return n
	}
	// tooltip reports whether resting the pointer on the text of the field
	// labeled field adds another text reading text.
	tooltip := func(entry, field, text string) bool {
		t.Helper()
		h.click(entry)
		h.tt.Move(1, 1)
		h.tt.Advance(time.Second)
		before := shown(text)
		label, _ := h.tt.Find(field)
		h.tt.Move(label.X+label.W+30, label.Y+label.H/2)
		h.tt.Frame()
		h.tt.Advance(time.Second)
		h.tt.Frame()
		return shown(text) > before
	}
	if !tooltip(long, "Name", long) {
		t.Error("the cut name has no tooltip")
	}
	if tooltip("URL", "Name", "URL") {
		t.Error("the whole name has a tooltip")
	}
	if tooltip("URL", "Kind", "URL") {
		t.Error("the kind has a tooltip")
	}
}

// A new entry's form puts the namespace above the name, as the entry's
// reference does, and starts in the first of them still empty, also when it
// replaces another form.
func TestNewEntryStartsAtFirstEmptyField(t *testing.T) {
	for _, tc := range []struct {
		name, chosen  string
		overEdit      bool
		wantNamespace string
		wantName      string
	}{
		{name: "all entries", chosen: "All Entries", wantNamespace: "synthetic"},
		{name: "a namespace", chosen: "app", wantNamespace: "app", wantName: "synthetic"},
		{name: "over an edit", chosen: "app", overEdit: true, wantNamespace: "app", wantName: "synthetic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.click(tc.chosen)
			if tc.overEdit {
				h.choose("URL", "Edit")
			}
			h.click("New Entry")
			if namespace, name := h.find("Namespace"), h.find("Name"); namespace.Y >= name.Y {
				t.Fatalf("Namespace at %v is not above Name at %v", namespace, name)
			}
			h.tt.Type("synthetic")
			if f := h.g.form; f.namespace != tc.wantNamespace || f.name != tc.wantName {
				t.Fatalf("typing filled namespace %q and name %q", f.namespace, f.name)
			}
		})
	}
}

func TestEditKeepsFieldsInPlace(t *testing.T) {
	h := newHarness(t)
	h.click("API_TOKEN")
	fields := []string{"Name", "Namespace", "Kind", "API Key"}
	before := make([]ui.Rect, len(fields))
	for i, s := range fields {
		before[i], _ = h.tt.Find(s)
	}
	// The shown value's bounds include the room it leaves as an input does.
	v, x := inputPadding(ui.DarkTheme())
	before[3].X, before[3].Y = before[3].X+x+1, before[3].Y+v+1
	h.click("Edit")
	for i, s := range fields {
		after, _ := h.tt.Find(s)
		if dx, dy := after.X-before[i].X, after.Y-before[i].Y; dx < -0.5 || dx > 0.5 || dy < -0.5 || dy > 0.5 {
			t.Errorf("%s moved from %v to %v", s, before[i], after)
		}
	}
}

// Program access's field is no taller than the Value field, and the info
// icon after its state explains it when the pointer rests there.
func TestAccessInfo(t *testing.T) {
	h := newHarness(t)
	h.click("URL")
	// Each of these is the middle of its field: a one-line field's label, or
	// the Value row's tallest control. With P the step between one-line
	// fields and X the step from Kind to Value, Modified's middle is 3X-P
	// below Value's when Program access, between them, is as tall as the
	// Value field, and less where it is shorter.
	middle := func(s string) float32 {
		r, ok := h.tt.Find(s)
		if !ok {
			t.Fatalf("%s is absent: %q", s, h.tt.Texts())
		}
		return r.Y + r.H/2
	}
	name, kind, value, modified := middle("Name"), middle("Kind"), middle("Show Value"), middle("Modified")
	if step, want := modified-value, 3*(value-kind)-(kind-name); step-want > 0.5 {
		t.Fatalf("Modified is %v below the Value field, want at most %v", step, want)
	}
	shown := func() int { return strings.Count(strings.Join(h.tt.Texts(), "\n"), accessCaption) }
	h.tt.Move(1, 1)
	h.tt.Advance(time.Second)
	before := shown()
	state, _ := h.tt.Find("Not allowed")
	// The icon's center, 5 points after the state.
	h.tt.Move(state.X+state.W+5+22*iconScale/2, state.Y+state.H/2)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.tt.Frame()
	if shown() != before+1 {
		t.Fatalf("resting on the info icon shows %q", h.tt.Texts())
	}
}

// The Value field's buttons center on the value's first line, hidden or
// shown on several lines.
func TestValueButtonsCenterOnFirstLine(t *testing.T) {
	const value = "synthetic-first\nsynthetic-second"
	backend := storetest.New()
	backend.Put("app/MULTI", []byte(value))
	h := newHarnessWith(t, backend)
	h.click("MULTI")
	find := func(s string) ui.Rect {
		t.Helper()
		r, ok := h.tt.Find(s)
		if !ok {
			t.Fatalf("%s is absent: %q", s, h.tt.Texts())
		}
		return r
	}
	check := func(state string, line float32, buttons ...string) {
		t.Helper()
		for _, b := range buttons {
			r := find(b)
			if d := r.Y + r.H/2 - line; d < -0.5 || d > 0.5 {
				t.Errorf("%s: %s's middle is %v from the first line's", state, b, d)
			}
		}
	}
	hidden := find(hiddenValue)
	check("hidden", hidden.Y+hidden.H/2, "Show Value", "Copy Value")
	h.click("Show Value")
	shown := find(value)
	line := shown.H / float32(strings.Count(value, "\n")+1)
	check("shown", shown.Y+line/2, "Hide Value", "Copy Value")
}

// The fields' trailing controls end at the field's edge, where Edit's
// inputs end, and line up by what they show, not by the room an icon button
// keeps for its hover background.
func TestTrailingControlsAlign(t *testing.T) {
	h := newHarness(t)
	h.click("URL")
	sw, ok := h.tt.AccessByRole(ui.RoleSwitch, "Program access")
	if !ok {
		t.Fatal("Program access has no switch")
	}
	end := sw.Bounds.X + sw.Bounds.W
	copyButton := h.find("Copy Value")
	if glyph := copyButton.X + copyButton.W - iconInset(22); !near(glyph, end) {
		t.Fatalf("the copy icon ends at %v, the switch at %v", glyph, end)
	}
	h.click("Edit")
	input, ok := h.tt.AccessByRole(ui.RoleTextField, "Value")
	if !ok {
		t.Fatal("the form has no Value input")
	}
	if inputEnd := input.Bounds.X + input.Bounds.W; !near(end, inputEnd) {
		t.Fatalf("the switch ends at %v, the Value input at %v", end, inputEnd)
	}
}

// Modified is written in local time as YYYY-MM-DD HH:mm:ss, whatever the
// region's hour cycle.
func TestModifiedFormat(t *testing.T) {
	backend := storetest.New()
	backend.Now = func() time.Time { return time.Date(2026, 10, 30, 13, 7, 42, 0, time.UTC) }
	backend.Put("app/URL", []byte(urlValue))
	h := newHarnessWith(t, backend)
	h.tt.SetRegion(i18n.Region{Locale: "en-US", HourCycle: 12})
	h.click("URL")
	want := time.Date(2026, 10, 30, 13, 7, 42, 0, time.UTC).Local().Format("2006-01-02 15:04:05")
	if !h.tt.HasText(want) {
		t.Fatalf("texts %q, want Modified %q", h.tt.Texts(), want)
	}
}
