package chat

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// maxToolOutput is how much of a tool's output an expanded call shows.
const maxToolOutput = 6000

// View draws the chat; keys is whether it takes the keys pressed.
func (t *Chat) View(c *ui.Context, keys bool) {
	if keys && keymap.Pressed(c, keymap.ChatStop) {
		t.Stop()
	}
	if t.running || t.starting {
		c.After(time.Second) // the time it has taken, as it goes
	}
	t.fx.step(c, t.Busy(), t.fieldFocused)
	t.frameCtx = t.ed.EditorContext() // once, for what the frame shows of it
	ui.Column(c).Grow(1).Shrink(1).MinHeight(0).Children(func() {
		t.header(c)
		ui.Scroll(c.Key("transcript")).TrackScroll(&t.scroll).Grow(1).Shrink(1).Children(func() {
			ui.Column(c).Padding(12, 14).Gap(12).FillWidth().Children(func() {
				t.transcriptView(c)
			})
		})
		t.followEnd()
		ui.Column(c).Padding(0, 12-ringPadding, 10-ringPadding, 12-ringPadding).Gap(8).FillWidth().Children(func() {
			if len(t.requests) > 0 {
				ui.Column(c).Gap(8).Padding(0, ringPadding).Children(func() {
					for _, r := range slices.Clone(t.requests) { // a card answered goes
						t.requestCard(c, r)
					}
				})
			}
			t.inputView(c)
		})
	})
}

// header is the chat's bar: what it is, the cost so far, and what
// starts over or hides it. The agent, model and effort are chosen under
// the message field, where they apply.
func (t *Chat) header(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	widgets.Bar(c, "AI Chat", func() {
		t.fx.orbView(c, 18, true)
		ui.Text(c, "AI Chat").FontSize(13).Bold().SingleLine()
		ui.Spacer(c)
		if t.cost > 0 {
			ui.Text(c, fmt.Sprintf("$%.2f", t.cost)).FontSize(12).TextColor(pal.Muted).Tooltip("What the replies cost so far, as the agent reports it")
		}
		if widgets.IconButton(c.Key("new"), widgets.IconPlus, "New Chat").Tooltip("Start the conversation over").Disabled(t.sess == nil && !t.starting && t.transcriptEmpty()).Clicked() {
			t.Restart(t.provider)
		}
		if t.OnHide != nil && widgets.IconButton(c.Key("hide"), widgets.IconX, "Hide Chat").Tooltip(keymap.Hint("Hide the chat, which keeps the conversation", keymap.ToggleChat)).Clicked() {
			t.OnHide()
		}
	})
}

// pickers choose the agent, its model and its effort, small, under the
// message field.
func (t *Chat) pickers(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	picker := func(key, label, tip string) ui.Element {
		return ui.ButtonBase(c.Key(key)).Padding(2, 6).Radius(6).Label(label).Tooltip(tip).Shrink(1).MinWidth(0).Children(func() {
			ui.Text(c, label+" ▾").FontSize(11.5).TextColor(pal.Muted).SingleLine()
		})
	}
	widgets.MenuOn(c, picker("provider", providerLabel(t.provider), "The agent the chat talks to; another starts over"), func(m *widgets.Menu) {
		for _, p := range Providers {
			if m.Item(p.Label).Checked(p.Name == t.provider).Chosen() && p.Name != t.provider {
				t.Restart(p.Name)
				s := t.a.Settings()
				s.ChatProvider = p.Name
				t.a.SaveSettings()
			}
		}
	})
	t.modelPickerView(c, picker("model", widgets.OneLine(t.modelLabel(), 28), "The model, from the next message on"))
	if efforts := t.efforts(); len(efforts) > 0 {
		label := "default effort"
		if t.effort != "" {
			label = string(t.effort) + " effort"
		}
		widgets.MenuOn(c, picker("effort", label, "How long the model reasons before it answers"), func(m *widgets.Menu) {
			if m.Item("Default").Checked(t.effort == "").Chosen() {
				t.chooseModel(t.modelID, "")
			}
			for _, e := range efforts {
				if m.Item(string(e)).Checked(e == t.effort).Chosen() {
					t.chooseModel(t.modelID, e)
				}
			}
		})
	}
}

func (t *Chat) transcriptEmpty() bool {
	return len(t.transcript.Items()) == 0 && len(t.sent) == 0 && len(t.waiting) == 0
}

// followEnd keeps the transcript at its end as it grows, while the user
// has not scrolled up from it.
func (t *Chat) followEnd() {
	s := &t.scroll
	if s.MaxY-s.Y > 24 && !s.Scrolling() {
		t.follow = false
	} else if s.MaxY-s.Y <= 4 {
		t.follow = true
	}
	n := len(t.requests)
	for _, it := range t.transcript.Items() {
		n += len(it.Text) + 1
		if it.Tool != nil {
			n += len(it.Tool.Output)
		}
	}
	n += len(t.sent) + len(t.waiting) + len(t.notices)
	if n != t.lastLen {
		t.lastLen = n
		if t.follow {
			s.Y = math.MaxFloat32
		}
	}
}

// transcriptView shows the conversation.
func (t *Chat) transcriptView(c *ui.Context) {
	th := c.Theme()
	if t.transcriptEmpty() && !t.starting && t.startErr == "" {
		t.emptyView(c)
	}
	keys := map[string]bool{} // the user's messages', unique
	for _, it := range t.transcript.Items() {
		if it.SubagentID != "" {
			continue // a subagent's work shows as its task's call
		}
		if it.Kind == agent.ItemUserMessage {
			if !it.Synthetic {
				// Keyed as it was sent, so that it stays in place as the
				// agent shows it back; by its item where an earlier chat's
				// conversation holds the same ID twice.
				key := "msg-" + cmp.Or(it.ClientID, it.ID)
				if keys[key] {
					key = "item-" + it.ID
				}
				keys[key] = true
				userBubble(c.Key(key), it.Text)
			}
			continue
		}
		t.itemView(c, it)
	}
	for _, s := range t.sent {
		// Shown back by the agent, it is the transcript's.
		if !keys["msg-"+s.id] {
			userBubble(c.Key("msg-"+s.id), s.text)
		}
	}
	for i, text := range t.waiting {
		userBubble(c.Key(fmt.Sprint("waiting-", i)), text)
	}
	for i, n := range t.notices {
		col := th.Warning
		if n.err {
			col = th.Danger
		}
		ui.Row(c.Key(fmt.Sprintf("notice-%d", i))).Gap(8).Padding(6, 10).Radius(8).Background(col.Alpha(0.12)).Transition(entering).Children(func() {
			ui.Icon(c, widgets.IconAlert).TextColor(col).FontSize(13)
			ui.Text(c, n.text).FontSize(12.5).Grow(1).Shrink(1).Selectable()
		})
	}
	switch {
	case t.startErr != "":
		ui.Column(c).Gap(8).Padding(10, 12).Radius(8).Background(th.Danger.Alpha(0.10)).Children(func() {
			ui.Text(c, t.startErr).Selectable()
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Try Again").Clicked() {
					t.start()
				}
			})
		})
	case t.starting:
		t.status(c, "Starting "+providerLabel(t.provider)+"…")
	case t.running && !t.writing():
		t.status(c, fmt.Sprintf("Thinking… %ds", int(time.Since(t.turnAt).Seconds())))
	}
}

// entering is how the transcript's items come in: rising a little as
// they fade in.
var entering = ui.ElementTransition{Enter: &ui.Motion{Opacity: 0, Y: 8}, Duration: 220 * time.Millisecond, Position: true}

// status is the agent at work: its orb, lively, and what it does,
// shimmering.
func (t *Chat) status(c *ui.Context, text string) {
	ui.Row(c.Key("status")).Gap(8).AlignItems(ui.Center).Transition(entering).Children(func() {
		t.fx.orbView(c, 16, true)
		t.fx.shimmerText(c, text, 12.5)
	})
}

// writing reports whether the reply's text is coming in, which shows
// the work itself.
func (t *Chat) writing() bool {
	items := t.transcript.Items()
	if len(items) == 0 {
		return false
	}
	last := items[len(items)-1]
	return last.Status == agent.ItemRunning && (last.Kind == agent.ItemAssistantMessage || last.Kind == agent.ItemToolCall)
}

// emptyView is a new chat: what it sees and does, and a few questions
// to start with.
func (t *Chat) emptyView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	ctx := t.frameCtx
	ui.Column(c).Gap(14).Padding(24, 4).Children(func() {
		ui.Column(c).Gap(6).Children(func() {
			t.fx.orbView(c, 48, true).Margin(0, 0, 6, 0)
			ui.Text(c, "Ask about this editor").FontSize(16).Bold()
			ui.Text(c, "Each message carries the editor's connection, the statement at the caret or the selection, and the last error. "+
				providerLabel(t.provider)+" reads the schema and runs only the reads you allow; it cannot change files or data.").
				FontSize(12.5).TextColor(pal.Muted)
		})
		ui.Column(c).Gap(6).Children(func() {
			n := 0
			suggest := func(icon *ui.SVG, label, prompt string, send bool) {
				th := c.Theme()
				b := ui.ButtonBase(c.Key("suggest-"+label)).Padding(7, 12).Radius(999).Border(1, th.Border).Background(th.Background).Label(label).AlignSelf(ui.Start)
				b.Transition(ui.ElementTransition{Enter: &ui.Motion{Opacity: 0, Y: 6}, Animation: &ui.Animation{Duration: 260 * time.Millisecond, Delay: time.Duration(n) * 60 * time.Millisecond}, Position: true})
				n++
				if b.Hovered() {
					b.Background(th.Accent.Alpha(0.08)).Border(1, th.Accent.Alpha(0.5))
				}
				b.Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icon).FontSize(13).TextColor(th.Accent)
						ui.Text(c, label).FontSize(12.5).SingleLine().Shrink(1)
					})
				})
				if b.Clicked() {
					if send {
						t.Send(prompt)
					} else {
						t.input, t.wantFocus = prompt, true
						t.inputKey++ // the field takes the text, the caret after it
					}
				}
			}
			if ctx.LastError != "" {
				suggest(widgets.IconAlert, "Why did the last run fail?", "Why did the last run fail, and how do I fix it?", true)
			}
			if ctx.Selection != "" || ctx.Statement != "" {
				suggest(widgets.IconInfo, "Explain this statement", "Explain what the statement does, step by step.", true)
				suggest(widgets.IconClock, "Make it faster", "How could the statement run faster? Read the schema for the indexes it could use.", true)
			}
			suggest(widgets.IconPencil, "Write a query that…", "Write a query that ", false)
		})
	})
}

// userBubble is a message the user sent, with the editor's block it
// carried as a line under it.
func userBubble(c *ui.Context, text string) {
	th, muted := c.Theme(), widgets.PaletteOf(c).Muted
	text, summary := splitContext(text)
	ui.Column(c).AlignItems(ui.End).Gap(3).Transition(ui.ElementTransition{Enter: &ui.Motion{Opacity: 0, X: 16, Scale: 0.97}, Duration: 240 * time.Millisecond, Position: true}).Children(func() {
		ui.Text(c, text).Padding(8, 12).Radius(14).Background(th.Accent.Alpha(0.12)).MaxWidth(620).Selectable()
		if summary != "" {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Tooltip("What the message carried of the editor").Children(func() {
				ui.Icon(c, widgets.IconCode).FontSize(10).TextColor(muted)
				ui.Text(c, summary).FontSize(11).TextColor(muted).SingleLine()
			})
		}
	})
}

// itemView shows an item of the transcript.
func (t *Chat) itemView(c *ui.Context, it agent.Item) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	key := c.Key("item-" + it.ID)
	switch it.Kind {
	case agent.ItemAssistantMessage, agent.ItemPlan:
		ui.Column(key).FillWidth().Transition(entering).Children(func() {
			opts := &markdown.Options{CodeBlock: t.codeBlock, WrapCode: true} // the panel is narrow
			if it.Status == agent.ItemRunning {
				markdown.Stream(c, it.Text, opts).FillWidth()
			} else {
				markdown.View(c, it.Text, opts).FillWidth()
			}
		})
	case agent.ItemReasoning:
		if strings.TrimSpace(it.Text) == "" {
			return
		}
		open := t.expanded[it.ID]
		b := ui.ButtonBase(key).Label("Thinking").AlignItems(ui.Start).Transition(entering)
		b.Children(func() {
			ui.Column(c).Gap(4).Grow(1).Children(func() {
				label := "Thinking ▸"
				if open {
					label = "Thinking ▾"
				}
				ui.Text(c, label).FontSize(12).TextColor(pal.Muted)
				if open {
					ui.Text(c, it.Text).FontSize(12.5).TextColor(pal.Muted).Selectable()
				}
			})
		})
		if b.Clicked() {
			t.expanded[it.ID] = !open
		}
	case agent.ItemToolCall:
		t.toolView(key, c, it)
	case agent.ItemError:
		ui.Text(key, it.Text).TextColor(th.Danger).Selectable()
	case agent.ItemCompaction:
		ui.Text(key, "The conversation was summarized to make room.").FontSize(12).TextColor(pal.Muted)
	}
}

// toolView shows a tool call on a line, its input and output when
// opened.
func (t *Chat) toolView(key *ui.Context, c *ui.Context, it agent.Item) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	call := it.Tool
	if call == nil {
		call = &agent.ToolCall{Name: "tool"}
	}
	open := t.expanded[it.ID]
	ui.Column(key).Radius(8).Border(1, th.Border).Background(th.Background).Clip().Transition(entering).Children(func() {
		head := ui.ButtonBase(c.Key("head")).Padding(5, 10).Label("Tool call " + toolName(call))
		head.Children(func() {
			ui.Row(c).Gap(8).Grow(1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, toolIcon(call.Kind)).FontSize(12).TextColor(pal.Muted)
				// Running only while the agent works: a call the agent left
				// so, stopping, moves no more, nor asks for frames.
				running := it.Status == agent.ItemRunning && t.Busy()
				if running {
					t.fx.shimmerText(c, toolName(call), 12.5)
				} else {
					ui.Text(c, toolName(call)).FontSize(12.5).Bold()
				}
				ui.Text(c, toolSummary(call)).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1).Grow(1)
				switch {
				case running:
					t.fx.orbView(c, 12, true)
				case it.Status == agent.ItemRunning:
					ui.Text(c, "stopped").FontSize(11).TextColor(pal.Muted)
				case it.Status == agent.ItemFailed || it.Status == agent.ItemDeclined:
					ui.Icon(c, widgets.IconX).FontSize(12).TextColor(th.Danger)
				case it.Status == agent.ItemDone:
					if call.IsError {
						ui.Icon(c, widgets.IconAlert).FontSize(12).TextColor(th.Warning)
					} else {
						ui.Icon(c, widgets.IconCheck).FontSize(12).TextColor(th.Success)
					}
				}
			})
		})
		if head.Clicked() {
			t.expanded[it.ID] = !open
		}
		if !open {
			return
		}
		ui.Column(c).Gap(6).Padding(6, 10, 8, 10).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			if len(call.Input) > 0 && string(call.Input) != "{}" {
				ui.Text(c, widgets.OneLine(string(call.Input), 2000)).Font(widgets.MonoFont).FontSize(11.5).Selectable()
			}
			for _, ch := range call.Changes {
				ui.Text(c, string(ch.Kind)+" "+ch.Path).FontSize(12).Selectable()
				if ch.Diff != "" {
					ui.Text(c, clip(ch.Diff)).Font(widgets.MonoFont).FontSize(11.5).Selectable()
				}
			}
			if call.Output != "" {
				ui.Text(c, clip(call.Output)).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted).Selectable()
			}
		})
	})
}

func clip(s string) string { return widgets.TextStart(s, maxToolOutput) }

// toolName is a tool call's name as the chat shows it, without the MCP
// server's prefix.
func toolName(call *agent.ToolCall) string {
	name := call.Name
	if call.DisplayName != "" {
		name = call.DisplayName
	}
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if i := strings.Index(rest, "__"); i >= 0 {
			name = rest[i+2:]
		}
	}
	return name
}

// toolSummary is what a tool call acts on, on a line.
func toolSummary(call *agent.ToolCall) string {
	switch {
	case call.Command != "":
		return call.Command
	case call.Path != "":
		return call.Path
	case call.Query != "":
		return call.Query
	case call.URL != "":
		return call.URL
	case len(call.Changes) > 0:
		return call.Changes[0].Path
	}
	return widgets.OneLine(argsSummary(call.Input), 160)
}

// argsSummary is a call's arguments as their values, as `orders · main`
// for {"table":"orders","schema":"main"}, in the order given; its JSON
// as it is where it is not an object of plain values.
func argsSummary(input json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(input))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return string(input)
	}
	var parts []string
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key
			return string(input)
		}
		var v any
		if err := dec.Decode(&v); err != nil {
			return string(input)
		}
		switch v := v.(type) {
		case string:
			if v != "" {
				parts = append(parts, v)
			}
		case float64, bool:
			parts = append(parts, fmt.Sprint(v))
		case []any:
			var items []string
			for _, it := range v {
				items = append(items, fmt.Sprint(it))
			}
			parts = append(parts, strings.Join(items, ", "))
		default:
			return string(input)
		}
	}
	return strings.Join(parts, " · ")
}

func toolIcon(k agent.ToolKind) *ui.SVG {
	switch k {
	case agent.ToolShell:
		return widgets.IconTerminal
	case agent.ToolRead:
		return widgets.IconFile
	case agent.ToolEdit:
		return widgets.IconPencil
	case agent.ToolSearch, agent.ToolWeb:
		return widgets.IconSearch
	case agent.ToolHost, agent.ToolMCP:
		return widgets.IconDatabase
	}
	return widgets.IconCode
}

// codeBlock shows a code block of a reply, with Insert under a SQL one:
// the SQL goes in the editor, in place of its selection, never run.
func (t *Chat) codeBlock(c *ui.Context, b *markdown.CodeBlockView) {
	markdown.DefaultCodeBlock(c, b)
	lang := strings.ToLower(b.Language)
	if b.Open || lang != "sql" && lang != "" && !strings.HasSuffix(lang, "sql") {
		return
	}
	code := strings.TrimRight(b.Code(), "\n")
	ui.Row(c).Gap(4).Padding(4, 0, 0, 0).Children(func() {
		label, tip := "Insert", "Put the SQL in the editor at the caret; nothing runs"
		if t.frameCtx.Selection != "" {
			label, tip = "Replace Selection", "Put the SQL in the editor in place of the selection; nothing runs"
		}
		if smallButton(c, widgets.IconPlus, label).Tooltip(tip).Clicked() {
			t.ed.InsertSQL(code)
		}
		if cn := t.conn(); cn != nil && smallButton(c, widgets.IconFile, "New Editor").Tooltip("A new editor on "+cn.Config.Name+" with this SQL").Clicked() {
			t.a.NewQueryTab(cn, "", code+"\n")
		}
	})
}

// smallButton is a button of a reply, of an icon and a label.
func smallButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	th := c.Theme()
	b := ui.ButtonBase(c.Key("small-"+label)).Padding(3, 8).Radius(6).Border(1, th.Border).Background(th.Background).Label(label)
	if b.Hovered() {
		b.Background(widgets.PaletteOf(c).Hover)
	}
	return b.Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).FontSize(11.5).TextColor(th.Accent)
			ui.Text(c, label).FontSize(12)
		})
	})
}

// requestCard asks the user about a request: to allow a tool or a read,
// or to answer a question.
func (t *Chat) requestCard(c *ui.Context, r *request) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	widgets.Appear(ui.Column(c.Key("request-"+r.id)).Gap(8).Padding(10, 12).Radius(10).Border(1, th.Warning).Background(th.Warning.Alpha(0.08))).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Icon(c, widgets.IconShield).TextColor(th.Warning).FontSize(14)
			ui.Text(c, r.title).Bold().Grow(1).Shrink(1)
		})
		if r.sql != "" {
			// Formatted when asked, highlighted as a reply's SQL is.
			ui.Column(c).FillWidth().Children(func() {
				markdown.View(c, "````sql\n"+r.sql+"\n````", &markdown.Options{WrapCode: true}).FillWidth()
			})
		}
		if r.detail != "" {
			ui.Text(c, clip(r.detail)).FontSize(12).TextColor(pal.Muted).Selectable()
		}
		if q := r.question; q != nil {
			for _, question := range q.Questions {
				ui.Text(c, cmp.Or(question.Text, question.Header)).Selectable()
				ui.Row(c).Gap(6).Wrap().Children(func() {
					for _, o := range question.Options {
						if ui.Button(c, o.Label).Tooltip(o.Description).Clicked() {
							t.answerQuestion(r, question, o.Label)
						}
					}
				})
			}
			if ui.Button(c, "Skip").Clicked() {
				t.answer(r, deny, "")
			}
			return
		}
		ui.Row(c).Gap(6).Wrap().Children(func() {
			if r.connect {
				if ui.PrimaryButton(c, "Connect").Clicked() {
					t.answer(r, allow, "")
				}
				if ui.Button(c, "Not Now").Clicked() {
					t.answer(r, deny, "")
				}
				return
			}
			if ui.PrimaryButton(c, "Allow").Clicked() {
				t.answer(r, allow, "")
			}
			if r.always && ui.Button(c, "Allow for This Chat").Tooltip("Allow its reads of "+r.connName+" without asking again, until the chat starts over").Clicked() {
				t.answer(r, allowAlways, "")
			}
			if ui.Button(c, "Deny").Clicked() {
				t.answer(r, deny, "")
			}
		})
	})
}

// inputView is where the user writes, in a ring that glows with the
// focus and flows while the agent works: what the message will carry of
// the editor above the field, the agent, model and effort under it. ↵
// sends, ⇧↵ starts a new line.
func (t *Chat) inputView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ring := ui.Column(c.Key("composer")).Padding(ringPadding).Draw(t.fx.drawRing())
	ring.Children(func() {
		box := ui.Column(c).Radius(fieldRadius).Border(1, th.Border).Background(th.Background).Padding(6, 6, 5, 6).Gap(2).
			Shadow(0, 2, 10, 0, ui.RGBA(0, 0, 0, 0.06))
		box.Children(func() {
			if summary := t.contextSummary(); summary != "" {
				ui.Row(c).Gap(5).Padding(1, 4).AlignItems(ui.Center).Tooltip("What the next message carries of the editor").Children(func() {
					ui.Icon(c, widgets.IconCode).FontSize(11).TextColor(th.Accent)
					ui.Text(c, summary).FontSize(11.5).TextColor(pal.Muted).SingleLine().Shrink(1)
				})
			}
			area := ui.TextArea(c.Key(fmt.Sprintf("input-%d", t.inputKey)), &t.input).Label("Message").
				Placeholder("Ask about the query, or for SQL").MinHeight(44).MaxHeight(220).FillWidth().
				BorderWidth(0).Background(th.Background)
			area.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEnter && ev.Mods == 0 {
					t.sendInput()
					return true
				}
				return false
			})
			if t.wantFocus && area.Focus().Focused() {
				t.wantFocus = false
			}
			ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
				t.pickers(c)
				ui.Spacer(c)
				t.sendButton(c)
			})
		})
		t.fieldFocused = box.FocusWithin()
		if t.fieldFocused {
			box.Border(1, th.Accent.Alpha(0.55))
		}
	})
}

// sendButton sends what was typed, round, in the accent once there is
// something to send; while the agent works, it stops it.
func (t *Chat) sendButton(c *ui.Context) {
	th := c.Theme()
	ready := strings.TrimSpace(t.input) != ""
	label, tip := "Send", "Send (↵); ⇧↵ for a new line"
	if t.running {
		label, tip = "Stop", keymap.Hint("Stop the reply", keymap.ChatStop)
	}
	b := ui.ButtonBase(c.Key("send")).Size(28, 28).Radius(14).Label(label).Tooltip(tip).Center().Disabled(!t.running && !ready)
	fill, ink := th.Border.Alpha(0.6), widgets.PaletteOf(c).Muted
	if t.running || ready {
		fill, ink = th.Accent, ui.RGBA(255, 255, 255, 1)
	}
	b.Background(fill).Transition(ui.ElementTransition{Colors: true, Duration: 150 * time.Millisecond})
	scale := b.Animate("press", map[bool]float32{true: 0.92, false: 1}[b.Pressed()], 90*time.Millisecond)
	if b.Hovered() && !b.Pressed() {
		scale = b.Animate("hover", 1.06, 120*time.Millisecond)
	}
	b.Scale(scale, scale)
	b.Children(func() {
		icon := widgets.IconArrowUp
		if t.running {
			icon = widgets.IconStop
		}
		ui.Icon(c, icon).FontSize(13).TextColor(ink)
	})
	if b.Clicked() {
		if t.running {
			t.Stop()
		} else {
			t.sendInput()
		}
	}
}

// sendInput sends what was typed, and empties the field.
func (t *Chat) sendInput() {
	text := t.input
	if strings.TrimSpace(text) == "" {
		return
	}
	t.input = ""
	t.Send(text)
}
