package iphonectl

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg" // the bridge's screenshots
	_ "image/png"
	"os"
	"time"
)

// The bridge answers an input once it wrote it to the phone; the phone does not confirm it. Whether a step did
// anything is read off the screen: a screenshot after it, compared with the one before.

// Effect is what the screen shows after a step's input.
type Effect string

const (
	EffectChanged   Effect = "screen_changed"
	EffectUnchanged Effect = "no_visible_change"
	EffectUnknown   Effect = "not_checked" // no screenshot to compare (see the step's detail)
)

// The screen is compared on a coarse grid of average brightness, which ignores the JPEG's noise; the status bar
// (the clock, the battery) is left out.
const (
	gridW, gridH = 24, 48
	statusRows   = 3  // of gridH: the status bar
	cellDiff     = 10 // brightness levels (0–255) a cell must move by to count
	changedCells = 3  // cells that must move for the screen to count as changed
)

// sig is a screenshot reduced to the grid.
type sig []uint8

// signature reduces img to the grid; nil when it can't be read.
func signature(img Image) sig {
	data := img.Data
	if len(data) == 0 && img.File != "" {
		var err error
		if data, err = os.ReadFile(img.File); err != nil {
			return nil
		}
	}
	m, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := m.Bounds()
	if b.Dx() < gridW || b.Dy() < gridH {
		return nil
	}
	out := make(sig, gridW*gridH)
	const samples = 4 // per cell and axis
	for gy := range gridH {
		for gx := range gridW {
			var sum uint32
			for sy := range samples {
				for sx := range samples {
					x := b.Min.X + (gx*samples+sx)*b.Dx()/(gridW*samples)
					y := b.Min.Y + (gy*samples+sy)*b.Dy()/(gridH*samples)
					r, g, bl, _ := m.At(x, y).RGBA()
					sum += (299*r + 587*g + 114*bl) / 1000 >> 8
				}
			}
			out[gy*gridW+gx] = uint8(sum / (samples * samples))
		}
	}
	return out
}

// differs says whether two signatures show a different screen.
func differs(a, b sig) bool {
	if len(a) != len(b) {
		return true
	}
	moved := 0
	for i := statusRows * gridW; i < len(a); i++ {
		if d := int(a[i]) - int(b[i]); d > cellDiff || d < -cellDiff {
			if moved++; moved >= changedCells {
				return true
			}
		}
	}
	return false
}

// screenshot takes one within deadline and remembers it as the latest of the call.
func (c *Controller) screenshot(ctx context.Context, cl *call, deadline time.Time) (sig, error) {
	img, err := read(ctx, deadline, c.limits.Primitive, func(ctx context.Context) (Image, error) {
		return cl.bridge.Screenshot(ctx, cl.device)
	})
	if err != nil {
		return nil, err
	}
	cl.lastImg, cl.dirty = &img, false
	s := signature(img)
	cl.lastSig = s
	return s, nil
}

// effect is what the screen shows after an input, compared with before (nil: nothing to compare with).
func (c *Controller) effect(ctx context.Context, cl *call, before sig, deadline time.Time) (Effect, string) {
	if !cl.info.can("screenshot") {
		return EffectUnknown, "no screenshot on this phone to check it"
	}
	t := time.NewTimer(max(min(c.limits.Settle, time.Until(deadline)), 0))
	select {
	case <-t.C:
	case <-ctx.Done():
	}
	t.Stop()
	after, err := c.screenshot(ctx, cl, deadline)
	switch {
	case err != nil:
		return EffectUnknown, "the screen could not be checked after it: " + err.Error()
	case after == nil || before == nil:
		return EffectUnknown, "the screenshots could not be compared"
	case differs(before, after):
		return EffectChanged, ""
	}
	return EffectUnchanged, ""
}

// before is the screen to compare an input's effect with: the call's latest screenshot while nothing was sent
// since, else a new one.
func (c *Controller) before(ctx context.Context, cl *call, deadline time.Time) sig {
	if !cl.info.can("screenshot") {
		return nil
	}
	if cl.lastSig != nil && !cl.dirty {
		return cl.lastSig
	}
	s, _ := c.screenshot(ctx, cl, deadline)
	return s
}
