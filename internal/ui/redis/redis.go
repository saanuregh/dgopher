// Package redis is the Redis tab: the keys as a tree, a key's value and
// its editing, and a console of commands, all through the safety policy.
package redis

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/decode"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Tab browses the keys of a Redis database, with a console below.
type Tab struct {
	a    Host
	conn *connection.Conn

	pattern    string // applied
	patternIn  string
	typeFilter string
	keys       []string
	keySet     map[string]bool
	cursor     db.ScanPos
	scanning   bool
	scanDone   bool
	scanErr    string
	dbsize     int64

	tree     ui.OutlineState[string]
	treeRow  int
	children map[string][]string // prefix → its children, folders ending in the separator
	treeKey  int                 // len(keys) the tree was built for
	treeSep  string              // the separator it was built with

	selected   string
	info       db.KeyInfo
	value      string
	whole      bool // value is the whole string, not its start
	strView    decodeView
	itemView   decodeView // the chosen item's
	itemName   string     // the item itemView and itemRaw were made for
	itemValue  string
	itemRaw    string // the chosen item's value as stored, as shown
	fields     []db.Field
	itemsPos   db.ItemsPos
	itemsDone  bool
	loadingKey bool
	keyErr     string
	valueGen   int

	editValue string
	editDirty bool
	fieldList ui.ListState
	fieldRow  int
	newName   string
	newValue  string
	newScore  string
	ttlIn     string
	fieldTTL  string // seconds, for the chosen hash field
	renameIn  string
	renaming  bool
	newKey    *newKeyForm
	bulk      *bulkDialog

	split       float32
	consoleH    float32
	consoleIn   string
	consoleLog  []consoleLine
	history     []string
	histIdx     int
	consoleList ui.ListState
	docs        map[string]db.CommandDoc // the commands' help; nil before it is read, or without it
	docsAsked   time.Time                // when the help was asked for; zero before, and after it failed a while
	caretToEnd  bool                     // the console's caret goes to the end of its line next frame
	file        *fileRun

	panel   int // the panel below the keys: console, monitor, slow log or memory
	monitor monitorState
	slow    slowLogState
	memory  memoryState
	pubsub  pubSubState
	stream  streamState
	vector  vectorState
	series  seriesState
	search  searchState
}

type newKeyForm struct {
	open  bool
	typ   string
	key   string
	field string
	value string
}

// newKeyTypes are the types of the keys the new key form makes.
var newKeyTypes = []string{"string", "hash", "list", "set", "zset"}

const (
	// itemsPage is how many items of a key are read at a time.
	itemsPage = 500
	// stringStart is how much of a string is read and shown before it is
	// asked for whole: the editor lays a larger text out slowly.
	stringStart = 64 << 10
)

func New(a Host, cn *connection.Conn) *Tab {
	r := &Tab{a: a, conn: cn, typeFilter: allTypes, split: 340, consoleH: 520, treeRow: -1, fieldRow: -1}
	r.tree.List.Selected = &r.treeRow
	r.consoleList.FollowEnd = true
	r.rescan()
	return r
}

func (r *Tab) Title() string { return r.conn.Config.Name + " · keys" }

func (r *Tab) Connection() *connection.Conn { return r.conn }

// CloseReason says what closing the tab stops: a file of commands running.
func (r *Tab) CloseReason() string {
	if r.file != nil {
		return fmt.Sprintf("%s is running, %d of its %d commands done: closing stops it.", filepath.Base(r.file.path), r.file.done, r.file.total)
	}
	return ""
}

func (r *Tab) Close() {
	if r.file != nil {
		r.file.cancel()
	}
	for _, cancel := range []context.CancelFunc{r.monitor.cancel, r.memory.cancel, r.pubsub.cancel} {
		if cancel != nil {
			cancel()
		}
	}
}

func (r *Tab) rescan() {
	r.keys, r.keySet, r.cursor, r.scanDone, r.scanErr = nil, map[string]bool{}, db.ScanPos{}, false, ""
	r.children, r.treeKey = nil, -1
	r.scan()
}

// scan reads the next keys, a thousand at a time, without blocking the
// server as KEYS would.
func (r *Tab) scan() {
	if r.scanning || r.scanDone {
		return
	}
	r.scanning = true
	kv := r.conn.KV
	cursor, pattern := r.cursor, r.pattern
	typ := typeOf(r.typeFilter)
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var found []string
		var done bool
		var err error
		for {
			var batch []string
			batch, cursor, done, err = kv.Scan(ctx, cursor, pattern, typ, 500)
			found = append(found, batch...)
			if err != nil || done || len(found) >= 1000 {
				break
			}
		}
		size, _ := kv.DBSize(ctx)
		return func() {
			r.scanning = false
			r.dbsize = size
			if err != nil {
				r.scanErr = err.Error()
				return
			}
			for _, k := range found {
				if !r.keySet[k] {
					r.keySet[k] = true
					r.keys = append(r.keys, k)
				}
			}
			r.cursor = cursor
			r.scanDone = done
		}
	})
}

// buildTree groups the keys by their separator.
func (r *Tab) buildTree() {
	sep := r.conn.Config.Separator()
	if r.treeKey == len(r.keys) && r.treeSep == sep && r.children != nil {
		return
	}
	r.treeKey, r.treeSep = len(r.keys), sep
	r.children = map[string][]string{}
	seen := map[string]bool{}
	sorted := append([]string(nil), r.keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		parent := ""
		rest := k
		for {
			i := strings.Index(rest, sep)
			if i < 0 || i == len(rest)-len(sep) {
				break
			}
			folder := parent + rest[:i+len(sep)]
			if !seen[folder] {
				seen[folder] = true
				r.children[parent] = append(r.children[parent], folder)
			}
			parent, rest = folder, rest[i+len(sep):]
		}
		r.children[parent] = append(r.children[parent], k)
	}
}

func isFolder(node string, r *Tab) bool {
	return strings.HasSuffix(node, r.treeSep) && !r.keySet[node]
}

func (r *Tab) countUnder(prefix string) int {
	n := 0
	for _, k := range r.keys {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// open shows a key.
func (r *Tab) open(key string) {
	r.selected = key
	r.stream, r.vector, r.series = streamState{}, vectorState{}, seriesState{}
	r.loadKey()
}

func (r *Tab) loadKey() {
	key := r.selected
	if key == "" {
		return
	}
	r.loadingKey, r.keyErr = true, ""
	r.valueGen++
	gen := r.valueGen
	kv := r.conn.KV
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		info, err := kv.Info(ctx, key)
		var value string
		var fields []db.Field
		var pos db.ItemsPos
		done := true
		switch {
		case err != nil || info.Type == "none":
		case info.Type == "string":
			value, err = kv.ReadString(ctx, key, stringStart)
		case info.Type == "ReJSON-RL":
			value, err = readJSON(ctx, kv, key)
		case slices.Contains(itemTypes, info.Type):
			fields, pos, done, err = readPage(ctx, kv, key, info.Type, db.ItemsPos{})
		}
		return func() {
			if gen != r.valueGen {
				return
			}
			r.loadingKey = false
			if err != nil {
				r.keyErr = err.Error()
				return
			}
			// Whole when shorter than asked: the length read before may
			// be of another moment.
			r.info, r.value, r.whole = info, value, len(value) < stringStart
			r.strView = newDecodeView(value)
			// An element's vector and attributes, and a series' samples,
			// are read again too.
			r.vector, r.series.readFor = vectorState{}, ""
			r.fields, r.itemsPos, r.itemsDone = nil, pos, done
			r.addItems(fields)
			r.editValue, r.editDirty = widgets.TextStart(value, stringStart), false
			r.ttlIn = ""
			if info.TTL > 0 {
				r.ttlIn = strconv.Itoa(int(info.TTL.Seconds()))
			}
		}
	})
}

// readPage reads a page of a key's items, a hash's fields with their
// times to live where the server keeps them.
func readPage(ctx context.Context, kv *db.KV, key, typ string, pos db.ItemsPos) ([]db.Field, db.ItemsPos, bool, error) {
	fields, next, done, err := kv.ReadItems(ctx, key, typ, pos, itemsPage)
	if err != nil || typ != "hash" || !kv.FieldExpiry() {
		return fields, next, done, err
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	ttls, err := kv.FieldTTLs(ctx, key, names)
	for i := range ttls {
		fields[i].TTL = ttls[i]
	}
	return fields, next, done, err
}

// addItems adds read items to those shown, but for those a scan gave
// again; a hash's in the order of their names, a set's of their values.
func (r *Tab) addItems(fields []db.Field) {
	seen := make(map[string]bool, len(r.fields))
	for _, f := range r.fields {
		seen[f.Name+"\x00"+f.Value] = true
	}
	for _, f := range fields {
		if id := f.Name + "\x00" + f.Value; !seen[id] {
			seen[id] = true
			r.fields = append(r.fields, f)
		}
	}
	// The chosen item stays chosen where the sort moves it: actions on
	// it go by its row.
	var chosen *db.Field
	if r.fieldRow >= 0 && r.fieldRow < len(r.fields) {
		f := r.fields[r.fieldRow]
		chosen = &f
	}
	switch r.info.Type {
	case "hash":
		sort.SliceStable(r.fields, func(i, j int) bool { return r.fields[i].Name < r.fields[j].Name })
	case "set":
		sort.SliceStable(r.fields, func(i, j int) bool { return r.fields[i].Value < r.fields[j].Value })
	}
	if chosen != nil {
		r.fieldRow = slices.IndexFunc(r.fields, func(f db.Field) bool { return f.Name == chosen.Name && f.Value == chosen.Value })
	}
}

// loadMore reads the next page of the key's items.
func (r *Tab) loadMore() {
	if r.loadingKey || r.itemsDone {
		return
	}
	r.loadingKey = true
	gen := r.valueGen
	kv, key, typ, pos := r.conn.KV, r.selected, r.info.Type, r.itemsPos
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		fields, next, done, err := readPage(ctx, kv, key, typ, pos)
		return func() {
			if gen != r.valueGen {
				return
			}
			r.loadingKey = false
			if err != nil {
				r.keyErr = err.Error()
				return
			}
			r.itemsPos, r.itemsDone = next, done
			r.addItems(fields)
		}
	})
}

// loadWhole reads all of a string shown in part.
func (r *Tab) loadWhole() {
	r.loadingKey = true
	gen := r.valueGen
	kv, key := r.conn.KV, r.selected
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		value, err := kv.ReadString(ctx, key, 0)
		return func() {
			if gen != r.valueGen {
				return
			}
			r.loadingKey = false
			if err != nil {
				r.keyErr = err.Error()
				return
			}
			r.value, r.whole, r.editValue, r.editDirty = value, true, value, false
		}
	})
}

// write runs a command through the safety policy, then reloads the key.
func (r *Tab) write(args []string, then func()) {
	v := safety.ReviewRedis(&r.conn.Config, r.conn.KV, args)
	if v.Blocked != "" {
		r.a.RecordBlocked(r.conn, v.Blocked, redact.Redis(args))
		r.a.ShowError("Not allowed", v.Blocked)
		return
	}
	run := func() {
		kv, cfg := r.conn.KV, r.conn.Config
		r.a.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			out, err := kv.Do(ctx, args)
			r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, redact.Redis(args), -1, time.Since(start), err)
			if err == nil && strings.EqualFold(args[0], "RENAMENX") && out == int64(0) {
				err = fmt.Errorf("a key named %s already exists: nothing was renamed", args[2])
			}
			return func() {
				if err != nil {
					r.a.ShowError(strings.ToUpper(args[0])+" failed", err.Error())
					return
				}
				if then != nil {
					then()
				}
			}
		})
	}
	if v.Confirm {
		r.a.AskConfirm(r.conn, v, "Run "+strings.ToUpper(args[0])+" on "+r.conn.Config.Name+"?", "Run", redact.Redis(args), run)
		return
	}
	run()
}

func (r *Tab) View(c *ui.Context) {
	a := r.a
	ui.Column(c).Grow(1).Children(func() {
		ui.SplitVertical(c, &r.consoleH, func() {
			ui.Split(c, &r.split, func() { r.keysView(c, a) }, func() { r.keyView(c, a) }).Fill()
		}, func() {
			r.panelView(c)
		}).Grow(1)
	})
}

func (r *Tab) keysView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(8).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Row(c).Gap(6).Children(func() {
				if widgets.SearchBox(c, &r.patternIn, "Pattern, e.g. user:*", 0).Submitted() {
					r.pattern = r.patternIn
					r.rescan()
				}
				if widgets.IconButton(c, widgets.IconRefresh, "Rescan").Clicked() {
					r.pattern = r.patternIn
					r.rescan()
				}
				if widgets.IconButton(c, widgets.IconPlus, "New key").Clicked() {
					r.newKey = &newKeyForm{open: true, typ: "string"}
				}
				widgets.IconButton(c, widgets.IconMore, "More").Menu(func(m *ui.Menu) {
					ro := r.conn.Config.ReadOnly
					if m.Item("Delete Keys Matching…").Disabled(ro).Chosen() {
						r.openBulk(bulkDelete)
					}
					m.Separator()
					if m.Item("Export Keys…").Chosen() {
						r.openBulk(bulkExport)
					}
					if m.Item("Import Keys…").Disabled(ro).Chosen() {
						r.openBulk(bulkImport)
					}
					m.Separator()
					if m.Item("Run Commands File…").Disabled(r.file != nil).Chosen() {
						r.chooseCommandFile()
					}
				})
			})
			ui.Row(c).Gap(6).Children(func() {
				if ui.Select(c, &r.typeFilter, typeLabels).Label("Type").Changed() {
					r.rescan()
				}
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%d of %d keys", len(r.keys), r.dbsize)).FontSize(12).TextColor(pal.Muted)
			})
		})
		if r.scanErr != "" {
			ui.Text(c, r.scanErr).TextColor(th.Danger).Padding(8)
		}
		r.buildTree()
		tree := ui.Outline(c, &r.tree, r.children[""], func(n string) []string {
			if isFolder(n, r) {
				return r.children[n]
			}
			return nil
		}, func(n string) {
			ui.Row(c).Gap(6).Grow(1).Children(func() {
				sep := r.treeSep
				if isFolder(n, r) {
					parent := strings.TrimSuffix(n, sep)
					if i := strings.LastIndex(parent, sep); i >= 0 {
						parent = parent[i+len(sep):]
					}
					ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(12)
					ui.Text(c, parent).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, strconv.Itoa(r.countUnder(n))).FontSize(11).TextColor(pal.Muted)
					return
				}
				name := n
				if i := strings.LastIndex(strings.TrimSuffix(n, sep), sep); i >= 0 {
					name = n[i+len(sep):]
				}
				ui.Icon(c, widgets.IconKey).TextColor(pal.Muted).FontSize(12)
				ui.Text(c, name).SingleLine().Grow(1).Shrink(1).Tooltip(n)
			})
		}).Grow(1).Label("Keys")
		if tree.Changed() && r.treeRow >= 0 && r.treeRow < r.tree.Rows() {
			if n := r.tree.Item(r.treeRow); !isFolder(n, r) {
				r.open(n)
			}
		}
		ui.Row(c).Padding(6, 8).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			switch {
			case r.scanning:
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, "Scanning…").FontSize(12).TextColor(pal.Muted)
			case !r.scanDone:
				if ui.Button(c, "Scan more").Clicked() {
					r.scan()
				}
			default:
				ui.Text(c, "Scan complete").FontSize(12).TextColor(pal.Muted)
			}
		})
	})
	r.newKeyView(c)
	r.bulkView(c)
}

func (r *Tab) keyView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if r.selected == "" {
		ui.Column(c).Fill().Center().Gap(6).Children(func() {
			ui.Icon(c, widgets.IconKey).FontSize(28).TextColor(pal.Muted)
			ui.Text(c, "Choose a key").TextColor(pal.Muted)
		})
		return
	}
	ui.Column(c).Fill().Children(func() {
		ui.Column(c).Padding(10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				if r.info.Type != "" {
					ui.Badge(c, strings.ToUpper(typeLabel(r.info.Type)))
				}
				if r.renaming {
					in := ui.TextInput(c, &r.renameIn).AutoFocus().Grow(1).Font(widgets.MonoFont).Label("New key name")
					if in.Submitted() && r.renameIn != "" && r.renameIn != r.selected {
						from, to := r.selected, r.renameIn
						// RENAMENX: never over a key that exists.
						r.write([]string{"RENAMENX", from, to}, func() {
							r.renaming = false
							r.rescan()
							r.open(to)
						})
					}
					if in.Shortcut(0, ui.KeyEscape) {
						r.renaming = false
					}
				} else {
					ui.Text(c, r.selected).Font(widgets.MonoFont).Bold().SingleLine().Grow(1).Shrink(1).Selectable()
				}
				if widgets.IconButton(c, widgets.IconCopy, "Copy key name").Clicked() {
					a.WriteClipboard(r.selected)
				}
				if widgets.IconButton(c, widgets.IconRefresh, "Reload").Clicked() {
					r.loadKey()
				}
				if !r.conn.Config.ReadOnly {
					if widgets.IconButton(c, widgets.IconWand, "Rename").Clicked() {
						r.renaming, r.renameIn = true, r.selected
					}
					if widgets.IconButton(c, widgets.IconTrash, "Delete key").Clicked() {
						key := r.selected
						a.AskDiscard("Delete "+key+"?", "The key and its value are removed from "+r.conn.Config.Name+".", func() {
							r.write([]string{"DEL", key}, func() {
								delete(r.keySet, key)
								for i, k := range r.keys {
									if k == key {
										r.keys = append(r.keys[:i], r.keys[i+1:]...)
										break
									}
								}
								r.treeKey = -1
								r.selected = ""
							})
						})
					}
				}
			})
			ui.Row(c).Gap(14).Children(func() {
				meta := func(label, value string) {
					ui.Row(c).Gap(4).Children(func() {
						ui.Text(c, label).FontSize(12).TextColor(pal.Muted)
						ui.Text(c, value).FontSize(12)
					})
				}
				unit := "items"
				if r.info.Type == "string" {
					unit = "bytes"
				}
				meta("Size", fmt.Sprintf("%d %s", r.info.Length, unit))
				if r.info.Memory >= 0 {
					meta("Memory", widgets.HumanBytes(r.info.Memory))
				}
				ttl := "none"
				if r.info.TTL > 0 {
					ttl = r.info.TTL.Round(time.Second).String()
				}
				meta("TTL", ttl)
				if r.info.Type == "none" {
					ui.Text(c, "This key no longer exists.").FontSize(12).TextColor(th.Warning)
				}
				ui.Spacer(c)
				if !r.conn.Config.ReadOnly {
					ui.TextInput(c, &r.ttlIn).Placeholder("TTL seconds").Width(110).FontSize(12).Label("TTL in seconds")
					if ui.Button(c, "Set TTL").Clicked() {
						if secs, err := strconv.Atoi(strings.TrimSpace(r.ttlIn)); err == nil && secs > 0 {
							r.write([]string{"EXPIRE", r.selected, strconv.Itoa(secs)}, r.loadKey)
						} else if strings.TrimSpace(r.ttlIn) == "" {
							r.write([]string{"PERSIST", r.selected}, r.loadKey)
						}
					}
				}
			})
		})
		if r.keyErr != "" {
			ui.Text(c, r.keyErr).TextColor(th.Danger).Padding(12)
			return
		}
		if r.loadingKey && r.info.Type == "" {
			ui.Row(c).Grow(1).Center().Children(func() { ui.Spinner(c) })
			return
		}
		r.valueView(c, a)
	})
}

func (r *Tab) valueView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ro := r.conn.Config.ReadOnly
	key := r.selected
	switch r.info.Type {
	case "string":
		ui.Column(c).Grow(1).Padding(10, 14).Gap(8).Children(func() {
			// Decoded, a value is read only; shown in part, it is not
			// decoded, as its start alone does not decode.
			if r.whole && r.strView.view(c, r.value, "Value", r.a.Background) {
				return
			}
			// A string shown in part is not saved: its rest would go.
			area := ui.TextArea(c, &r.editValue).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(ro || !r.whole).Label("Value")
			if area.Changed() {
				r.editDirty = r.editValue != r.value
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if !r.whole {
					note := fmt.Sprintf("Showing the first %s of %s.", widgets.HumanBytes(int64(len(r.value))), widgets.HumanBytes(r.info.Length))
					if f := decode.Detect([]byte(r.value)); f != "" {
						note += " It looks like " + string(f) + ": loaded whole, it shows decoded."
					}
					ui.Text(c, note).FontSize(12).TextColor(pal.Muted).Shrink(1)
					if ui.Button(c, "Load All").Disabled(r.loadingKey).Clicked() {
						r.loadWhole()
					}
				}
				if pretty := dataview.PrettyValue(r.editValue); pretty != r.editValue && ui.Button(c, "Format JSON").Clicked() {
					r.editValue = pretty
					r.editDirty = r.editValue != r.value
				}
				ui.Spacer(c)
				if r.editDirty && !ro && r.whole {
					if ui.Button(c, "Revert").Clicked() {
						r.editValue, r.editDirty = r.value, false
					}
					if ui.PrimaryButton(c, "Save").Clicked() || a.KeysTo(r) && c.Shortcut(ui.Cmd, ui.KeyS) {
						args := []string{"SET", key, r.editValue, "KEEPTTL"}
						r.write(args, r.loadKey)
					}
				}
			})
		})
		return
	case "ReJSON-RL":
		r.jsonView(c, ro)
		return
	case "TSDB-TYPE":
		r.seriesView(c, ro)
		return
	case "none", "":
		return
	}
	if !slices.Contains(itemTypes, r.info.Type) {
		ui.Text(c, "The browser has no viewer for a "+typeLabel(r.info.Type)+": read and change it with its commands in the console.").
			FontSize(12.5).TextColor(pal.Muted).Padding(12, 14)
		return
	}
	if r.info.Type == "stream" {
		ui.Row(c).Padding(6, 14).Children(func() {
			if ui.Segmented(c, &r.stream.panel, "Entries", "Consumer Groups").Label("Stream view").Changed() && r.stream.panel == streamGroups {
				r.loadGroups()
			}
		})
		if r.stream.panel == streamGroups {
			r.groupsView(c)
			return
		}
	}
	var cols []ui.TableColumn
	switch r.info.Type {
	case "hash":
		cols = []ui.TableColumn{{Title: "Field", Width: 220}, {Title: "Value"}}
		if r.conn.KV.FieldExpiry() {
			cols = append(cols, ui.TableColumn{Title: "TTL", Width: 90, Align: ui.End})
		}
	case "list":
		cols = []ui.TableColumn{{Title: "Index", Width: 70, Align: ui.End}, {Title: "Value"}}
	case "set":
		cols = []ui.TableColumn{{Title: "Member"}}
	case "zset":
		cols = []ui.TableColumn{{Title: "Member"}, {Title: "Score", Width: 120, Align: ui.End}}
	case "stream":
		cols = []ui.TableColumn{{Title: "ID", Width: 200}, {Title: "Fields"}}
	case "array":
		cols = []ui.TableColumn{{Title: "Index", Width: 90, Align: ui.End}, {Title: "Value"}}
	case "vectorset":
		cols = []ui.TableColumn{{Title: "Element", Width: 260}, {Title: "Attributes"}}
	}
	r.fieldList.Selected = &r.fieldRow
	ui.Table(c, &r.fieldList, cols, len(r.fields), func(row, col int) {
		f := r.fields[row]
		var text string
		switch {
		case r.info.Type == "zset" && col == 1:
			text = strconv.FormatFloat(f.Score, 'g', -1, 64)
		case r.info.Type == "hash" && col == 2:
			if f.TTL > 0 {
				text = f.TTL.Round(time.Second).String()
			}
		case r.info.Type == "set" || r.info.Type == "zset":
			text = f.Value
		case col == 0:
			text = f.Name
		default:
			text = f.Value
		}
		ui.Text(c, widgets.OneLine(text, 300)).Font(widgets.MonoFont).FontSize(12).SingleLine()
	}).Grow(1).Label("Items")
	if !r.itemsDone {
		ui.Row(c).Padding(4, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("Showing %d of %d items.", len(r.fields), r.info.Length)).FontSize(12).TextColor(pal.Muted)
			if ui.Button(c, "Load More").Disabled(r.loadingKey).Clicked() {
				r.loadMore()
			}
		})
	}
	r.itemDetail(c)
	if ro {
		return
	}
	// Add, change and remove items.
	ui.Row(c).Padding(8, 14).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		switch r.info.Type {
		case "hash":
			ui.TextInput(c, &r.newName).Placeholder("Field").Width(160).Label("Field")
			ui.TextInput(c, &r.newValue).Placeholder("Value").Grow(1).Label("Value")
			if ui.Button(c, "Set").Clicked() && r.newName != "" {
				r.write([]string{"HSET", key, r.newName, r.newValue}, func() { r.newName, r.newValue = "", ""; r.loadKey() })
			}
			if r.fieldRow >= 0 && r.fieldRow < len(r.fields) && r.conn.KV.FieldExpiry() {
				field := r.fields[r.fieldRow].Name
				ui.TextInput(c, &r.fieldTTL).Placeholder("TTL seconds").Width(100).Label("Field TTL in seconds")
				if ui.Button(c, "Expire Field").Clicked() {
					if secs, err := strconv.Atoi(strings.TrimSpace(r.fieldTTL)); err == nil && secs > 0 {
						r.write([]string{"HEXPIRE", key, strconv.Itoa(secs), "FIELDS", "1", field}, func() { r.fieldTTL = ""; r.loadKey() })
					}
				}
				if r.fields[r.fieldRow].TTL > 0 && ui.Button(c, "Persist Field").Clicked() {
					r.write([]string{"HPERSIST", key, "FIELDS", "1", field}, r.loadKey)
				}
			}
		case "list":
			ui.TextInput(c, &r.newValue).Placeholder("Value").Grow(1).Label("Value")
			if ui.Button(c, "Append").Clicked() {
				r.write([]string{"RPUSH", key, r.newValue}, func() { r.newValue = ""; r.loadKey() })
			}
			if r.fieldRow >= 0 && r.fieldRow < len(r.fields) && ui.Button(c, "Replace Chosen").Clicked() {
				r.write([]string{"LSET", key, r.fields[r.fieldRow].Name, r.newValue}, func() { r.newValue = ""; r.loadKey() })
			}
		case "set":
			ui.TextInput(c, &r.newValue).Placeholder("Member").Grow(1).Label("Member")
			if ui.Button(c, "Add").Clicked() {
				r.write([]string{"SADD", key, r.newValue}, func() { r.newValue = ""; r.loadKey() })
			}
		case "zset":
			ui.TextInput(c, &r.newValue).Placeholder("Member").Grow(1).Label("Member")
			ui.TextInput(c, &r.newScore).Placeholder("Score").Width(90).Label("Score")
			if ui.Button(c, "Add").Clicked() {
				if _, err := strconv.ParseFloat(r.newScore, 64); err == nil {
					r.write([]string{"ZADD", key, r.newScore, r.newValue}, func() { r.newValue, r.newScore = "", ""; r.loadKey() })
				}
			}
		case "array":
			ui.TextInput(c, &r.newName).Placeholder("Index").Width(100).Font(widgets.MonoFont).Label("Index")
			ui.TextInput(c, &r.newValue).Placeholder("Value").Grow(1).Label("Value")
			if ui.Button(c, "Set").Disabled(strings.TrimSpace(r.newName) == "").Clicked() {
				r.write([]string{"ARSET", key, strings.TrimSpace(r.newName), r.newValue}, func() { r.newName, r.newValue = "", ""; r.loadKey() })
			}
			if ui.Button(c, "Insert").Tooltip("At the array's next index").Clicked() {
				r.write([]string{"ARINSERT", key, r.newValue}, func() { r.newValue = ""; r.loadKey() })
			}
		case "vectorset":
			ui.TextInput(c, &r.newName).Placeholder("Element").Width(140).Font(widgets.MonoFont).Label("Element")
			ui.TextInput(c, &r.newValue).Placeholder("Vector, e.g. 0.1 0.5 0.2").Grow(1).Font(widgets.MonoFont).Label("Vector")
			ui.TextInput(c, &r.newScore).Placeholder("Attributes as JSON").Width(180).Font(widgets.MonoFont).Label("Attributes")
			if ui.Button(c, "Add").Clicked() {
				r.addVector()
			}
		case "stream":
			ui.TextInput(c, &r.newName).Placeholder("ID, * for the next").Width(140).Font(widgets.MonoFont).Label("Entry ID")
			ui.TextInput(c, &r.newValue).Placeholder("field value field value").Font(widgets.MonoFont).Grow(1).Label("Entry fields")
			if ui.Button(c, "Add Entry").Clicked() {
				r.addEntry()
			}
		}
		if r.fieldRow >= 0 && r.fieldRow < len(r.fields) && ui.Button(c, "Remove Chosen").Clicked() {
			f := r.fields[r.fieldRow]
			var args []string
			switch r.info.Type {
			case "hash":
				args = []string{"HDEL", key, f.Name}
			case "list":
				// The item at its index, not the first of the same value;
				// none when the list changed meanwhile.
				args = []string{"EVAL", removeListItem, "1", key, f.Name, f.Value}
			case "stream":
				args = []string{"XDEL", key, f.Name}
			case "array":
				args = []string{"ARDEL", key, f.Name}
			case "vectorset":
				args = []string{"VREM", key, f.Name}
			case "set":
				args = []string{"SREM", key, f.Value}
			case "zset":
				args = []string{"ZREM", key, f.Value}
			}
			r.write(args, r.loadKey)
		}
	})
}

// itemDetail shows the chosen item's value whole, decoded where its
// first bytes name a format.
func (r *Tab) itemDetail(c *ui.Context) {
	if r.fieldRow < 0 || r.fieldRow >= len(r.fields) {
		return
	}
	f := r.fields[r.fieldRow]
	if r.info.Type == "vectorset" {
		r.vectorDetail(c, f)
		return
	}
	// Made once for each item: comparing the strings shared is cheap.
	if r.itemName != f.Name || r.itemValue != f.Value {
		r.itemName, r.itemValue = f.Name, f.Value
		r.itemView, r.itemRaw = newDecodeView(f.Value), widgets.TextStart(decode.Text([]byte(f.Value)), stringStart)
	}
	th := c.Theme()
	ui.Column(c).Height(200).Padding(8, 14).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		if !r.itemView.view(c, f.Value, "Item", r.a.Background) {
			ui.TextArea(c, &r.itemRaw).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(true).Label("Item value")
		}
	})
}

// removeListItem removes the item of a list at an index, when it still
// holds the value shown: it marks it with a value no list holds, then
// removes the mark, atomically. Without TIME, which Redis before 5 does
// not let a script write after.
const removeListItem = `local v = redis.call('LINDEX', KEYS[1], ARGV[1])
if v ~= ARGV[2] then return redis.error_reply('the list changed: reload it') end
local mark = '\0dgopher-removed\0'
redis.call('LSET', KEYS[1], ARGV[1], mark)
return redis.call('LREM', KEYS[1], 1, mark)`

// addEntry adds an entry to the stream shown: its fields typed as the
// console takes arguments, by pairs.
func (r *Tab) addEntry() {
	fields, err := db.SplitCommand(r.newValue)
	switch {
	case err != nil:
		r.a.ShowError("Not added", err.Error())
		return
	case len(fields) == 0 || len(fields)%2 != 0:
		r.a.ShowError("Not added", "An entry's fields go by pairs: a field, then its value.")
		return
	}
	id := strings.TrimSpace(r.newName)
	if id == "" {
		id = "*"
	}
	r.write(append([]string{"XADD", r.selected, id}, fields...), func() { r.newName, r.newValue = "", ""; r.loadKey() })
}

func (r *Tab) newKeyView(c *ui.Context) {
	f := r.newKey
	if f == nil {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "New Key").FontSize(15).Bold()
			ui.Select(c, &f.typ, newKeyTypes).Label("Type")
			ui.TextInput(c, &f.key).Placeholder("Key name, e.g. user:42").AutoFocus().Label("Key")
			if f.typ == "hash" {
				ui.TextInput(c, &f.field).Placeholder("Field").Label("Field")
			}
			if f.typ == "zset" {
				ui.TextInput(c, &f.field).Placeholder("Score").Label("Score")
			}
			ui.TextArea(c, &f.value).Placeholder("Value").Lines(2, 8).Label("Value")
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Create").Clicked() && f.key != "" {
					var args []string
					switch f.typ {
					case "string":
						args = []string{"SET", f.key, f.value, "NX"}
					case "hash":
						args = []string{"HSET", f.key, f.field, f.value}
					case "list":
						args = []string{"RPUSH", f.key, f.value}
					case "set":
						args = []string{"SADD", f.key, f.value}
					case "zset":
						args = []string{"ZADD", f.key, f.field, f.value}
					}
					key := f.key
					f.open = false
					r.write(args, func() {
						if !r.keySet[key] {
							r.keySet[key] = true
							r.keys = append(r.keys, key)
							r.treeKey = -1
						}
						r.open(key)
					})
				}
			})
		})
	})
	if !f.open {
		r.newKey = nil
	}
}
