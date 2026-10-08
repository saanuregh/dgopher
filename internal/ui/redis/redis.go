// Package redis is the Redis tab: the keys as a tree, a key's value and
// its editing, and a console of commands, all through the safety policy.
package redis

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/store"
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

	selected   string
	info       db.KeyInfo
	value      string
	fields     []db.Field
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
	renameIn  string
	renaming  bool
	newKey    *newKeyForm

	split       float32
	consoleH    float32
	consoleIn   string
	consoleLog  []consoleLine
	history     []string
	histIdx     int
	consoleList ui.ListState
}

type consoleLine struct {
	input bool
	text  string
	err   bool
}

type newKeyForm struct {
	open  bool
	typ   string
	key   string
	field string
	value string
}

const keySep = ":"

var redisTypes = []string{"All types", "string", "hash", "list", "set", "zset", "stream"}

func New(a Host, cn *connection.Conn) *Tab {
	r := &Tab{a: a, conn: cn, typeFilter: redisTypes[0], split: 340, consoleH: 520, treeRow: -1, fieldRow: -1}
	r.tree.List.Selected = &r.treeRow
	r.consoleList.FollowEnd = true
	r.rescan()
	return r
}

func (r *Tab) Title() string { return r.conn.Config.Name + " · keys" }

func (r *Tab) Connection() *connection.Conn { return r.conn }

func (r *Tab) CloseReason() string { return "" }

func (r *Tab) Close() {}

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
	typ := r.typeFilter
	if typ == redisTypes[0] {
		typ = ""
	}
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
	if r.treeKey == len(r.keys) && r.children != nil {
		return
	}
	r.treeKey = len(r.keys)
	r.children = map[string][]string{}
	seen := map[string]bool{}
	sorted := append([]string(nil), r.keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		parent := ""
		rest := k
		for {
			i := strings.Index(rest, keySep)
			if i < 0 || i == len(rest)-1 {
				break
			}
			folder := parent + rest[:i+1]
			if !seen[folder] {
				seen[folder] = true
				r.children[parent] = append(r.children[parent], folder)
			}
			parent, rest = folder, rest[i+1:]
		}
		r.children[parent] = append(r.children[parent], k)
	}
}

func isFolder(node string, r *Tab) bool {
	return strings.HasSuffix(node, keySep) && !r.keySet[node]
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
		if err == nil && info.Type != "none" {
			value, fields, err = kv.Value(ctx, key, info.Type, 1000)
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
			r.info, r.value, r.fields = info, value, fields
			r.editValue, r.editDirty = value, false
			r.ttlIn = ""
			if info.TTL > 0 {
				r.ttlIn = strconv.Itoa(int(info.TTL.Seconds()))
			}
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
	th := c.Theme()
	ui.Column(c).Grow(1).Children(func() {
		ui.SplitVertical(c, &r.consoleH, func() {
			ui.Split(c, &r.split, func() { r.keysView(c, a) }, func() { r.keyView(c, a) }).Fill()
		}, func() {
			r.consoleView(c, a)
		}).Grow(1)
	})
	_ = th
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
			})
			ui.Row(c).Gap(6).Children(func() {
				if ui.Select(c, &r.typeFilter, redisTypes).Label("Type").Changed() {
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
				if isFolder(n, r) {
					parent := n[:len(n)-1]
					if i := strings.LastIndex(parent, keySep); i >= 0 {
						parent = parent[i+1:]
					}
					ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(12)
					ui.Text(c, parent).SingleLine().Grow(1).Shrink(1)
					ui.Text(c, strconv.Itoa(r.countUnder(n))).FontSize(11).TextColor(pal.Muted)
					return
				}
				name := n
				if i := strings.LastIndex(strings.TrimSuffix(n, keySep), keySep); i >= 0 {
					name = n[i+1:]
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
					ui.Badge(c, strings.ToUpper(r.info.Type))
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
			area := ui.TextArea(c, &r.editValue).Font(widgets.MonoFont).FontSize(12.5).Grow(1).ReadOnly(ro).Label("Value")
			if area.Changed() {
				r.editDirty = r.editValue != r.value
			}
			ui.Row(c).Gap(8).Children(func() {
				if pretty := dataview.PrettyValue(r.editValue); pretty != r.editValue && ui.Button(c, "Format JSON").Clicked() {
					r.editValue = pretty
					r.editDirty = r.editValue != r.value
				}
				ui.Spacer(c)
				if r.editDirty && !ro {
					if ui.Button(c, "Revert").Clicked() {
						r.editValue, r.editDirty = r.value, false
					}
					if ui.PrimaryButton(c, "Save").Clicked() || c.Shortcut(ui.Cmd, ui.KeyS) {
						args := []string{"SET", key, r.editValue, "KEEPTTL"}
						r.write(args, r.loadKey)
					}
				}
			})
		})
		return
	case "none", "":
		return
	}
	var cols []ui.TableColumn
	switch r.info.Type {
	case "hash":
		cols = []ui.TableColumn{{Title: "Field", Width: 220}, {Title: "Value"}}
	case "list":
		cols = []ui.TableColumn{{Title: "Index", Width: 70, Align: ui.End}, {Title: "Value"}}
	case "set":
		cols = []ui.TableColumn{{Title: "Member"}}
	case "zset":
		cols = []ui.TableColumn{{Title: "Member"}, {Title: "Score", Width: 120, Align: ui.End}}
	case "stream":
		cols = []ui.TableColumn{{Title: "ID", Width: 200}, {Title: "Fields"}}
	}
	r.fieldList.Selected = &r.fieldRow
	ui.Table(c, &r.fieldList, cols, len(r.fields), func(row, col int) {
		f := r.fields[row]
		var text string
		switch {
		case r.info.Type == "zset" && col == 1:
			text = strconv.FormatFloat(f.Score, 'g', -1, 64)
		case r.info.Type == "set" || r.info.Type == "zset":
			text = f.Value
		case col == 0:
			text = f.Name
		default:
			text = f.Value
		}
		ui.Text(c, widgets.OneLine(text, 300)).Font(widgets.MonoFont).FontSize(12).SingleLine()
	}).Grow(1).Label("Items")
	if int64(len(r.fields)) < r.info.Length {
		ui.Text(c, fmt.Sprintf("Showing the first %d of %d items.", len(r.fields), r.info.Length)).FontSize(12).TextColor(pal.Muted).Padding(4, 14)
	}
	if ro || r.info.Type == "stream" {
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
		}
		if r.fieldRow >= 0 && r.fieldRow < len(r.fields) && ui.Button(c, "Remove Chosen").Clicked() {
			f := r.fields[r.fieldRow]
			var args []string
			switch r.info.Type {
			case "hash":
				args = []string{"HDEL", key, f.Name}
			case "list":
				args = []string{"LREM", key, "1", f.Value}
			case "set":
				args = []string{"SREM", key, f.Value}
			case "zset":
				args = []string{"ZREM", key, f.Value}
			}
			r.write(args, r.loadKey)
		}
	})
}

func (r *Tab) newKeyView(c *ui.Context) {
	f := r.newKey
	if f == nil {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "New Key").FontSize(15).Bold()
			ui.Select(c, &f.typ, redisTypes[1:6]).Label("Type")
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

func (r *Tab) consoleView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Fill().Background(pal.EditorBg).Children(func() {
		ui.Row(c).Padding(4, 10).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconTerminal).FontSize(13).TextColor(pal.Muted)
			ui.Text(c, "Console").FontSize(12).Bold()
			ui.Spacer(c)
			if ui.Link(c, "Clear", "").FontSize(12).Clicked() {
				r.consoleLog = nil
			}
		})
		ui.List(c, &r.consoleList, len(r.consoleLog), func(i int) {
			l := r.consoleLog[i]
			txt := ui.Text(c, l.text).Font(widgets.MonoFont).FontSize(12).Padding(1, 12).Selectable()
			switch {
			case l.input:
				txt.TextColor(th.Accent).Bold()
			case l.err:
				txt.TextColor(th.Danger)
			}
		}).Grow(1).Label("Console output")
		ui.Row(c).Padding(6, 10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, ">").Font(widgets.MonoFont).TextColor(pal.Muted)
			in := ui.TextInputBase(c, &r.consoleIn).Font(widgets.MonoFont).FontSize(12.5).Grow(1).Placeholder("Type a command, e.g. GET user:1").Label("Command")
			if in.Shortcut(0, ui.KeyUp) && len(r.history) > 0 {
				r.histIdx = max(0, r.histIdx-1)
				r.consoleIn = r.history[r.histIdx]
			}
			if in.Shortcut(0, ui.KeyDown) && len(r.history) > 0 {
				r.histIdx = min(len(r.history), r.histIdx+1)
				if r.histIdx == len(r.history) {
					r.consoleIn = ""
				} else {
					r.consoleIn = r.history[r.histIdx]
				}
			}
			if in.Submitted() {
				r.runConsole(strings.TrimSpace(r.consoleIn))
			}
		})
	})
}

func (r *Tab) runConsole(line string) {
	if line == "" {
		return
	}
	r.consoleIn = ""
	r.history = append(r.history, line)
	r.histIdx = len(r.history)
	r.consoleLog = append(r.consoleLog, consoleLine{input: true, text: "> " + line})
	args, err := db.SplitCommand(line)
	if err != nil {
		r.consoleLog = append(r.consoleLog, consoleLine{text: "(error) " + err.Error(), err: true})
		return
	}
	v := safety.ReviewRedis(&r.conn.Config, r.conn.KV, args)
	if v.Blocked != "" {
		r.a.RecordBlocked(r.conn, v.Blocked, redact.Redis(args))
		r.consoleLog = append(r.consoleLog, consoleLine{text: "(refused) " + v.Blocked, err: true})
		return
	}
	run := func() {
		kv := r.conn.KV
		st, cfg := r.conn.Project.Local, r.conn.Config
		r.a.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			start := time.Now()
			out, err := kv.Do(ctx, args)
			logged := redact.Redis(args)
			r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, logged, -1, time.Since(start), err)
			entry := storeHistory(cfg, logged, time.Since(start), err)
			st.AppendHistory(entry)
			return func() {
				if err != nil {
					r.consoleLog = append(r.consoleLog, consoleLine{text: "(error) " + err.Error(), err: true})
					return
				}
				for _, l := range strings.Split(db.FormatReply(out), "\n") {
					r.consoleLog = append(r.consoleLog, consoleLine{text: l})
				}
				if v.Writes && r.selected != "" {
					r.loadKey()
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

// storeHistory is a history entry of a statement or command that ran.
func storeHistory(cfg db.Config, sql string, d time.Duration, err error) store.HistoryEntry {
	e := store.HistoryEntry{Time: time.Now().Add(-d), ConnectionID: cfg.ID, Connection: cfg.Name, Database: cfg.Database, SQL: redact.Secrets(sql), Duration: d}
	if err != nil {
		e.Error = err.Error()
	}
	return e
}
