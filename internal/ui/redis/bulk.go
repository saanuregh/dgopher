package redis

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// What a bulk dialog does with keys.
type bulkKind int

const (
	bulkDelete bulkKind = iota
	bulkExport
	bulkImport
)

// sampleKeys is how many keys a count names.
const sampleKeys = 5

// bulkDialog deletes or exports the keys matching a pattern, or imports
// keys from a file.
type bulkDialog struct {
	open    bool
	kind    bulkKind
	pattern string
	path    string
	replace bool // an import replaces the keys there already

	// counted is the pattern count and sample are of.
	counted string
	count   int64
	sample  []string
	dumps   []db.KeyDump // an import's keys, read from path

	running bool
	cancel  context.CancelFunc
	done    int64
	err     string
	result  string
}

func (r *Tab) openBulk(kind bulkKind) {
	pattern := r.pattern
	if pattern == "" {
		pattern = "*"
	}
	r.bulk = &bulkDialog{open: true, kind: kind, pattern: pattern}
	if kind == bulkImport {
		r.chooseImport(r.bulk)
	}
}

// background runs work with a cancel the dialog offers, then its result
// on the UI thread.
func (r *Tab) background(b *bulkDialog, work func(ctx context.Context) func()) {
	ctx, cancel := context.WithCancel(context.Background())
	b.running, b.cancel, b.done, b.err, b.result = true, cancel, 0, "", ""
	r.a.Background(func() func() {
		defer cancel()
		then := work(ctx)
		return func() {
			b.running, b.cancel = false, nil
			then()
		}
	})
}

// countKeys counts the keys matching the pattern, naming a few.
func (r *Tab) countKeys(b *bulkDialog) {
	kv, pattern := r.conn.KV, b.pattern
	r.background(b, func(ctx context.Context) func() {
		n, sample, err := kv.CountMatching(ctx, pattern, sampleKeys)
		return func() {
			if err != nil {
				b.err = err.Error()
				return
			}
			b.counted, b.count, b.sample = pattern, n, sample
		}
	})
}

// deleteKeys deletes the keys matching the pattern counted, once the
// policy and the user agree.
func (r *Tab) deleteKeys(b *bulkDialog) {
	cfg := r.conn.Config
	pattern, n := b.counted, b.count
	v := safety.ReviewRedis(&cfg, r.conn.KV, []string{"UNLINK", pattern})
	if v.Blocked != "" {
		r.a.RecordBlocked(r.conn, v.Blocked, "UNLINK keys matching "+pattern)
		b.err = v.Blocked
		return
	}
	// Always asked: the keys are many, and counted a moment ago.
	v.Confirm, v.TypeName = true, v.TypeName || cfg.Env == db.Production
	v.Reasons = append([]string{fmt.Sprintf("Deletes the %s keys matching %s, and any matching by then.", widgets.HumanCount(n), pattern)}, v.Reasons...)
	if pattern == "*" {
		v.Reasons = append(v.Reasons, "The pattern matches every key of the database.")
	}
	preview := "UNLINK each key matching " + pattern + "\n\n" + strings.Join(b.sample, "\n")
	r.a.AskConfirm(r.conn, v, fmt.Sprintf("Delete %s keys from %s?", widgets.HumanCount(n), cfg.Name), "Delete", preview, func() {
		kv := r.conn.KV
		r.background(b, func(ctx context.Context) func() {
			start := time.Now()
			gone, err := kv.DeleteMatching(ctx, pattern, func(n int64) { r.a.Post(func() { b.done = n }) })
			r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, "UNLINK each key matching "+pattern, gone, time.Since(start), err)
			return func() {
				b.counted = ""
				r.selected = ""
				r.rescan()
				if err != nil {
					b.err = fmt.Sprintf("%s, after %s keys were deleted.", err, widgets.HumanCount(gone))
					return
				}
				b.result = "Deleted " + widgets.HumanCount(gone) + " keys."
			}
		})
	})
}

// chooseExport asks where the export goes, then writes it.
func (r *Tab) chooseExport(b *bulkDialog) {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(r.conn.Config.Name) + "-keys.jsonl"
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Export Keys", DefaultPath: name})
		if err != nil || path == "" {
			return
		}
		r.a.Post(func() {
			b.path = path
			r.exportKeys(b)
		})
	}()
}

func (r *Tab) exportKeys(b *bulkDialog) {
	kv, cfg, pattern, path := r.conn.KV, r.conn.Config, b.pattern, b.path
	r.background(b, func(ctx context.Context) func() {
		start := time.Now()
		written, skipped, err := exportTo(ctx, kv, pattern, path, func(n int64) { r.a.Post(func() { b.done = n }) })
		r.a.RecordRun(cfg, audit.KindExport, cfg.Database, "keys matching "+pattern+" to "+path, written, time.Since(start), err)
		return func() {
			if err != nil {
				b.err = err.Error()
				return
			}
			b.result = fmt.Sprintf("Exported %s keys to %s.", widgets.HumanCount(written), filepath.Base(path))
			if skipped > 0 {
				b.result += fmt.Sprintf(" %s of a module's types were passed over.", widgets.HumanCount(skipped))
			}
		}
	})
}

// exportTo writes the keys into a file, which is removed when the export
// fails: half an export would import as if it were whole.
func exportTo(ctx context.Context, kv *db.KV, pattern, path string, progress func(int64)) (written, skipped int64, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
	w := bufio.NewWriter(f)
	written, skipped, err = kv.ExportKeys(ctx, pattern, w, progress)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		if ctx.Err() != nil {
			err = errors.New("cancelled")
		}
	}
	return written, skipped, err
}

// chooseImport asks for the file of keys, then reads it.
func (r *Tab) chooseImport(b *bulkDialog) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Import Keys"})
		if err != nil || len(paths) == 0 {
			return
		}
		r.a.Post(func() { r.readImport(b, paths[0]) })
	}()
}

func (r *Tab) readImport(b *bulkDialog, path string) {
	b.path, b.dumps = path, nil
	r.background(b, func(context.Context) func() {
		f, err := os.Open(path)
		var dumps []db.KeyDump
		if err == nil {
			dumps, err = db.ReadKeyDumps(f)
			f.Close()
		}
		return func() {
			if err != nil {
				b.err = err.Error()
				return
			}
			b.dumps = dumps
		}
	})
}

// importKeys writes the file's keys, once the policy and, where it asks,
// the user agree.
func (r *Tab) importKeys(b *bulkDialog) {
	cfg := r.conn.Config
	// Every kind of command the import runs, as the policy weighs them.
	var v safety.Verdict
	var kinds []string
	for _, d := range b.dumps {
		for _, args := range db.ImportCommands(d, b.replace) {
			if slices.Contains(kinds, args[0]) {
				continue
			}
			kinds = append(kinds, args[0])
			kv := safety.ReviewRedis(&cfg, r.conn.KV, args)
			if kv.Blocked != "" {
				r.a.RecordBlocked(r.conn, kv.Blocked, redact.Redis(args))
				b.err = kv.Blocked
				return
			}
			v.Confirm, v.TypeName = v.Confirm || kv.Confirm, v.TypeName || kv.TypeName
			v.Reasons = append(v.Reasons, kv.Reasons...)
		}
	}
	if b.replace {
		v.Confirm = true
		v.Reasons = append(v.Reasons, "Keys there already are deleted, then written as the file holds them.")
	}
	dumps, replace, path := b.dumps, b.replace, b.path
	run := func() {
		kv := r.conn.KV
		r.background(b, func(ctx context.Context) func() {
			start := time.Now()
			written, skipped, err := kv.ImportKeys(ctx, dumps, replace, func(n int64) { r.a.Post(func() { b.done = n }) })
			r.a.RecordRun(cfg, audit.KindImport, cfg.Database, "keys from "+path, written, time.Since(start), err)
			return func() {
				r.rescan()
				if err != nil {
					b.err = fmt.Sprintf("%s, after %s keys were written.", err, widgets.HumanCount(written))
					return
				}
				b.result = fmt.Sprintf("Imported %s keys.", widgets.HumanCount(written))
				if skipped > 0 {
					b.result += fmt.Sprintf(" %s there already were left as they were.", widgets.HumanCount(skipped))
				}
			}
		})
	}
	if v.Confirm {
		title := fmt.Sprintf("Import %s keys into %s?", widgets.HumanCount(int64(len(dumps))), cfg.Name)
		r.a.AskConfirm(r.conn, v, title, "Import", strings.Join(kinds, ", ")+" from "+path, run)
		return
	}
	run()
}

func (r *Tab) bulkView(c *ui.Context) {
	b := r.bulk
	if b == nil {
		return
	}
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ro := r.conn.Config.ReadOnly
	ui.Modal(c, &b.open, func() {
		ui.Column(c).Width(560).Gap(12).Children(func() {
			title := map[bulkKind]string{bulkDelete: "Delete Keys", bulkExport: "Export Keys", bulkImport: "Import Keys"}[b.kind]
			ui.Text(c, title+" · "+r.conn.Config.Name).FontSize(15).Bold()
			ui.Form(c, func() {
				if b.kind != bulkImport {
					ui.Field(c, "Matching", func() {
						if ui.TextInput(c, &b.pattern).Font(widgets.MonoFont).Label("Pattern").Disabled(b.running).Changed() {
							b.counted = ""
						}
					}).Description("A glob, as SCAN takes: * any characters, ? one, [abc] one of them.")
				} else {
					ui.Field(c, "File", func() {
						ui.Row(c).Gap(6).Grow(1).Children(func() {
							ui.Text(c, b.path).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1)
							if ui.Button(c, "Choose…").Disabled(b.running).Clicked() {
								r.chooseImport(b)
							}
						})
					}).Description("Lines of JSON, as Export Keys writes them.")
					ui.Field(c, "", func() {
						ui.Checkbox(c, &b.replace, "Replace the keys there already").Disabled(b.running)
					})
				}
			})
			switch {
			case b.kind == bulkDelete && b.counted == b.pattern && b.counted != "":
				text := widgets.HumanCount(b.count) + " keys match"
				if len(b.sample) > 0 {
					text += ", as " + strings.Join(b.sample, ", ")
				}
				ui.Text(c, text+".").FontSize(12.5).SingleLine()
			case b.kind == bulkImport && b.dumps != nil:
				ui.Text(c, fmt.Sprintf("%s keys in the file.", widgets.HumanCount(int64(len(b.dumps))))).FontSize(12.5)
			}
			if b.err != "" {
				ui.Text(c, b.err).TextColor(th.Danger).Selectable()
			} else if b.result != "" {
				ui.Text(c, b.result).TextColor(pal.Muted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if b.running {
					ui.Spinner(c).Size(14, 14)
					if b.done > 0 {
						ui.Text(c, widgets.HumanCount(b.done)+" keys…").FontSize(12).TextColor(pal.Muted)
					}
				}
				ui.Spacer(c)
				if b.running {
					if ui.Button(c, "Cancel").Clicked() {
						b.cancel()
					}
					return
				}
				if ui.Button(c, "Close").Clicked() {
					b.open = false
				}
				switch b.kind {
				case bulkDelete:
					if b.counted != b.pattern || b.counted == "" {
						if ui.PrimaryButton(c, "Count Keys").Disabled(strings.TrimSpace(b.pattern) == "").Clicked() {
							r.countKeys(b)
						}
					} else if ui.PrimaryButton(c, "Delete "+widgets.HumanCount(b.count)+" Keys…").Disabled(ro || b.count == 0).Clicked() {
						r.deleteKeys(b)
					}
				case bulkExport:
					if ui.PrimaryButton(c, "Export…").Disabled(strings.TrimSpace(b.pattern) == "").Clicked() {
						r.chooseExport(b)
					}
				case bulkImport:
					if ui.PrimaryButton(c, "Import").Disabled(ro || len(b.dumps) == 0).Clicked() {
						r.importKeys(b)
					}
				}
			})
		})
	})
	if !b.open {
		if b.cancel != nil {
			b.cancel()
		}
		r.bulk = nil
	}
}
