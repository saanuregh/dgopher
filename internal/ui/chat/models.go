package chat

import (
	"context"
	"slices"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"

	"dgopher/internal/redact"
	"dgopher/internal/ui/dataview"
)

// modelList is the models an agent offers, as its command line lists
// them.
type modelList struct {
	loading bool
	models  []agent.Model
	err     string
}

// modelLists are each agent's models, read once, by the agent's name;
// read and written on the UI thread.
var modelLists = map[string]*modelList{}

// listed are the agent's models as read so far, without reading them:
// listing starts the agent's command line.
func (t *Chat) listed() *modelList {
	if l := modelLists[t.provider]; l != nil {
		return l
	}
	return &modelList{}
}

// models are the agent's models, read when first asked, as the model
// menu opens or the session starts: loading until they come.
func (t *Chat) models() *modelList {
	if l := modelLists[t.provider]; l != nil {
		return l
	}
	l := &modelList{loading: true}
	modelLists[t.provider] = l
	name := t.provider
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowError, func() { l.loading = false })
		// Made here, not on the UI thread: Codex's asks its command line
		// which features it has.
		lister, ok := NewProvider(name).(agent.ModelLister)
		if !ok {
			t.a.Post(func() { l.loading = false })
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		models, err := lister.ListModels(ctx)
		t.a.Post(func() {
			l.loading, l.models = false, models
			if err != nil {
				l.err = redact.Secrets(err.Error())
			}
		})
	}()
	return l
}

// forgetModels has the agent's models read again.
func forgetModels(provider string) { delete(modelLists, provider) }

// modelOf is the model of an ID among those listed, the default's for
// "", nil for none.
func (l *modelList) modelOf(id string) *agent.Model {
	for i, m := range l.models {
		if id == "" && m.IsDefault || id != "" && m.ID == id {
			return &l.models[i]
		}
	}
	return nil
}

// efforts are the efforts the chosen model takes, none when unknown.
func (t *Chat) efforts() []agent.Effort {
	if m := t.listed().modelOf(t.modelID); m != nil {
		return m.Efforts
	}
	return nil
}

// modelLabel is the model the chat uses, as its header shows it.
func (t *Chat) modelLabel() string {
	if t.modelID == "" {
		if t.model != "" {
			return t.model
		}
		return "Default model"
	}
	if m := t.listed().modelOf(t.modelID); m != nil && m.DisplayName != "" {
		return m.DisplayName
	}
	return t.modelID
}

// chooseModel has the chat use a model, "" for the agent's default, and
// an effort, from the next request on; the agent keeps both.
func (t *Chat) chooseModel(id string, effort agent.Effort) {
	if id == t.modelID && effort == t.effort {
		return
	}
	if effort != "" && t.modelID != id {
		if m := t.listed().modelOf(id); m != nil && !slices.Contains(m.Efforts, effort) {
			effort = "" // the model does not take it
		}
	}
	modelChanged, effortChanged := id != t.modelID, effort != t.effort
	t.modelID, t.effort = id, effort
	s := t.a.Settings()
	s.ChatModels = setOrDelete(s.ChatModels, t.provider, id)
	s.ChatEfforts = setOrDelete(s.ChatEfforts, t.provider, string(effort))
	t.a.SaveSettings()
	sess := t.sess
	if sess == nil {
		return // the session starts with them
	}
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowError, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		if modelChanged {
			err = sess.SetModel(ctx, id)
		}
		if err == nil && effortChanged {
			err = sess.SetEffort(ctx, effort)
		}
		if err != nil {
			t.a.Post(func() {
				t.notices = append(t.notices, notice{text: "Not switched: " + redact.Secrets(err.Error()) + ". It takes effect when the chat starts over.", err: true})
			})
		}
	}()
}

func setOrDelete(m map[string]string, key, value string) map[string]string {
	if value == "" {
		delete(m, key)
		return m
	}
	if m == nil {
		m = map[string]string{}
	}
	m[key] = value
	return m
}
