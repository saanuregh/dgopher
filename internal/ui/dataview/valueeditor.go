package dataview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Forms of the value editor besides its text.
const (
	formTree     = "Tree"
	formList     = "List"
	formCalendar = "Calendar"
	formSwitch   = "True or False"
	formText     = "Text"
)

// valueEditor edits a cell's whole value: as text, and in the form its
// type suits, a tree of JSON, the list of an enum's values, a calendar
// for dates, a switch for booleans, which writes the text.
type valueEditor struct {
	open     bool
	g        *Grid
	src      *Source
	row, col int
	text     string
	editable bool
	info     columnInfo

	forms []string // those offered, the text last
	form  string   // the one shown

	enum     []string
	choice   string     // the enum value chosen
	when     time.Time  // the calendar's date and time
	date     dateLayout // how the value spells its date
	truth    int        // the switch: 0 true, 1 false
	jsonOpen map[string]*bool

	// The tree's JSON, decoded once for treeText, its objects as jsonObject.
	treeText string
	treeRoot any
	treeErr  error
	treeOK   bool
}

// jsonObject is a decoded JSON object with its keys sorted.
type jsonObject struct {
	keys   []string
	values map[string]any
}

// sortedJSON turns every object under v into a jsonObject.
func sortedJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k, item := range x {
			keys = append(keys, k)
			x[k] = sortedJSON(item)
		}
		slices.Sort(keys)
		return jsonObject{keys: keys, values: x}
	case []any:
		for i, item := range x {
			x[i] = sortedJSON(item)
		}
	}
	return v
}

// decodedTree is e.text decoded, decoding only when the text changed.
func (e *valueEditor) decodedTree() (any, error) {
	if !e.treeOK || e.treeText != e.text {
		dec := json.NewDecoder(bytes.NewReader([]byte(e.text)))
		dec.UseNumber()
		var root any
		err := dec.Decode(&root)
		e.treeText, e.treeRoot, e.treeErr, e.treeOK = e.text, sortedJSON(root), err, true
	}
	return e.treeRoot, e.treeErr
}

// columnInfo is what the editor needs to know of a column of the rows:
// its type, as the table's catalog says where the column is a table's,
// and the engine.
type columnInfo struct {
	Type   string
	Engine db.Engine
}

func openValueEditor(a Host, g *Grid, src *Source, row, col int) {
	v, _ := g.value(src, row, col)
	text := ""
	if v != nil && v != unset && v != db.Default {
		text = PrettyValue(a.Settings().ViewFormat.Format(v))
	}
	e := &valueEditor{open: true, g: g, src: src, row: row, col: col, text: text,
		editable: g.edits != nil && g.columnReadOnly(col) == "" && !g.isMasked(col),
		info:     columnInfo{Type: src.Cols[col].Type}, jsonOpen: map[string]*bool{}}
	if g.isMasked(col) {
		e.text = MaskedText
	}
	if g.columnInfo != nil {
		e.info = g.columnInfo(col)
	}
	e.offerForms()
	a.Dialogs().valueEdit = e
	if g.enumValues != nil && e.editable && e.form == formText {
		g.enumValues(col, func(values []string) {
			if len(values) > 0 && a.Dialogs().valueEdit == e {
				e.enum, e.choice = values, e.text
				e.forms = []string{formList, formText}
				e.form = formList
			}
		})
	}
}

// offerForms works out the forms the column's type suits, the first
// shown.
func (e *valueEditor) offerForms() {
	typ := strings.ToLower(e.info.Type)
	e.form = formText
	switch {
	case e.text == MaskedText:
	case typ == "bool" || typ == "boolean" || typ == "tinyint(1)":
		e.form = formSwitch
		if t := strings.ToLower(e.text); t == "false" || t == "0" || t == "f" {
			e.truth = 1
		}
	case dateType(typ) != "":
		d, when, ok := parseDateText(e.text, dateType(typ) == "datetime")
		if ok {
			e.form, e.date, e.when = formCalendar, d, when
		}
	case strings.Contains(typ, "json") || looksLikeJSON(e.text):
		if json.Valid([]byte(e.text)) && looksLikeJSON(e.text) {
			e.form = formTree
		}
	}
	e.forms = []string{formText}
	if e.form != formText {
		e.forms = []string{e.form, formText}
	}
}

// dateType says whether a type holds dates ("date") or dates and times
// ("datetime"), "" for neither.
func dateType(typ string) string {
	switch {
	case strings.Contains(typ, "timestamp") || strings.Contains(typ, "datetime"):
		return "datetime"
	case typ == "date" || typ == "date32":
		return "date"
	}
	return ""
}

func looksLikeJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

// dateLayout is how a value spells its date and time: the separator
// between them, 0 for a date alone, and what follows the minutes, as
// seconds and a zone, kept as they were.
type dateLayout struct {
	sep  byte
	rest string
}

var dateText = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:([ T])(\d{2}:\d{2})(.*))?$`)

// parseDateText reads a date, with its time when it has one or withTime
// asks for one: an empty value starts today.
func parseDateText(text string, withTime bool) (dateLayout, time.Time, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		now := time.Now()
		d := dateLayout{}
		if withTime {
			d.sep, d.rest = ' ', ":00"
		}
		return d, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), true
	}
	m := dateText.FindStringSubmatch(text)
	if m == nil {
		return dateLayout{}, time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", m[1], time.Local)
	if err != nil {
		return dateLayout{}, time.Time{}, false
	}
	if m[2] == "" {
		return dateLayout{}, day, true
	}
	clock, err := time.Parse("15:04", m[3])
	if err != nil {
		return dateLayout{}, time.Time{}, false
	}
	when := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, time.Local)
	return dateLayout{sep: m[2][0], rest: m[4]}, when, true
}

// text spells a date and time as the value spelled its own.
func (d dateLayout) text(when time.Time) string {
	if d.sep == 0 {
		return when.Format("2006-01-02")
	}
	return when.Format("2006-01-02") + string(d.sep) + when.Format("15:04") + d.rest
}

// truthText is how an engine's column takes true or false.
func truthText(e db.Engine, typ string, truth bool) string {
	numeric := e == db.SQLite || e == db.MySQL || strings.EqualFold(typ, "tinyint(1)")
	switch {
	case numeric && truth:
		return "1"
	case numeric:
		return "0"
	case truth:
		return "true"
	}
	return "false"
}

func valueEditorView(a Host, c *ui.Context) {
	e := a.Dialogs().valueEdit
	pal := widgets.PaletteOf(c)
	th := c.Theme()
	ui.Modal(c, &e.open, func() {
		ui.Column(c).Width(640).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.src.Cols[e.col].Name).FontSize(15).Bold()
				ui.Text(c, e.info.Type).FontSize(12).TextColor(pal.Muted).Grow(1)
				if len(e.forms) > 1 {
					at := slices.Index(e.forms, e.form)
					if ui.Segmented(c, &at, e.forms...).Label("Editor").Changed() {
						e.switchForm(e.forms[at])
					}
				}
			})
			ui.Column(c).Height(320).Children(func() {
				switch e.form {
				case formTree:
					e.treeView(c)
				case formList:
					ui.Scroll(c).Grow(1).Border(1, th.Border).Radius(6).Children(func() {
						ui.RadioGroup(c, func() {
							for _, v := range e.enum {
								ui.Radio(c, &e.choice, v, v).Disabled(!e.editable).Padding(4, 8)
							}
						}).Label("Values").Padding(4)
					})
					e.text = e.choice
				case formCalendar:
					ui.Row(c).Gap(16).Children(func() {
						ui.Calendar(c, &e.when).Disabled(!e.editable)
						if e.date.sep != 0 {
							ui.TimeInput(c, &e.when).Label("Time").Disabled(!e.editable)
						}
					})
					e.text = e.date.text(e.when)
					ui.Text(c, e.text).Font(widgets.MonoFont).FontSize(12.5).TextColor(pal.Muted).Selectable()
				case formSwitch:
					ui.Segmented(c, &e.truth, "True", "False").Label("Value").Disabled(!e.editable)
					e.text = truthText(e.info.Engine, e.info.Type, e.truth == 0)
				default:
					area := ui.TextArea(c, &e.text).Font(widgets.MonoFont).FontSize(12.5).Grow(1).AutoFocus().Label("Value")
					if !e.editable {
						area.Disabled(true)
					}
				}
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Copy").Disabled(e.text == MaskedText).Clicked() {
					a.WriteClipboard(e.text)
				}
				if e.editable && ui.Button(c, "Set to NULL").Clicked() {
					e.open = false
					e.g.checkpoint()
					e.g.setValue(e.src, e.row, e.col, nil)
				}
				if ui.Button(c, "Cancel").Clicked() {
					e.open = false
				}
				if e.editable && widgets.Activated(c, ui.PrimaryButton(c, "Save")) {
					e.open = false
					e.g.checkpoint()
					e.g.setValue(e.src, e.row, e.col, db.Typed(e.text))
				}
			})
		})
	})
	if !e.open && a.Dialogs().valueEdit == e {
		a.Dialogs().valueEdit = nil
	}
}

// switchForm shows another form, which reads the text as it now is.
func (e *valueEditor) switchForm(form string) {
	switch form {
	case formCalendar:
		d, when, ok := parseDateText(e.text, e.date.sep != 0)
		if !ok {
			return // the text is no date the calendar can show: it stays
		}
		e.date, e.when = d, when
	case formTree:
		if !json.Valid([]byte(e.text)) {
			return
		}
	case formSwitch:
		e.truth = 0
		if t := strings.ToLower(e.text); t == "false" || t == "0" || t == "f" {
			e.truth = 1
		}
	case formList:
		e.choice = e.text
	}
	e.form = form
}

// treeView shows the JSON value as a tree of its objects and arrays.
func (e *valueEditor) treeView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	root, err := e.decodedTree()
	if err != nil {
		ui.Text(c, "Not JSON: "+err.Error()).TextColor(th.Danger)
		return
	}
	ui.Scroll(c).Grow(1).Border(1, th.Border).Radius(6).Children(func() {
		ui.Tree(c, func() { e.treeNode(c, "", "$", root) }).Padding(4).Label("JSON")
	})
	ui.Text(c, "The Text editor changes the value.").FontSize(12).TextColor(pal.Muted)
}

// treeNode shows a JSON value under its key, at a path naming it.
func (e *valueEditor) treeNode(c *ui.Context, key, path string, v any) {
	open := func() *bool {
		if e.jsonOpen[path] == nil {
			o := strings.Count(path, ".")+strings.Count(path, "[") < 2 // the first levels start open
			e.jsonOpen[path] = &o
		}
		return e.jsonOpen[path]
	}
	label := func(s string) string {
		if key == "" {
			return s
		}
		return key + ": " + s
	}
	switch x := v.(type) {
	case jsonObject:
		ui.TreeItem(c.Key(path), label(fmt.Sprintf("{%d}", len(x.keys))), open(), func() {
			for _, k := range x.keys {
				e.treeNode(c, k, path+"."+k, x.values[k])
			}
		})
	case []any:
		ui.TreeItem(c.Key(path), label(fmt.Sprintf("[%d]", len(x))), open(), func() {
			for i, item := range x {
				e.treeNode(c, fmt.Sprint(i), fmt.Sprintf("%s[%d]", path, i), item)
			}
		})
	case string:
		s, _ := json.Marshal(x)
		ui.TreeItem(c.Key(path), label(cellText(string(s), 200)), nil, nil)
	case nil:
		ui.TreeItem(c.Key(path), label("null"), nil, nil)
	default:
		ui.TreeItem(c.Key(path), label(fmt.Sprint(x)), nil, nil)
	}
}
