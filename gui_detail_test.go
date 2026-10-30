package main

import (
	"strings"
	"testing"

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
	h.tt.SetSize(820, 600)
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
