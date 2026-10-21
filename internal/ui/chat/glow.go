package chat

import (
	_ "embed"
	"math"
	"time"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// The chat's shaders, after MyGo's chat-glow example: the agent's orb,
// the message field's ring, and text shimmering while the agent works.
var (
	//go:embed shaders/orb.kage
	orbSource string
	//go:embed shaders/shimmer.kage
	shimmerSource string

	orbShader     = shader.MustCompile("package main\n" + widgets.GlowPalette + orbSource)
	shimmerShader = shader.MustCompile("package main\n" + shimmerSource)

	orbPhase, orbEnergy                    = orbShader.Uniform("Phase"), orbShader.Uniform("Energy")
	shimmerTime, shimmerBase, shimmerShine = shimmerShader.Uniform("Time"), shimmerShader.Uniform("Base"), shimmerShader.Uniform("Shine")
)

// The glow comes up over glowIn seconds and goes over glowOut; the orb's
// liveliness follows the work over liven. The ring glows ringPad DIPs
// around the field, whose corners are fieldRadius.
const (
	glowIn, glowOut, liven = 0.35, 0.9, 0.5
	ringPadding            = 8
	fieldRadius            = 12
)

// glow is a chat's motion: its clock, how much the work glows, how lively
// the orb is and how far its colors moved, and the shaders' values.
type glow struct {
	now, glow, energy, phase float64
	last                     time.Time
	// focus is how much the ring glows for the field's focus.
	focus    float64
	still    bool
	orb      *shader.Values
	stillOrb *shader.Values
	shimmer  *shader.Values
}

func newGlow() glow {
	return glow{orb: orbShader.NewValues(), stillOrb: orbShader.NewValues(), shimmer: shimmerShader.NewValues()}
}

// step moves the motion on to the frame's time, toward the work going on
// and the field's focus, and keeps frames coming while something moves.
func (g *glow) step(c *ui.Context, working, focused bool) {
	g.still = c.Preferences().ReduceMotion
	now := c.Now()
	dt := 0.0
	if !g.last.IsZero() {
		dt = min(max(now.Sub(g.last).Seconds(), 0), 0.25)
	}
	g.last = now
	g.now += dt
	if working {
		g.glow = min(1, g.glow+dt/glowIn)
	} else {
		g.glow = max(0, g.glow-dt/glowOut)
	}
	goal, focusGoal := boolf(working), boolf(focused)
	if g.still {
		g.glow, g.energy, g.focus = goal, goal, focusGoal
		return
	}
	g.energy += (goal - g.energy) * (1 - math.Exp(-dt/liven))
	g.focus += (focusGoal - g.focus) * (1 - math.Exp(-dt/0.12))
	g.phase += dt * (0.4 + 1.8*g.energy)
	if working || g.glow > 0.001 || g.energy > 0.01 || math.Abs(g.focus-focusGoal) > 0.01 {
		c.AnimationFrame()
	}
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// ease is smoothstep's curve, slow at both ends, for the glow's fades.
func ease(x float64) float64 { return x * x * (3 - 2*x) }

// orb draws the agent's orb in a box of size DIPs: lively while it works,
// at rest as it was otherwise.
func (g *glow) orbView(c *ui.Context, size float32, live bool) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).Label("Agent").Draw(func(p *ui.Painter, r ui.Rect) {
		if !widgets.Accelerated(p) {
			s := min(r.W, r.H) / 1.2
			p.Fill(ui.Rect{X: r.X + (r.W-s)/2, Y: r.Y + (r.H-s)/2, W: s, H: s}, c.Theme().Accent, s/2)
			return
		}
		if live {
			orbShader.Draw(p, r, shader.Options{Values: g.orb.Float(orbPhase, float32(g.phase)).Float(orbEnergy, float32(g.energy))})
			return
		}
		orbShader.Draw(p, r, shader.Options{Values: g.stillOrb.Float(orbPhase, 1.3).Float(orbEnergy, 0)})
	})
}

// drawRing draws the ring around the message field: faint with the
// focus, bright while the agent works.
func (g *glow) drawRing() func(p *ui.Painter, r ui.Rect) {
	return func(p *ui.Painter, r ui.Rect) {
		strength := 0.15*g.focus + 0.85*ease(g.glow)
		widgets.GlowRing(p, r, float32(strength), float32(g.now), ringPadding, fieldRadius)
	}
}

// shimmerText is a line of text with light sweeping across it while the
// agent works; plain without a GPU, or with less motion asked.
func (g *glow) shimmerText(c *ui.Context, text string, size float32) {
	pal := widgets.PaletteOf(c)
	if g.still {
		ui.Text(c, text).FontSize(size).TextColor(pal.Muted)
		return
	}
	c.AnimationFrame()
	ui.Text(c, text).FontSize(size).TextColor(ui.Color{}).Label(text).Draw(func(p *ui.Painter, r ui.Rect) {
		_, h := p.MeasureText(0, ui.Span{Text: text, Size: size})
		y := r.Y + (r.H-h)/2
		if !widgets.Accelerated(p) {
			p.Text(r.X, y, text, size, pal.Muted)
			return
		}
		shimmerShader.Text(p, r.X, y, text, size, shader.Options{Values: g.shimmer.
			Float(shimmerTime, float32(g.now)).Color(shimmerBase, pal.Muted).Color(shimmerShine, c.Theme().Text)})
	})
}
