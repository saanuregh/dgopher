package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// pubSubKeep is how many messages the panel keeps, the latest.
const pubSubKeep = 5000

type pubSubState struct {
	listenIn  string             // channels and patterns as typed, by spaces
	listening string             // what the subscription listens to
	cancel    context.CancelFunc // set while it listens
	messages  []db.PubSubMessage
	err       string
	list      ui.ListState

	channel string // the publish form's
	message string

	channels      []db.ChannelInfo // those clients listen to, as last read
	channelsAsked bool
	channelList   ui.ListState
}

// listen subscribes to the channels and patterns typed.
func (r *Tab) listen() {
	p := &r.pubsub
	var channels, patterns []string
	for _, name := range strings.Fields(p.listenIn) {
		if db.IsPattern(name) {
			patterns = append(patterns, name)
		} else {
			channels = append(channels, name)
		}
	}
	if len(channels)+len(patterns) == 0 || p.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.err, p.listening = cancel, "", strings.Join(strings.Fields(p.listenIn), " ")
	kv, cfg := r.conn.KV, r.conn.Config
	messages := feed[db.PubSubMessage]{keep: pubSubKeep}
	go func() {
		defer cancel()
		done := make(chan struct{})
		go messages.run(done, func(batch []db.PubSubMessage) {
			r.a.Post(func() { p.messages = keepLatest(p.messages, batch, pubSubKeep) })
		})
		start := time.Now()
		err := kv.Subscribe(ctx, channels, patterns, messages.add)
		close(done)
		r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, "SUBSCRIBE "+strings.Join(append(channels, patterns...), " "), -1, time.Since(start), err)
		r.a.Post(func() {
			p.cancel = nil
			if err != nil {
				p.err = err.Error()
			}
		})
	}()
}

// publish sends the message typed to the channel typed, as the policy
// says.
func (r *Tab) publish() {
	p := &r.pubsub
	channel := strings.TrimSpace(p.channel)
	if channel == "" {
		return
	}
	r.write([]string{"PUBLISH", channel, p.message}, func() {
		p.message = ""
		r.loadChannels()
	})
}

// loadChannels reads the channels clients listen to.
func (r *Tab) loadChannels() {
	p := &r.pubsub
	p.channelsAsked = true
	kv := r.conn.KV
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		channels, err := kv.Channels(ctx, "*")
		return func() {
			if err != nil {
				p.err = err.Error()
				return
			}
			p.channels = channels
		}
	})
}

func (r *Tab) pubSubActions(c *ui.Context) {
	p := &r.pubsub
	pal := widgets.PaletteOf(c)
	if p.cancel != nil {
		ui.Spinner(c).Size(12, 12)
		ui.Text(c, fmt.Sprintf("%d messages", len(p.messages))).FontSize(12).TextColor(pal.Muted)
	}
	if ui.Link(c, "Clear", "").FontSize(12).Clicked() {
		p.messages = nil
	}
}

func (r *Tab) pubSubView(c *ui.Context) {
	p := &r.pubsub
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if !p.channelsAsked {
		r.loadChannels()
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).Children(func() {
			ui.Row(c).Padding(6, 10).Gap(6).AlignItems(ui.Center).Children(func() {
				if p.cancel != nil {
					ui.Text(c, "Listening to "+p.listening).FontSize(12.5).SingleLine().Grow(1).Shrink(1)
					if ui.Button(c, "Stop").Clicked() {
						p.cancel()
					}
					return
				}
				in := ui.TextInput(c, &p.listenIn).Placeholder("Channels or patterns, e.g. news orders.*").Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label("Channels to listen to")
				if ui.Button(c, "Listen").Disabled(strings.TrimSpace(p.listenIn) == "").Clicked() || in.Submitted() {
					r.listen()
				}
			})
			if p.err != "" {
				ui.Text(c, p.err).TextColor(th.Danger).Padding(4, 12).Selectable()
			}
			p.list.FollowEnd = true
			ui.List(c, &p.list, len(p.messages), func(i int) {
				m := p.messages[i]
				ui.Row(c).Padding(1, 12).Gap(10).Children(func() {
					ui.Text(c, m.At.Format("15:04:05.000")).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted)
					channel := m.Channel
					if m.Pattern != "" {
						channel += " (" + m.Pattern + ")"
					}
					ui.Text(c, channel).Font(widgets.MonoFont).FontSize(12).TextColor(th.Accent).Width(200).SingleLine()
					ui.Text(c, widgets.OneLine(m.Message, 500)).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1).Selectable()
				})
			}).Grow(1).Label("Messages")
			if r.conn.Config.ReadOnly {
				return
			}
			ui.Row(c).Padding(6, 10).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
				ui.TextInput(c, &p.channel).Placeholder("Channel").Font(widgets.MonoFont).FontSize(12.5).Width(180).Label("Channel to publish to")
				in := ui.TextInput(c, &p.message).Placeholder("Message").Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label("Message to publish")
				if ui.Button(c, "Publish").Disabled(strings.TrimSpace(p.channel) == "").Clicked() || in.Submitted() {
					r.publish()
				}
			})
		})
		ui.Column(c).Width(240).BorderWidth(0, 0, 0, 1).BorderColor(th.Border).Children(func() {
			ui.Row(c).Padding(6, 10).Gap(6).Children(func() {
				ui.Text(c, "Channels listened to").FontSize(12).Bold().Grow(1)
				if ui.Link(c, "Refresh", "").FontSize(12).Clicked() {
					r.loadChannels()
				}
			})
			ui.List(c, &p.channelList, len(p.channels), func(i int) {
				ch := p.channels[i]
				row := ui.ButtonBase(c).Padding(2, 10).Label(ch.Name)
				row.Children(func() {
					ui.Row(c).Gap(6).FillWidth().Children(func() {
						ui.Text(c, ch.Name).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1)
						ui.Text(c, fmt.Sprint(ch.Subscribers)).FontSize(11.5).TextColor(pal.Muted)
					})
				})
				if row.Clicked() {
					p.channel = ch.Name
				}
			}).Grow(1).Label("Channels listened to")
		})
	})
}
