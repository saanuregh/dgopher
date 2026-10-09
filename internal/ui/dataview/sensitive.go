package dataview

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"dgopher/internal/audit"

	"github.com/egoist/mygo/ui"
)

// maskedText is what a grid shows of a hidden value.
const maskedText = "••••••"

var (
	// sensitiveName matches the words of column names whose values are
	// secrets or personal numbers: passwords, tokens, keys, card numbers.
	sensitiveName = regexp.MustCompile(`(^|_)(pass|password|passwd|pwd|secret|token|api_?key|private_?key|salt|otp|ssn|social_security|tax_id|card_?(number|no)|cvv|cvc|iban|pin)($|_)`)
	camelWord     = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

// looksSensitive reports whether a column's name says its values are
// secrets, in snake_case or camelCase.
func looksSensitive(name string) bool {
	return sensitiveName.MatchString(strings.ToLower(camelWord.ReplaceAllString(name, "${1}_${2}")))
}

// maskColumns works out which columns of the rows hide their values: those
// the project hides for the table, and those that look sensitive unless
// the settings show them; what the user chose in this view comes first.
func (v *Viewer) maskColumns() {
	var listed []string
	if key := v.historyKey(); key != "" {
		listed = v.source.Conn.Project.HiddenValues[key]
	}
	auto := !v.a.Settings().ShowSensitive
	v.grid.masked = make([]bool, len(v.src.Cols))
	for i, c := range v.src.Cols {
		masked, chosen := v.maskChosen[c.Name]
		if !chosen {
			masked = slices.Contains(listed, c.Name) || auto && looksSensitive(c.Name)
		}
		v.grid.masked[i] = masked
	}
}

// valuesMenu offers to hide a column's values, or show them: for the
// team, in the project's file, when the rows are a table's; else in this
// view only.
func (v *Viewer) valuesMenu(m *ui.Menu, col int) {
	name := v.src.Cols[col].Name
	key := v.historyKey()
	p := v.source.Conn.Project
	listed := key != "" && slices.Contains(p.HiddenValues[key], name)
	if col < len(v.grid.masked) && v.grid.masked[col] {
		if m.Item("Show Values").Chosen() {
			if listed {
				v.setHiddenValues(name, false)
			} else {
				v.chooseMask(name, false)
			}
		}
		return
	}
	if m.Item("Hide Values").Chosen() {
		if key != "" && p.Writable() == nil {
			v.setHiddenValues(name, true)
		} else {
			v.chooseMask(name, true)
		}
	}
}

func (v *Viewer) chooseMask(name string, masked bool) {
	if v.maskChosen == nil {
		v.maskChosen = map[string]bool{}
	}
	v.maskChosen[name] = masked
}

// setHiddenValues adds a column to the table's hidden values in the
// project's file, or takes it out.
func (v *Viewer) setHiddenValues(name string, hide bool) {
	p, key := v.source.Conn.Project, v.historyKey()
	if err := p.Writable(); err != nil {
		v.a.ShowError("Could not change the hidden values", err.Error())
		return
	}
	if p.HiddenValues == nil {
		p.HiddenValues = map[string][]string{}
	}
	cols := slices.DeleteFunc(slices.Clone(p.HiddenValues[key]), func(c string) bool { return c == name })
	if hide {
		cols = append(cols, name)
	}
	if len(cols) == 0 {
		delete(p.HiddenValues, key)
	} else {
		p.HiddenValues[key] = cols
	}
	if err := p.Save(v.a.ProjectConfigs(p)); err != nil {
		v.a.ShowError("Could not change the hidden values", err.Error())
		return
	}
	delete(v.maskChosen, name)
	verb := "shown"
	if hide {
		verb = "hidden"
	}
	v.a.Record(&v.source.Conn.Config, audit.Event{Kind: audit.KindConfig, Detail: fmt.Sprintf("values of %s in %s %s on screen", name, key, verb)})
}
