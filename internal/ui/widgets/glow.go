package widgets

import (
	_ "embed"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// The glow of the AI's work: a ring of its colors flowing around a box,
// after MyGo's chat-glow example. GlowPalette is the colors' source, for
// the AI chat's other shaders.
var (
	//go:embed shaders/palette.kage
	GlowPalette string
	//go:embed shaders/ring.kage
	ringSource string
	//go:embed shaders/sweep.kage
	sweepSource string

	ringShader  = shader.MustCompile("package main\n" + GlowPalette + ringSource)
	sweepShader = shader.MustCompile("package main\n" + GlowPalette + sweepSource)

	// Their uniforms, looked up once, and values set again for each
	// drawing: views run on the UI thread alone.
	ringTime, ringStrength, ringPad, ringRadius = ringShader.Uniform("Time"), ringShader.Uniform("Strength"), ringShader.Uniform("Pad"), ringShader.Uniform("Radius")
	sweepTime                                   = sweepShader.Uniform("Time")
	ringValues, sweepValues                     = ringShader.NewValues(), sweepShader.NewValues()
)

// ShadersWithoutGPU has the shaders draw without a GPU too, slowly, as
// the docs' screenshots and tests' snapshots do.
var ShadersWithoutGPU = false

// Accelerated reports whether p draws the shaders: with a GPU, the ones
// without drawing plain stand-ins.
func Accelerated(p *ui.Painter) bool { return p.Accelerated() || ShadersWithoutGPU }

// GlowRing draws in r a line of the glow's colors inset pad, with corners
// of radius, glowing toward the edge, flowing as t, in seconds, goes on;
// strength from 0, none, to 1. Without a GPU it draws nothing: the box's
// own border stands for it.
func GlowRing(p *ui.Painter, r ui.Rect, strength, t, pad, radius float32) {
	if !Accelerated(p) || strength < 0.01 {
		return
	}
	ringShader.Draw(p, r, shader.Options{Values: ringValues.Float(ringTime, t).Float(ringStrength, strength).Float(ringPad, pad).Float(ringRadius, radius)})
}

// flashTime is how long a Flash shows: up quickly, then fading.
const flashTime = 1200 * time.Millisecond

// Flash is a glow that comes once and fades, as on where the AI chat's
// SQL landed.
type Flash struct{ at time.Time }

// Start has it show from now.
func (f *Flash) Start(now time.Time) { f.at = now }

// Strength is how much it glows now, 0 before it started, once done, or
// with less motion asked; it keeps frames coming while it shows.
func (f *Flash) Strength(c *ui.Context) float32 {
	if f.at.IsZero() || c.Preferences().ReduceMotion {
		return 0
	}
	t := c.Now().Sub(f.at)
	if t >= flashTime {
		f.at = time.Time{}
		return 0
	}
	c.AnimationFrame()
	x := float32(t) / float32(flashTime)
	if x < 0.12 {
		return x / 0.12
	}
	return 1 - (x-0.12)/0.88
}

// GlowLine draws in r, a thin line, the glow's colors with light
// sweeping along them as t, in seconds, goes on: work going on in one
// place, as a notebook's running cell. Without a GPU, a plain line of the
// accent.
func GlowLine(c *ui.Context, p *ui.Painter, r ui.Rect, t float32) {
	if !Accelerated(p) {
		p.Fill(r, c.Theme().Accent, 0)
		return
	}
	sweepShader.Draw(p, r, shader.Options{Values: sweepValues.Float(sweepTime, t)})
}

// LandedTint is the color behind lines that just came, as the AI chat's
// SQL, at full strength: the glow's violet, faint.
var LandedTint = ui.RGBA(139, 92, 246, 0.16)

// GlowTime is the glow's clock, in seconds, which moves its colors.
func GlowTime(c *ui.Context) float32 {
	return float32(c.Now().UnixMilli()%1_000_000) / 1000
}
