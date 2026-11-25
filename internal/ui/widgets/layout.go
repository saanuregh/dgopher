package widgets

// Layout is the windows' layout as the user left it, the sizes dragged and
// the sidebar shown or not, which the app keeps in its UI state, each value
// under its JSON name. No value is left out when empty: the app reads back
// the names a layout has, and one left out was never read, nor written
// once set back to empty.
type Layout struct {
	SidebarWidth  float32 `json:"sidebarWidth"`
	SidebarHidden bool    `json:"sidebarHidden"`
	// EditorPercent is an editor's percentage of the height it shares with
	// its results, which a new editor takes.
	EditorPercent float32 `json:"editorPercent"`
	// Results is how the editors' results show, which every editor
	// shares, as VS Code's panel.
	Results ResultsState `json:"results"`
	// RedisKeysWidth and RedisPanelHeight are the width of the Redis
	// browser's keys and the height of the panel under them, as its
	// console: the keys and the value take the rest of a tall window.
	RedisKeysWidth   float32 `json:"redisKeysWidth"`
	RedisPanelHeight float32 `json:"redisPanelHeight"`
	// ChatWidth is the width of the editors' AI chat, 0 for its default.
	ChatWidth float32 `json:"chatWidth"`
	// GridPanelWidth is the width of the panel at a grid's right, as
	// dragged, 0 for its default.
	GridPanelWidth float32 `json:"gridPanelWidth"`
	// NavFilesHeight is the height of the navigator's pane of the
	// project's files, under the database tree, as dragged, 0 for its
	// default.
	NavFilesHeight float32 `json:"navFilesHeight"`
	// NavClosed are the navigator's headings left closed, as Connections
	// or Queries of a project, a line each: the others open as they first
	// show. Text, for the layout to compare as a value.
	NavClosed string `json:"navClosed"`
}

// ResultsState is how the editors' results show: under the editor,
// hidden down to their bar of results, or filling the tab instead of the
// editor.
type ResultsState string

const (
	ResultsShown     ResultsState = ""
	ResultsHidden    ResultsState = "hidden"
	ResultsMaximized ResultsState = "maximized"
)

// DefaultLayout is the layout of a first run.
func DefaultLayout() Layout {
	return Layout{SidebarWidth: 260, EditorPercent: 40, RedisKeysWidth: 340, RedisPanelHeight: 200}
}
