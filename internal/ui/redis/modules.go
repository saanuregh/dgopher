package redis

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// keyTypes are the types of keys the browser knows, as TYPE names them,
// with what it calls them.
var keyTypes = []struct{ typ, label string }{
	{"string", "string"}, {"hash", "hash"}, {"list", "list"}, {"set", "set"}, {"zset", "sorted set"},
	{"stream", "stream"}, {"ReJSON-RL", "JSON"}, {"vectorset", "vector set"}, {"array", "array"},
	{"TSDB-TYPE", "time series"}, {"MBbloom--", "Bloom filter"},
}

// allTypes is the type filter's choice of every type.
const allTypes = "All types"

// typeLabels are the type filter's choices.
var typeLabels = func() []string {
	out := []string{allTypes}
	for _, t := range keyTypes {
		out = append(out, t.label)
	}
	return out
}()

// typeLabel is what the browser calls a type.
func typeLabel(typ string) string {
	for _, t := range keyTypes {
		if t.typ == typ {
			return t.label
		}
	}
	return typ
}

// typeOf is the type a label names, "" for every type.
func typeOf(label string) string {
	for _, t := range keyTypes {
		if t.label == label {
			return t.typ
		}
	}
	return ""
}

// itemTypes are the types whose values the browser reads item by item.
var itemTypes = []string{"hash", "list", "set", "zset", "stream", "array", "vectorset"}

// readJSON reads a JSON document, indented for editing.
func readJSON(ctx context.Context, kv *db.KV, key string) (string, error) {
	doc, err := kv.JSONDocument(ctx, key)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if json.Indent(&b, []byte(doc), "", "  ") != nil {
		return doc, nil
	}
	return b.String(), nil
}

// jsonView edits a RedisJSON document: whole, saved with JSON.SET once it
// is JSON.
func (r *Tab) jsonView(c *ui.Context, ro bool) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	key := r.selected
	ui.Column(c).Grow(1).Padding(10, 14).Gap(8).Children(func() {
		if !r.whole {
			// A document longer than the editor shows is not saved from it.
			ui.Text(c, fmt.Sprintf("The document is longer than %s: shown in part, and edited with JSON.SET in the console.", widgets.HumanBytes(stringStart))).FontSize(12).TextColor(pal.Muted)
		}
		area := ui.TextArea(c, &r.editValue).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(ro || !r.whole).Label("Document")
		if area.Changed() {
			r.editDirty = r.editValue != r.value
		}
		valid := json.Valid([]byte(r.editValue))
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if !valid {
				ui.Text(c, "Not JSON.").FontSize(12).TextColor(th.Danger)
			} else if pretty := dataview.PrettyValue(r.editValue); pretty != r.editValue && ui.Button(c, "Format").Clicked() {
				r.editValue = pretty
				r.editDirty = r.editValue != r.value
			}
			ui.Spacer(c)
			if r.editDirty && !ro && r.whole {
				if ui.Button(c, "Revert").Clicked() {
					r.editValue, r.editDirty = r.value, false
				}
				if ui.PrimaryButton(c, "Save").Disabled(!valid).Clicked() || valid && r.a.KeysTo(r) && c.Shortcut(ui.Cmd, ui.KeyS) {
					var compact bytes.Buffer
					json.Compact(&compact, []byte(r.editValue))
					r.write([]string{"JSON.SET", key, "$", compact.String()}, r.loadKey)
				}
			}
		})
	})
}

// vectorState is the chosen element of a vector set: its vector and the
// elements most like it.
type vectorState struct {
	element string // the element read
	loading bool
	vector  []float64
	similar []db.Field
	err     string
	attrs   string // the attributes as edited
	list    ui.ListState
}

// similarShown is how many of the elements most like one are listed.
const similarShown = 10

// loadVector reads a vector set's element's vector, and the elements most
// like it.
func (r *Tab) loadVector(element string) {
	v := &r.vector
	v.element, v.loading, v.err, v.vector, v.similar = element, true, "", nil, nil
	kv, key, gen := r.conn.KV, r.selected, r.valueGen
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		vector, err := kv.VectorOf(ctx, key, element)
		var similar []db.Field
		if err == nil {
			similar, err = kv.Similar(ctx, key, element, similarShown+1)
		}
		return func() {
			if gen != r.valueGen || v.element != element {
				return
			}
			v.loading = false
			if err != nil {
				v.err = err.Error()
				return
			}
			// Not the element itself, which is the most like it.
			v.vector, v.similar = vector, slices.DeleteFunc(similar, func(f db.Field) bool { return f.Name == element })
		}
	})
}

// vectorDetail shows the chosen element of a vector set: its attributes,
// to edit, its vector, and the elements most like it.
func (r *Tab) vectorDetail(c *ui.Context, f db.Field) {
	v := &r.vector
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if v.element != f.Name {
		r.loadVector(f.Name)
		v.attrs = f.Value
	}
	ro := r.conn.Config.ReadOnly
	ui.Row(c).Height(220).Padding(8, 14).Gap(12).AlignItems(ui.Stretch).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		ui.Column(c).Grow(1).Gap(6).Children(func() {
			ui.Text(c, "Attributes of "+f.Name).FontSize(12).Bold()
			ui.TextArea(c, &v.attrs).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(ro).Label("Element attributes")
			if !ro && v.attrs != f.Value {
				valid := strings.TrimSpace(v.attrs) == "" || json.Valid([]byte(v.attrs))
				ui.Row(c).Gap(8).Children(func() {
					if !valid {
						ui.Text(c, "Not JSON.").FontSize(12).TextColor(th.Danger)
					}
					ui.Spacer(c)
					// Empty, they are removed, as VSETATTR does.
					if ui.Button(c, "Save Attributes").Disabled(!valid).Clicked() {
						r.write([]string{"VSETATTR", r.selected, f.Name, strings.TrimSpace(v.attrs)}, r.loadKey)
					}
				})
			}
		})
		ui.Column(c).Width(320).Gap(6).Children(func() {
			switch {
			case v.loading:
				ui.Spinner(c).Size(14, 14)
				return
			case v.err != "":
				ui.Text(c, v.err).FontSize(12).TextColor(th.Danger).Selectable()
				return
			}
			parts := make([]string, len(v.vector))
			for i, x := range v.vector {
				parts[i] = strconv.FormatFloat(x, 'g', 4, 64)
			}
			ui.Text(c, fmt.Sprintf("Vector, %d dimensions", len(v.vector))).FontSize(12).Bold()
			ui.Text(c, "["+widgets.OneLine(strings.Join(parts, ", "), 400)+"]").Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted).Selectable()
			ui.Text(c, "Most like it").FontSize(12).Bold()
			ui.List(c, &v.list, len(v.similar), func(i int) {
				s := v.similar[i]
				row := ui.ButtonBase(c).Padding(2, 4).Label(s.Name)
				row.Children(func() {
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.Text(c, s.Name).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1)
						ui.Text(c, strconv.FormatFloat(s.Score, 'f', 3, 64)).FontSize(11.5).TextColor(pal.Muted)
					})
				})
				if row.Clicked() {
					if i := slices.IndexFunc(r.fields, func(f db.Field) bool { return f.Name == s.Name }); i >= 0 {
						r.fieldRow = i
					}
				}
			}).Grow(1).Label("Elements most like it")
		})
	})
}

// addVector adds an element to the vector set shown: its vector typed as
// numbers, by spaces or commas, with its attributes when typed.
func (r *Tab) addVector() {
	element := strings.TrimSpace(r.newName)
	values := strings.FieldsFunc(r.newValue, func(c rune) bool { return c == ' ' || c == ',' || c == '[' || c == ']' })
	for _, v := range values {
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			r.a.ShowError("Not added", fmt.Sprintf("%q is not a number of the vector.", v))
			return
		}
	}
	attrs := strings.TrimSpace(r.newScore)
	switch {
	case element == "" || len(values) == 0:
		r.a.ShowError("Not added", "Name the element, and type its vector.")
		return
	case attrs != "" && !json.Valid([]byte(attrs)):
		r.a.ShowError("Not added", "The attributes are not JSON.")
		return
	}
	args := append([]string{"VADD", r.selected, "VALUES", strconv.Itoa(len(values))}, values...)
	args = append(args, element)
	if attrs != "" {
		args = append(args, "SETATTR", attrs)
	}
	r.write(args, func() { r.newName, r.newValue, r.newScore = "", "", ""; r.loadKey() })
}
