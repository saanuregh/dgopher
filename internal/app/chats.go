package app

import (
	"os"
	"path/filepath"

	"dgopher/internal/connection"
	"dgopher/internal/ui/query"
)

// Connections are the app's connections, as the navigator lists them.
func (a *App) Connections() []*connection.Conn { return a.conns }

// ChatDir is the folder a chat without a project works in, made when
// missing.
func (a *App) ChatDir() string {
	dir := filepath.Join(a.st.Dir(), "chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return a.st.Dir()
	}
	return dir
}

// toggleChat shows or hides the AI chat of the editor in front; with
// none in front, it opens a new editor on the SQL connection in front,
// with its chat.
func (a *App) toggleChat() {
	if q, ok := a.ActiveTab().(*query.Tab); ok {
		q.ToggleChat()
		return
	}
	cn := a.activeConn()
	if cn == nil || !cn.Config.Engine.IsSQL() {
		a.ShowError("No SQL editor", "The AI chat works beside a SQL editor: open one on a SQL connection, then ask.")
		return
	}
	if q := a.createQueryFile(cn, "", connection.HeaderLine(cn)); q != nil {
		a.Connect(cn, nil)
		q.ToggleChat()
	}
}
