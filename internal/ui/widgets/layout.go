package widgets

// Layout is the windows' layout as the user left it, the sizes dragged and
// the sidebar shown or not, which the app keeps in its UI state, each value
// under its JSON name.
type Layout struct {
	SidebarWidth  float32 `json:"sidebarWidth"`
	SidebarHidden bool    `json:"sidebarHidden"`
	// EditorPercent is an editor's percentage of the height it shares with
	// its results, which a new editor takes.
	EditorPercent float32 `json:"editorPercent"`
	// Results is how the editors' results show, which every editor
	// shares, as VS Code's panel.
	Results ResultsState `json:"results"`
	// SideWidth is the width of the left of two tabs side by side.
	SideWidth float32 `json:"sideWidth"`
	// RedisKeysWidth and RedisConsoleHeight are the width of the Redis
	// browser's keys and the height above its console.
	RedisKeysWidth     float32 `json:"redisKeysWidth"`
	RedisConsoleHeight float32 `json:"redisConsoleHeight"`
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
	return Layout{SidebarWidth: 260, EditorPercent: 40, SideWidth: 560, RedisKeysWidth: 340, RedisConsoleHeight: 520}
}
