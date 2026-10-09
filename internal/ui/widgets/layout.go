package widgets

// Layout is the windows' layout as the user left it, the sizes dragged and
// the sidebar shown or not, which the app keeps in its UI state, each value
// under its JSON name.
type Layout struct {
	SidebarWidth  float32 `json:"sidebarWidth"`
	SidebarHidden bool    `json:"sidebarHidden"`
	// EditorHeight is an editor's height above its results, which a new
	// editor takes.
	EditorHeight float32 `json:"editorHeight"`
	// SideWidth is the width of the left of two tabs side by side.
	SideWidth float32 `json:"sideWidth"`
	// RedisKeysWidth and RedisConsoleHeight are the width of the Redis
	// browser's keys and the height above its console.
	RedisKeysWidth     float32 `json:"redisKeysWidth"`
	RedisConsoleHeight float32 `json:"redisConsoleHeight"`
}

// DefaultLayout is the layout of a first run.
func DefaultLayout() Layout {
	return Layout{SidebarWidth: 260, EditorHeight: 260, SideWidth: 560, RedisKeysWidth: 340, RedisConsoleHeight: 520}
}
