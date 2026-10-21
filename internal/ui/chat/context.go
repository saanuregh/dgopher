package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"dgopher/internal/redact"
)

// maxEditorText is how much of the editor's text a message carries:
// past it, the selection or the statement alone.
const maxEditorText = 16 << 10

// contextBlock matches the editor's block a message begins with, and
// its summary.
var contextBlock = regexp.MustCompile(`(?s)\A<editor summary=("(?:[^"\\]|\\.)*")[^>]*>.*?</editor>\n*`)

// withContext puts the editor's block before a message: its text only
// where the agent has not had it as it is, which costs it tokens again.
func (t *Chat) withContext(text string) string {
	name, detail := t.connLabel()
	e := t.ed.EditorContext()
	if cn := t.conn(); cn != nil && cn.Config.NoRowsToChat && e.LastError != "" {
		// A database's message can hold a row's values, as a duplicate
		// key's.
		e.LastError = "it failed; its message stays with the user, as the connection keeps its data from you"
	}
	same := t.textSent != nil && *t.textSent == e.Text
	t.textSent = &e.Text
	// The agent's command line keeps what it is sent, on disk: no
	// password goes, as none goes to the history or the audit log.
	e.Text, e.Selection, e.Statement, e.LastError = redact.Secrets(e.Text), redact.Secrets(e.Selection), redact.Secrets(e.Statement), redact.Secrets(e.LastError)
	return editorBlock(e, name, detail, same) + "\n\n" + text
}

// contextSummary says what the next message carries of the editor.
func (t *Chat) contextSummary() string {
	name, _ := t.connLabel()
	return summaryOf(t.frameCtx, name)
}

// connLabel is the editor's connection: its name, and its engine and
// environment.
func (t *Chat) connLabel() (name, detail string) {
	if cn := t.conn(); cn != nil {
		cfg := cn.Config
		return cfg.Name, cfg.Engine.Label() + ", " + cfg.Env.Label()
	}
	return "", ""
}

// splitContext takes the editor's block off a message, and returns the
// message and the block's summary, "" without one.
func splitContext(text string) (message, summary string) {
	m := contextBlock.FindStringSubmatchIndex(text)
	if m == nil {
		return text, ""
	}
	summary, _ = strconv.Unquote(text[m[2]:m[3]])
	return text[m[1]:], summary
}

// editorBlock tells the agent what the editor shows, on the connection
// of a name, its engine and environment told by detail; its text, unless
// it is the same as the last message's (same).
func editorBlock(e EditorContext, conn, detail string, same bool) string {
	var b strings.Builder
	lines := func(what string) string { return linesOf(e, what) }
	fmt.Fprintf(&b, "<editor summary=%s>\n", strconv.Quote(summaryOf(e, conn)))
	if conn != "" {
		fmt.Fprintf(&b, "Connection: %s (%s)\n", conn, detail)
	}
	if e.Database != "" {
		fmt.Fprintf(&b, "Database: %s\n", e.Database)
	}
	if e.File != "" {
		fmt.Fprintf(&b, "File: %s\n", e.File)
	}
	code := func(title, sql string) {
		fmt.Fprintf(&b, "%s:\n```sql\n%s\n```\n", title, strings.TrimRight(escapeBlock(sql), "\n"))
	}
	switch {
	case e.Selection != "":
		code(lines("Selected"), e.Selection)
	case e.Statement != "":
		code(lines("Statement at the caret"), e.Statement)
	}
	switch {
	case strings.TrimSpace(e.Text) == "":
		b.WriteString("The editor is empty.\n")
	case same:
		b.WriteString("The editor's text is as in the last message.\n")
	case len(e.Text) <= maxEditorText:
		code("The editor's text", e.Text)
	default:
		fmt.Fprintf(&b, "The editor's text, %d KB, is too long to include.\n", len(e.Text)>>10)
	}
	if e.LastError != "" {
		fmt.Fprintf(&b, "The last run's error: %s\n", escapeBlock(e.LastError))
	}
	b.WriteString("</editor>")
	return b.String()
}

// summaryOf is what a message carries of the editor, on a line.
func summaryOf(e EditorContext, conn string) string {
	var summary []string
	if e.File != "" {
		summary = append(summary, e.File)
	}
	switch {
	case e.Selection != "":
		summary = append(summary, linesOf(e, "selection"))
	case e.Statement != "":
		summary = append(summary, linesOf(e, "statement"))
	}
	if conn != "" {
		summary = append(summary, conn)
	}
	return strings.Join(summary, " · ")
}

// linesOf names the selection's or the statement's lines.
func linesOf(e EditorContext, what string) string {
	if e.From == e.To {
		return fmt.Sprintf("%s, line %d", what, e.From)
	}
	return fmt.Sprintf("%s, lines %d–%d", what, e.From, e.To)
}

// escapeBlock keeps text from ending the editor's block early.
func escapeBlock(s string) string { return strings.ReplaceAll(s, "</editor>", "<\\/editor>") }
