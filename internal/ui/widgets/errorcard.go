package widgets

import (
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"

	"github.com/egoist/mygo/ui"
)

// ErrorAction is a button beside an error, as Retry.
type ErrorAction struct {
	Label string
	Run   func()
}

// ErrorReport is an error as the user sees it.
type ErrorReport struct {
	Title   string
	Hint    string // plain-language cause, may be ""
	Detail  string // full redacted text
	Actions []ErrorAction
}

// ErrorCard draws a report: title, hint, the full text selectable under a
// Details disclosure (open when there is no hint), Copy, and the actions.
// It runs the action clicked and reports whether one was.
func ErrorCard(c *ui.Context, r ErrorReport, open *bool, copyText func(string)) bool {
	t := c.Theme()
	clicked := -1
	ui.Column(c).Gap(12).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Icon(c, IconAlert).TextColor(t.Danger).FontSize(IconLarge)
			ui.Text(c, r.Title).FontSize(TextTitle).Bold()
		})
		details := func() {
			ui.Scroll(c).MaxHeight(300).Children(func() {
				ui.Text(c, r.Detail).Selectable()
			})
		}
		if r.Hint == "" {
			// Without a hint the full text is all there is to read.
			details()
		} else {
			ui.Text(c, r.Hint)
			ui.Collapsible(c, "Details", open, details)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "Copy").Clicked() && copyText != nil {
				copyText(r.Detail)
			}
			for i, a := range r.Actions {
				var b ui.Element
				if i == len(r.Actions)-1 {
					b = ui.PrimaryButton(c.Key(i), a.Label)
				} else {
					b = ui.Button(c.Key(i), a.Label)
				}
				if b.Clicked() {
					clicked = i
				}
			}
		})
	})
	if clicked >= 0 && r.Actions[clicked].Run != nil {
		r.Actions[clicked].Run()
	}
	return clicked >= 0
}

// ErrorBanner draws a report in a row where the failure happened: its
// title and the first line of its hint, else of its text, with the text
// whole on hover, the report's actions, and Details, which hands the report
// to show. The text shown is redacted.
func ErrorBanner(c *ui.Context, r ErrorReport, show func(ErrorReport)) {
	t := c.Theme()
	r, line := bannerText(r)
	NoticeBar(c, t.Danger, func() {
		ui.Text(c, line).Grow(1).Shrink(1).Selectable().Tooltip(r.Detail)
		reportButtons(c, r, show, false)
	})
}

// ErrorBlock draws a report as ErrorBanner does, stacked for a narrow
// place such as a dashboard panel: the text above, the buttons under it.
func ErrorBlock(c *ui.Context, r ErrorReport, show func(ErrorReport)) {
	t := c.Theme()
	r, line := bannerText(r)
	ui.Column(c).Padding(8, 12).Gap(6).Background(TintNotice(t.Danger)).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Icon(c, IconAlert).TextColor(t.Danger).FontSize(IconMedium)
			ui.Text(c, line).FontSize(TextBody).Grow(1).Shrink(1).Selectable().Tooltip(r.Detail)
		})
		ui.Row(c).Gap(8).Wrap().Children(func() { reportButtons(c, r, show, true) })
	})
}

// bannerText redacts a report and gives the line a banner shows of it.
func bannerText(r ErrorReport) (ErrorReport, string) {
	r.Detail = redact.Secrets(r.Detail)
	line := r.Hint
	if line == "" {
		line = r.Detail
	}
	return r, r.Title + ": " + FirstLine(line)
}

// reportButtons draws a report's actions and Details, which hands the
// report to show; compact ones fit a short place.
func reportButtons(c *ui.Context, r ErrorReport, show func(ErrorReport), compact bool) {
	button := func(c *ui.Context, label string) ui.Element {
		b := ui.Button(c, label)
		if compact {
			b.Padding(3, 10).FontSize(TextSmall)
		}
		return b
	}
	for i, a := range r.Actions {
		if button(c.Key(i), a.Label).Clicked() && a.Run != nil {
			a.Run()
		}
	}
	if button(c, "Details").Clicked() && show != nil {
		show(r)
	}
}

// ErrorReportOf is a database error's report: its text, and the hint
// db.DescribeError finds, as for a transaction that clashed.
func ErrorReportOf(title string, err error) ErrorReport {
	return ErrorReport{Title: title, Hint: db.DescribeError(err).Hint, Detail: err.Error()}
}

// ErrorText is a database error's text followed by that hint, where the
// error shows as text alone.
func ErrorText(err error) string {
	if hint := db.DescribeError(err).Hint; hint != "" {
		return err.Error() + "\n\n" + hint
	}
	return err.Error()
}

// ConnectionReport is a failed connection's report, offering to edit the
// connection and to retry; retry and edit may be nil.
func ConnectionReport(cn *connection.Conn, retry, edit func()) ErrorReport {
	r := ErrorReport{Title: "Could not connect to " + cn.Config.Name, Hint: cn.ErrKind.Hint(), Detail: cn.Err}
	if edit != nil {
		r.Actions = append(r.Actions, ErrorAction{Label: "Edit Connection", Run: edit})
	}
	if retry != nil {
		r.Actions = append(r.Actions, ErrorAction{Label: "Retry", Run: retry})
	}
	return r
}
