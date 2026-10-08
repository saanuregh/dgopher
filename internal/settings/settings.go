// Package settings holds the user's app-wide preferences, kept in the
// app's config directory.
package settings

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"dgopher/internal/db"
)

// Settings are the user's preferences.
type Settings struct {
	Theme         string  `json:"theme"` // system, light or dark
	EditorFont    float32 `json:"editorFontSize"`
	SidebarWidth  float32 `json:"sidebarWidth"`
	ResultsHeight float32 `json:"resultsHeight"`
	PageSize      int     `json:"pageSize"`
	// SemicolonOnly ends statements at ';' only; by default a blank line
	// ends one too, as in DBeaver.
	SemicolonOnly bool `json:"semicolonOnly,omitempty"`
	// ContinueOnError runs a script's next statements after one fails.
	ContinueOnError bool `json:"continueOnError,omitempty"`
	// GridFont is the size of the grids' text; 0 for the default.
	GridFont float32 `json:"gridFont,omitempty"`
	// ViewFormat is how the grids show values.
	ViewFormat ViewFormat `json:"viewFormat,omitzero"`
	// NotifyAfter is how many seconds work takes for its end to be told
	// by a system notification while the app is in the background; 0 for
	// never. Kept even at 0, which the default would otherwise replace.
	NotifyAfter int `json:"notifyAfterSeconds"`
	// Export keeps the export dialog's choices.
	Export ExportPrefs `json:"export,omitzero"`
	// AdvancedCopy is the last choice of the Advanced Copy dialog.
	AdvancedCopy CopyOptions `json:"advancedCopy,omitzero"`
	// Projects are the project folders in the sidebar, in its order.
	Projects []string `json:"projects,omitempty"`
	// TrustedShared holds the connections the user agreed to connect
	// to, by where they go and how they get the password
	// (sharedFingerprint). It is kept here, not in a project, for a
	// repository not to trust its own servers.
	TrustedShared []string `json:"trustedShared,omitempty"`
}

// Default returns the settings of a first run.
func Default() Settings {
	return Settings{Theme: "system", EditorFont: 13, SidebarWidth: 260, ResultsHeight: 320, PageSize: 500, NotifyAfter: 10}
}

// ExportPrefs are the export choices the app keeps from one export to
// the next.
type ExportPrefs struct {
	Format     string `json:"format,omitempty"`
	Folder     string `json:"folder,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
	OpenFolder bool   `json:"openFolder,omitempty"`
	// RowLimit is the most rows an export reads by running its statement
	// again, 0 for DefaultExportRowLimit; Unlimited reads every row.
	RowLimit  int  `json:"rowLimit,omitempty"`
	Unlimited bool `json:"unlimited,omitempty"`
}

// DefaultExportRowLimit is the most rows an export reads by running its
// statement again, until the user chooses another limit: enough for any
// spreadsheet, and short of a table that would take the server's
// evening.
const DefaultExportRowLimit = 1_000_000

// Limit is the most rows an export reads by running its statement
// again, 0 for no limit.
func (p ExportPrefs) Limit() int {
	switch {
	case p.Unlimited:
		return 0
	case p.RowLimit > 0:
		return p.RowLimit
	}
	return DefaultExportRowLimit
}

// CopyOptions say how Advanced Copy writes the chosen rows.
type CopyOptions struct {
	Delimiter  rune `json:"delimiter"`
	Header     bool `json:"header"`
	RowNumbers bool `json:"rowNumbers"`
	QuoteAll   bool `json:"quoteAll"`
	// NullText is what a NULL is written as.
	NullText string `json:"nullText"`
}

// ViewFormat is how the grids show values; the zero value shows them as
// the database gives them.
type ViewFormat struct {
	// GroupNumbers writes thousands separators in numbers.
	GroupNumbers bool `json:"groupNumbers,omitempty"`
	// Dates is "" for ISO 8601, or "short" for "Oct 8, 2026 14:05".
	Dates string `json:"dates,omitempty"`
	// BoolTicks shows booleans as ✓ and ✗.
	BoolTicks bool `json:"boolTicks,omitempty"`
	// Binary is "" for text when it is UTF-8, else hex; "hex"; or "base64".
	Binary string `json:"binary,omitempty"`
	// NullText is how NULL shows: "" for NULL.
	NullText string `json:"nullText,omitempty"`
	// Raw shows values as the database gives them, ignoring the rest.
	Raw bool `json:"raw,omitempty"`
}

func (f ViewFormat) Format(v any) string {
	if f.Raw {
		return db.Display(v)
	}
	switch x := v.(type) {
	case nil:
		if f.NullText != "" {
			return f.NullText
		}
		return "NULL"
	case bool:
		if f.BoolTicks {
			if x {
				return "✓"
			}
			return "✗"
		}
	case []byte:
		switch f.Binary {
		case "hex":
			return `\x` + hex.EncodeToString(x)
		case "base64":
			return base64.StdEncoding.EncodeToString(x)
		}
	case time.Time:
		if f.Dates == "short" {
			if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 {
				return x.Format("Jan 2, 2006")
			}
			return x.Format("Jan 2, 2006 15:04")
		}
	}
	s := db.Display(v)
	if f.GroupNumbers && db.IsNumeric(v) {
		return GroupDigits(s)
	}
	return s
}

// GroupDigits puts thousands separators in the integer part of a number.
func GroupDigits(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if len(whole) <= 3 || strings.ContainsAny(whole, "eE") {
		return sign + s
	}
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if hasFrac {
		return sign + b.String() + "." + frac
	}
	return sign + b.String()
}

// GridFontSize is the size of the grids' text.
func (s Settings) GridFontSize() float32 {
	if s.GridFont <= 0 {
		return 12.5
	}
	return s.GridFont
}
