package db

import (
	"context"
	"slices"
	"strings"

	"github.com/redis/rueidis"
)

// CommandDoc is the help of a Redis command, as its server documents it.
type CommandDoc struct {
	Name    string // upper case, with its container: "CONFIG GET"
	Summary string
	Since   string
	Group   string
	// Syntax is its arguments as redis-cli writes them: optional ones in
	// brackets, choices split by |, repeated ones followed by ….
	Syntax string
}

// CommandDocs reads the help of every command the server has, by name,
// once; nil, without an error, from a server without COMMAND DOCS (Redis
// before 7).
func (k *KV) CommandDocs(ctx context.Context) (map[string]CommandDoc, error) {
	k.docsMu.Lock()
	defer k.docsMu.Unlock()
	if k.docs != nil {
		return k.docs, nil
	}
	msg, err := k.Client.Do(ctx, k.Client.B().CommandDocs().Build()).ToMessage()
	if _, refused := rueidis.IsRedisErr(err); refused {
		return nil, nil // an older server, or COMMAND refused
	}
	if err != nil {
		return nil, err
	}
	docs := map[string]CommandDoc{}
	for name, doc := range asMap(msg) {
		addDoc(docs, name, doc)
	}
	k.docs = docs
	return docs, nil
}

// addDoc adds a command's help, and its subcommands'.
func addDoc(docs map[string]CommandDoc, name string, msg rueidis.RedisMessage) {
	fields := asMap(msg)
	text := func(key string) string {
		m, ok := fields[key]
		if !ok {
			return ""
		}
		s, _ := m.ToString()
		return s
	}
	d := CommandDoc{Name: strings.ToUpper(strings.ReplaceAll(name, "|", " ")), Summary: text("summary"), Since: text("since"), Group: text("group")}
	if args, ok := fields["arguments"]; ok {
		list, _ := args.ToArray()
		d.Syntax = argsSyntax(list, " ")
	}
	docs[d.Name] = d
	if subs, ok := fields["subcommands"]; ok {
		for sub, doc := range asMap(subs) {
			addDoc(docs, sub, doc)
		}
	}
}

// argsSyntax writes arguments as redis-cli does, joined by sep.
func argsSyntax(args []rueidis.RedisMessage, sep string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		fields := asMap(a)
		text := func(key string) string {
			m, ok := fields[key]
			if !ok {
				return ""
			}
			s, _ := m.ToString()
			return s
		}
		var flags []string
		if f, ok := fields["flags"]; ok {
			flags, _ = f.AsStrSlice()
		}
		var s string
		switch typ := text("type"); typ {
		case "pure-token":
			s = text("token")
		case "block", "oneof":
			var nested []rueidis.RedisMessage
			if m, ok := fields["arguments"]; ok {
				nested, _ = m.ToArray()
			}
			inner := " "
			if typ == "oneof" {
				inner = " | "
			}
			s = argsSyntax(nested, inner)
			if text("token") != "" {
				s = text("token") + " " + s
			}
		default:
			s = text("display_text")
			if s == "" {
				s = text("name")
			}
			if text("token") != "" {
				s = text("token") + " " + s
			}
		}
		if slices.Contains(flags, "multiple") {
			repeat := s
			if slices.Contains(flags, "multiple_token") && text("token") != "" {
				repeat = text("token") + " " + strings.TrimPrefix(s, text("token")+" ")
			}
			s += " [" + repeat + " ...]"
		}
		if slices.Contains(flags, "optional") {
			s = "[" + s + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, sep)
}

// asMap reads a reply of names and values: a map in RESP3, an array of
// pairs in RESP2.
func asMap(m rueidis.RedisMessage) map[string]rueidis.RedisMessage {
	if out, err := m.AsMap(); err == nil {
		return out
	}
	list, _ := m.ToArray()
	out := make(map[string]rueidis.RedisMessage, len(list)/2)
	for i := 0; i+1 < len(list); i += 2 {
		if k, err := list[i].ToString(); err == nil {
			out[k] = list[i+1]
		}
	}
	return out
}

// DocFor finds the help of the command a line of the console starts with:
// a container's subcommand where the line names one.
func DocFor(docs map[string]CommandDoc, args []string) (CommandDoc, bool) {
	if len(args) == 0 {
		return CommandDoc{}, false
	}
	name := strings.ToUpper(args[0])
	if len(args) > 1 {
		if d, ok := docs[name+" "+strings.ToUpper(args[1])]; ok {
			return d, true
		}
	}
	d, ok := docs[name]
	return d, ok
}

// CompleteCommand lists the commands, not subcommands, whose names start
// with prefix, in order.
func CompleteCommand(docs map[string]CommandDoc, prefix string) []string {
	prefix = strings.ToUpper(prefix)
	var out []string
	for name := range docs {
		if !strings.Contains(name, " ") && strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
