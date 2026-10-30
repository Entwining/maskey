package main

import (
	"math"
	"testing"
)

// Every tile's white icon has the 3:1 contrast WCAG 2.2 asks of graphics.
func TestTileColorsContrastWithWhite(t *testing.T) {
	linear := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	for _, c := range tileColors {
		luminance := 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
		if ratio := 1.05 / (luminance + 0.05); ratio < 3 {
			t.Errorf("white on #%02X%02X%02X has contrast %.2f:1", c.R, c.G, c.B, ratio)
		}
	}
}
