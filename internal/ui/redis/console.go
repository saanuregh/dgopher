package redis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/store"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type consoleLine struct {
	input bool
	text  string
	err   bool
}

// fileRun is a file of commands running in the console.
type fileRun struct {
	path   string
	total  int
	done   int
	cancel context.CancelFunc
}

// docsRetry is how long help that failed to load waits to be asked again.
const docsRetry = 30 * time.Second

// loadDocs reads the commands' help for the console to show: once, but
// again a while after it failed.
func (r *Tab) loadDocs() {
	if r.docs != nil || time.Since(r.docsAsked) < docsRetry {
		return
	}
	r.docsAsked = time.Now()
	kv := r.conn.KV
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		docs, err := kv.CommandDocs(ctx) // without help, the console still runs
		return func() {
			if err == nil && docs == nil {
				docs = map[string]db.CommandDoc{} // a server without help: not asked again
			}
			r.docs = docs
		}
	})
}

// complete completes the command being typed: the one command starting so,
// or as far as all of them agree.
func (r *Tab) complete() {
	word := r.consoleIn
	if strings.ContainsAny(word, " \t") {
		return
	}
	names := db.CompleteCommand(r.docs, word)
	if len(names) == 0 {
		return
	}
	common := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, common) {
			common = common[:len(common)-1]
		}
	}
	if len(names) == 1 {
		common += " "
	}
	if len(common) > len(word) {
		r.consoleIn = common
	}
}

// helpView shows the syntax of the command typed, or the commands its
// first letters may start.
func (r *Tab) helpView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	line := strings.TrimLeft(r.consoleIn, " ")
	if line == "" || r.docs == nil {
		return
	}
	args := strings.Fields(line)
	// A command's help once its name is whole: past it, or the only one
	// starting so; before, the commands it may be.
	if d, ok := db.DocFor(r.docs, args); ok && (len(args) > 1 || strings.HasSuffix(line, " ") || len(db.CompleteCommand(r.docs, args[0])) == 1) {
		ui.Column(c).Padding(4, 12).Gap(1).Children(func() {
			ui.Text(c, d.Name+" "+d.Syntax).Font(widgets.MonoFont).FontSize(12).SingleLine()
			summary := d.Summary
			if d.Since != "" {
				summary += " Since " + d.Since + "."
			}
			ui.Text(c, summary).FontSize(11.5).TextColor(pal.Muted).SingleLine()
		})
		return
	}
	if len(args) == 1 && !strings.HasSuffix(line, " ") {
		names := db.CompleteCommand(r.docs, args[0])
		if len(names) == 0 {
			return
		}
		shown := strings.Join(names[:min(len(names), 12)], "  ")
		if len(names) > 12 {
			shown += fmt.Sprintf("  … %d more", len(names)-12)
		}
		ui.Text(c, shown+"   Tab completes").Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted).Padding(4, 12).SingleLine()
	}
}

func (r *Tab) consoleView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	r.loadDocs()
	ui.Column(c).Fill().Background(pal.EditorBg).Children(func() {
		ui.Row(c).Padding(4, 10).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconTerminal).FontSize(13).TextColor(pal.Muted)
			ui.Text(c, "Console").FontSize(12).Bold()
			ui.Spacer(c)
			if f := r.file; f != nil {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, fmt.Sprintf("%s: %d of %d", filepath.Base(f.path), f.done, f.total)).FontSize(12).TextColor(pal.Muted)
				if ui.Link(c, "Stop", "").FontSize(12).Clicked() {
					f.cancel()
				}
			}
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
		// In a box of its own, always built: the input after it keeps its
		// place, and with it the focus, as help comes and goes.
		ui.Column(c).Children(func() { r.helpView(c) })
		ui.Row(c).Padding(6, 10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, ">").Font(widgets.MonoFont).TextColor(pal.Muted)
			in := ui.TextInputBase(c, &r.consoleIn).Font(widgets.MonoFont).FontSize(12.5).Grow(1).Placeholder("Type a command, e.g. GET user:1").Label("Command")
			if r.caretToEnd {
				// The text set last frame is the input's now.
				end := utf8.RuneCountInString(r.consoleIn)
				in.SetTextSelection(end, end)
				r.caretToEnd = false
			}
			typed := r.consoleIn
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
			if in.Shortcut(0, ui.KeyTab) {
				r.complete()
			}
			if r.consoleIn != typed {
				// A line recalled or completed: the caret goes after it.
				r.caretToEnd = true
				c.AnimationFrame()
			}
			if in.Submitted() && r.file == nil {
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

// chooseCommandFile asks for a file of commands, then runs it.
func (r *Tab) chooseCommandFile() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Run Commands File"})
		if err != nil || len(paths) == 0 {
			return
		}
		r.a.Post(func() { r.runCommandFile(paths[0]) })
	}()
}

// runCommandFile runs a file of commands in the console, one after the
// other, stopping at the first that fails: every one through the safety
// policy first, then, when any writes, once the user agrees.
func (r *Tab) runCommandFile(path string) {
	fail := func(msg string) { r.consoleLog = append(r.consoleLog, consoleLine{text: "(error) " + msg, err: true}) }
	text, err := os.ReadFile(path)
	if err != nil {
		fail(err.Error())
		return
	}
	cmds, err := db.SplitCommands(string(text))
	if err != nil {
		fail(filepath.Base(path) + ": " + err.Error())
		return
	}
	if len(cmds) == 0 {
		fail(filepath.Base(path) + " holds no command")
		return
	}
	cfg := r.conn.Config
	var v safety.Verdict
	writes := 0
	for _, cmd := range cmds {
		cv := safety.ReviewRedis(&cfg, r.conn.KV, cmd.Args)
		if cv.Blocked != "" {
			r.a.RecordBlocked(r.conn, cv.Blocked, redact.Redis(cmd.Args))
			fail(fmt.Sprintf("%s, line %d: %s", filepath.Base(path), cmd.Line, cv.Blocked))
			return
		}
		if cv.Writes {
			writes++
		}
		v.Confirm, v.TypeName = v.Confirm || cv.Confirm, v.TypeName || cv.TypeName
		v.Reasons = append(v.Reasons, cv.Reasons...)
	}
	run := func() { r.startCommandFile(path, cmds) }
	if writes == 0 && !v.Confirm {
		run()
		return
	}
	v.Reasons = append([]string{fmt.Sprintf("%d of the %d commands write, one after the other, stopping at the first that fails: those before it stay.", writes, len(cmds))}, uniqueReasons(v.Reasons)...)
	lines := make([]string, 0, min(len(cmds), 50))
	for _, cmd := range cmds[:min(len(cmds), 50)] {
		lines = append(lines, redact.Redis(cmd.Args))
	}
	if len(cmds) > 50 {
		lines = append(lines, fmt.Sprintf("… and %d more", len(cmds)-50))
	}
	r.a.AskConfirm(r.conn, v, fmt.Sprintf("Run %d commands of %s on %s?", len(cmds), filepath.Base(path), cfg.Name), "Run", strings.Join(lines, "\n"), run)
}

func (r *Tab) startCommandFile(path string, cmds []db.CommandLine) {
	// Asked again: the connection may have changed while the user read.
	cfg := r.conn.Config
	for _, cmd := range cmds {
		if v := safety.ReviewRedis(&cfg, r.conn.KV, cmd.Args); v.Blocked != "" {
			r.a.RecordBlocked(r.conn, v.Blocked, redact.Redis(cmd.Args))
			r.consoleLog = append(r.consoleLog, consoleLine{text: fmt.Sprintf("(refused) line %d: %s", cmd.Line, v.Blocked), err: true})
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &fileRun{path: path, total: len(cmds), cancel: cancel}
	r.file = f
	r.consoleLog = append(r.consoleLog, consoleLine{input: true, text: "> run " + filepath.Base(path)})
	kv, st := r.conn.KV, r.conn.Project.Local
	go func() {
		defer cancel()
		for i, cmd := range cmds {
			if ctx.Err() != nil {
				r.a.Post(func() { r.consoleLog = append(r.consoleLog, consoleLine{text: "(stopped)", err: true}) })
				break
			}
			// Stop waits for the command sent: cut off, it might have run
			// while the log says it failed.
			start := time.Now()
			cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
			out, err := kv.Do(cctx, cmd.Args)
			ccancel()
			logged := redact.Redis(cmd.Args)
			r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, logged, -1, time.Since(start), err)
			st.AppendHistory(storeHistory(cfg, logged, time.Since(start), err))
			r.a.Post(func() {
				f.done = i + 1
				r.consoleLog = append(r.consoleLog, consoleLine{input: true, text: fmt.Sprintf("%d> %s", cmd.Line, logged)})
				if err != nil {
					r.consoleLog = append(r.consoleLog, consoleLine{text: "(error) " + err.Error(), err: true})
					return
				}
				for _, l := range strings.Split(db.FormatReply(out), "\n") {
					r.consoleLog = append(r.consoleLog, consoleLine{text: l})
				}
			})
			if err != nil {
				break
			}
		}
		r.a.Post(func() {
			r.file = nil
			r.rescan()
			if r.selected != "" {
				r.loadKey()
			}
		})
	}()
}

// uniqueReasons drops the reasons said already, which commands alike give.
func uniqueReasons(reasons []string) []string {
	var out []string
	for _, r := range reasons {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}
