package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// What a stream shows.
const (
	streamEntries = iota
	streamGroups
)

// pendingRead is how many pending entries of a group are read.
const pendingRead = 500

// streamState is a stream's consumer groups, with the chosen group's
// consumers and pending entries.
type streamState struct {
	panel     int
	loading   bool
	loads     int // counts the loads: a slower one's result is dropped
	err       string
	groups    []db.StreamGroup
	group     int // the chosen group's row
	consumers []db.StreamConsumer
	consumer  int
	pending   []db.PendingEntry
	entry     int
	groupList ui.ListState
	consList  ui.ListState
	pendList  ui.ListState

	newGroup string // the new group's name
	fromAll  bool   // the new group reads the entries there already too
	claimTo  string // the consumer a pending entry is claimed for
}

// chosenGroup is the name of the group chosen, "" for none.
func (s *streamState) chosenGroup() string {
	if s.group >= 0 && s.group < len(s.groups) {
		return s.groups[s.group].Name
	}
	return ""
}

// loadGroups reads the stream's groups, and the chosen one's consumers
// and pending entries.
func (r *Tab) loadGroups() {
	s := &r.stream
	s.loading, s.err = true, ""
	s.loads++
	kv, key, chosen, load := r.conn.KV, r.selected, s.chosenGroup(), s.loads
	gen := r.valueGen
	dataview.BackgroundResetOnPanic(r.a, func() {
		if gen == r.valueGen && load == s.loads {
			s.loading = false
		}
	}, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		groups, err := kv.StreamGroups(ctx, key)
		var consumers []db.StreamConsumer
		var pending []db.PendingEntry
		found := false
		for _, g := range groups {
			found = found || g.Name == chosen
		}
		if err == nil && found {
			consumers, err = kv.StreamConsumers(ctx, key, chosen)
		}
		if err == nil && found {
			pending, err = kv.PendingEntries(ctx, key, chosen, pendingRead)
		}
		return func() {
			if gen != r.valueGen || load != s.loads {
				return
			}
			s.loading = false
			if err != nil {
				s.err = err.Error()
				return
			}
			s.groups, s.consumers, s.pending = groups, consumers, pending
			s.group, s.consumer, s.entry = -1, -1, -1
			for i, g := range groups {
				if g.Name == chosen {
					s.group = i
				}
			}
		}
	})
}

// groupWrite runs a command on the stream's groups, then reads them again.
func (r *Tab) groupWrite(args ...string) {
	r.write(args, r.loadGroups)
}

func (r *Tab) groupsView(c *ui.Context) {
	s := &r.stream
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	key, ro := r.selected, r.conn.Config.ReadOnly
	ui.Column(c).Grow(1).Gap(6).Padding(0, 14, 8, 14).Children(func() {
		if s.err != "" {
			ui.Text(c, s.err).TextColor(th.Danger).Selectable()
		}
		s.groupList.Selected = &s.group
		cols := []ui.TableColumn{{Title: "Group"}, {Title: "Consumers", Width: 90, Align: ui.End}, {Title: "Pending", Width: 80, Align: ui.End},
			{Title: "Last delivered", Width: 160}, {Title: "Lag", Width: 70, Align: ui.End}}
		t := ui.Table(c, &s.groupList, cols, len(s.groups), func(row, col int) {
			g := s.groups[row]
			text := g.Name
			switch col {
			case 1:
				text = fmt.Sprint(g.Consumers)
			case 2:
				text = fmt.Sprint(g.Pending)
			case 3:
				text = g.LastDelivered
			case 4:
				text = ""
				if g.Lag >= 0 {
					text = fmt.Sprint(g.Lag)
				}
			}
			ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
		}).Height(140).Label("Consumer groups")
		if t.Changed() {
			// The rows below are the last group's until the new one's come.
			s.consumers, s.pending, s.consumer, s.entry = nil, nil, -1, -1
			r.loadGroups()
		}
		// Actions wait for the rows they act on.
		ro := ro || s.loading
		if !ro {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &s.newGroup).Placeholder("New group").Font(widgets.MonoFont).Width(180).Label("New group's name")
				ui.Checkbox(c, &s.fromAll, "Reads the entries there already")
				if ui.Button(c, "Create Group").Disabled(strings.TrimSpace(s.newGroup) == "").Clicked() {
					from := "$"
					if s.fromAll {
						from = "0"
					}
					r.groupWrite("XGROUP", "CREATE", key, strings.TrimSpace(s.newGroup), from)
					s.newGroup = ""
				}
				ui.Spacer(c)
				if g := s.chosenGroup(); g != "" && ui.Button(c, "Destroy Group").Clicked() {
					r.a.AskDiscard("Destroy the group "+g+"?", "Its consumers and the entries pending with them go; the stream's entries stay.", func() {
						r.groupWrite("XGROUP", "DESTROY", key, g)
					})
				}
			})
		}
		group := s.chosenGroup()
		if group == "" {
			ui.Text(c, "Choose a group to see its consumers and the entries pending with them.").FontSize(12.5).TextColor(pal.Muted)
			return
		}
		ui.Row(c).Grow(1).Gap(12).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(300).Gap(4).Children(func() {
				ui.Text(c, "Consumers of "+group).FontSize(12).Bold()
				s.consList.Selected = &s.consumer
				cols := []ui.TableColumn{{Title: "Consumer"}, {Title: "Pending", Width: 70, Align: ui.End}, {Title: "Idle", Width: 80, Align: ui.End}}
				ui.Table(c, &s.consList, cols, len(s.consumers), func(row, col int) {
					cs := s.consumers[row]
					text := cs.Name
					switch col {
					case 1:
						text = fmt.Sprint(cs.Pending)
					case 2:
						text = cs.Idle.Round(time.Second).String()
					}
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
				}).Grow(1).Label("Consumers")
				if !ro && s.consumer >= 0 && s.consumer < len(s.consumers) && ui.Button(c, "Delete Consumer").Clicked() {
					name := s.consumers[s.consumer].Name
					r.a.AskDiscard("Delete the consumer "+name+"?", "The entries pending with it are no longer pending with any.", func() {
						r.groupWrite("XGROUP", "DELCONSUMER", key, group, name)
					})
				}
			})
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, "Pending entries").FontSize(12).Bold()
				s.pendList.Selected = &s.entry
				cols := []ui.TableColumn{{Title: "ID", Width: 170}, {Title: "Consumer"}, {Title: "Idle", Width: 80, Align: ui.End}, {Title: "Deliveries", Width: 80, Align: ui.End}}
				ui.Table(c, &s.pendList, cols, len(s.pending), func(row, col int) {
					p := s.pending[row]
					text := p.ID
					switch col {
					case 1:
						text = p.Consumer
					case 2:
						text = p.Idle.Round(time.Second).String()
					case 3:
						text = fmt.Sprint(p.Deliveries)
					}
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
				}).Grow(1).Label("Pending entries")
				if ro || s.entry < 0 || s.entry >= len(s.pending) {
					return
				}
				id := s.pending[s.entry].ID
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if ui.Button(c, "Acknowledge").Clicked() {
						r.groupWrite("XACK", key, group, id)
					}
					ui.TextInput(c, &s.claimTo).Placeholder("Consumer").Font(widgets.MonoFont).Width(150).Label("Consumer to claim for")
					if ui.Button(c, "Claim").Disabled(strings.TrimSpace(s.claimTo) == "").Clicked() {
						r.groupWrite("XCLAIM", key, group, strings.TrimSpace(s.claimTo), "0", id)
					}
				})
			})
		})
	})
}
