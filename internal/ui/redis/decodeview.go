package redis

import (
	"strings"

	"dgopher/internal/decode"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// How a value shows, besides decode's formats.
const (
	viewRaw  = "As stored"
	viewAuto = "Decoded as detected"
)

// viewChoices are the choices of how a value shows.
var viewChoices = func() []string {
	out := []string{viewRaw, viewAuto}
	for _, f := range decode.Formats {
		out = append(out, "As "+string(f))
	}
	return out
}()

// decodeView is how a value shows, with its decoding kept for the value
// and the choice it was made for.
type decodeView struct {
	as      string // one of viewChoices
	value   string // the value decoded, or being decoded
	asFor   string // the choice it was decoded for
	runs    int    // counts the decodings: a stale one's result is dropped
	running bool
	shown   decode.Decoded
	steps   string // shown's steps, joined for the label
	err     string
	display string // what the text area shows: its start, past stringStart
}

// newDecodeView shows a value decoded when its first bytes name a format,
// else as stored.
func newDecodeView(value string) decodeView {
	if decode.Detect([]byte(value)) != "" {
		return decodeView{as: viewAuto}
	}
	return decodeView{as: viewRaw}
}

// decode decodes the value as chosen, off the UI thread, once for each
// value and choice: a value made to be slow does not hold the app up.
func (d *decodeView) decode(value string, run func(work func() func())) {
	if d.value == value && d.asFor == d.as {
		return
	}
	d.value, d.asFor, d.err, d.running = value, d.as, "", true
	d.runs++
	as, at := d.as, d.runs
	run(func() func() {
		var shown decode.Decoded
		var err error
		if as == viewAuto {
			shown, err = decode.Auto([]byte(value))
		} else {
			shown, err = decode.As(decode.Format(strings.TrimPrefix(as, "As ")), []byte(value))
		}
		text := shown.Text
		if err != nil {
			text = decode.Text([]byte(value))
		}
		display := widgets.TextStart(text, stringStart)
		return func() {
			if at != d.runs {
				return
			}
			steps := make([]string, len(shown.Steps))
			for i, s := range shown.Steps {
				steps[i] = string(s)
			}
			d.running, d.shown, d.display = false, shown, display
			d.steps = strings.Join(steps, " → ")
			if err != nil {
				d.err = err.Error()
			}
		}
	})
}

// view draws the choice of how the value shows and, unless it is as
// stored, the value decoded; it reports whether it drew the value. A
// value that does not decode as its marks told shows as stored, which
// text starting as a format's marks may be.
func (d *decodeView) view(c *ui.Context, value, label string, run func(work func() func())) bool {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if d.as != viewRaw {
		d.decode(value, run)
	}
	fallBack := d.as == viewAuto && d.err != "" && !d.running
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Select(c, &d.as, viewChoices).Label(label + " view").Width(220)
		if d.as == viewRaw {
			return
		}
		switch {
		case d.running:
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, "Decoding…").FontSize(12).TextColor(pal.Muted)
		case d.err != "":
			ui.Text(c, d.err).FontSize(12).TextColor(th.Danger).SingleLine().Shrink(1)
		case len(d.shown.Steps) == 0:
			ui.Text(c, "Nothing to decode: shown as stored.").FontSize(12).TextColor(pal.Muted)
		default:
			ui.Text(c, d.steps+", read only").FontSize(12).TextColor(pal.Muted)
		}
	})
	if d.as == viewRaw || fallBack {
		return false
	}
	if !d.running {
		ui.TextArea(c, &d.display).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(true).Label(label + " decoded")
	}
	return true
}
