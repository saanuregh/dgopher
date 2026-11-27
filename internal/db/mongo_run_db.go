package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/mongosh"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// databaseCommands are the database methods that send one command and
// take no arguments: the command, and whether it runs on admin.
var databaseCommands = map[string]struct {
	command string
	admin   bool
}{
	"hello":              {"hello", false},
	"isMaster":           {"isMaster", false},
	"serverBuildInfo":    {"buildInfo", false},
	"serverCmdLineOpts":  {"getCmdLineOpts", true},
	"hostInfo":           {"hostInfo", true},
	"listCommands":       {"listCommands", false},
	"dropDatabase":       {"dropDatabase", false},
	"getProfilingStatus": {"profile", false},
}

func planDatabase(database string, op mongosh.Op) (docPlan, error) {
	m, args := op.Method, op.Args
	if m == "aggregate" {
		cmd, err := aggregateCommand(int32(1), m, args)
		if err != nil {
			return docPlan{}, err
		}
		return cursorChain(op, docPlan{command: cmd, cursor: true, write: mongosh.Classify(op).Writes, explainable: true})
	}
	p, err := databaseCommand(database, op)
	if err != nil {
		return docPlan{}, err
	}
	if len(op.Chain) > 0 {
		return docPlan{}, fmt.Errorf("%s does not return a cursor: .%s() cannot follow it", m, op.Chain[0].Name)
	}
	return p, nil
}

func databaseCommand(database string, op mongosh.Op) (docPlan, error) {
	m, args := op.Method, op.Args
	if c, ok := databaseCommands[m]; ok {
		var v any = int32(1)
		if m == "getProfilingStatus" {
			v = int32(-1)
		}
		return docPlan{command: bson.D{{Key: c.command, Value: v}}, admin: c.admin, write: m == "dropDatabase"}, nil
	}
	// withOptions is cmd and the options args[i] gives, any the server
	// takes.
	withOptions := func(cmd bson.D, i int) (bson.D, error) {
		o, err := docOptions(m, docArg(args, i), nil)
		return append(cmd, o...), err
	}
	named := func(i int, what string) (string, error) {
		if s, ok := docArg(args, i).(string); ok && s != "" {
			return s, nil
		}
		return "", fmt.Errorf("%s expects %s", m, what)
	}
	switch m {
	case "runCommand", "adminCommand":
		if len(args) != 1 {
			return docPlan{}, fmt.Errorf("%s takes one command document", m)
		}
		var cmd bson.D
		switch v := args[0].(type) {
		case string:
			cmd = bson.D{{Key: v, Value: int32(1)}}
		case bson.D:
			cmd = v
		}
		if len(cmd) == 0 || cmd[0].Key == "" {
			return docPlan{}, fmt.Errorf("%s expects a command document, as {ping: 1}", m)
		}
		return docPlan{command: cmd, admin: m == "adminCommand", write: true}, nil
	case "getCollectionNames":
		return docPlan{do: func(ctx context.Context, d *Doc, db *mongo.Database) (DocResult, error) {
			names, err := db.ListCollectionNames(ctx, bson.D{})
			if err != nil {
				return DocResult{Affected: -1}, err
			}
			slices.Sort(names)
			return replyOf(bson.D{{Key: m, Value: names}})
		}}, nil
	case "getCollectionInfos":
		cmd := bson.D{{Key: "listCollections", Value: int32(1)}}
		if err := docFilterInto(&cmd, "filter", m, args, 0); err != nil {
			return docPlan{}, err
		}
		o, err := docOptions(m, docArg(args, 1), nameSet("nameOnly", "authorizedCollections"))
		return docPlan{command: append(cmd, o...), cursor: true}, err
	case "createCollection":
		name, err := named(0, "the new collection's name")
		if err != nil {
			return docPlan{}, err
		}
		cmd, err := withOptions(bson.D{{Key: "create", Value: name}}, 1)
		return docPlan{command: cmd, write: true}, err
	case "createView":
		name, err := named(0, "the view's name")
		if err != nil {
			return docPlan{}, err
		}
		source, err := named(1, "the collection the view reads")
		if err != nil {
			return docPlan{}, err
		}
		pipeline, ok := docArg(args, 2).(bson.A)
		if !ok {
			return docPlan{}, errors.New("createView expects a pipeline: an array of stages")
		}
		cmd, err := withOptions(bson.D{{Key: "create", Value: name}, {Key: "viewOn", Value: source}, {Key: "pipeline", Value: pipeline}}, 3)
		return docPlan{command: cmd, write: true}, err
	case "stats":
		cmd := bson.D{{Key: "dbStats", Value: int32(1)}}
		if n, ok := docInteger(docArg(args, 0)); ok {
			return docPlan{command: append(cmd, bson.E{Key: "scale", Value: n})}, nil
		}
		cmd, err := withOptions(cmd, 0)
		return docPlan{command: cmd}, err
	case "serverStatus":
		cmd, err := withOptions(bson.D{{Key: "serverStatus", Value: int32(1)}}, 0)
		return docPlan{command: cmd, admin: true}, err
	case "currentOp":
		cmd := bson.D{{Key: "currentOp", Value: int32(1)}}
		if docArg(args, 0) == true {
			return docPlan{command: append(cmd, bson.E{Key: "$all", Value: true}), admin: true}, nil
		}
		cmd, err := withOptions(cmd, 0)
		return docPlan{command: cmd, admin: true}, err
	case "killOp":
		id := docArg(args, 0)
		if id == nil {
			return docPlan{}, errors.New("killOp expects the operation's opid")
		}
		return docPlan{command: bson.D{{Key: "killOp", Value: int32(1)}, {Key: "op", Value: id}}, admin: true, write: true}, nil
	case "setProfilingLevel":
		level, ok := docInteger(docArg(args, 0))
		if !ok {
			return docPlan{}, errors.New("setProfilingLevel expects a level: 0, 1 or 2")
		}
		cmd := bson.D{{Key: "profile", Value: level}}
		if ms, ok := docInteger(docArg(args, 1)); ok {
			return docPlan{command: append(cmd, bson.E{Key: "slowms", Value: ms}), write: true}, nil
		}
		cmd, err := withOptions(cmd, 1)
		return docPlan{command: cmd, write: true}, err
	case "getUsers", "getRoles":
		key := map[string]string{"getUsers": "usersInfo", "getRoles": "rolesInfo"}[m]
		cmd, err := withOptions(bson.D{{Key: key, Value: int32(1)}}, 0)
		return docPlan{command: cmd}, err
	case "getUser", "getRole":
		key := map[string]string{"getUser": "usersInfo", "getRole": "rolesInfo"}[m]
		name, err := named(0, "a name")
		if err != nil {
			return docPlan{}, err
		}
		cmd, err := withOptions(bson.D{{Key: key, Value: name}}, 1)
		return docPlan{command: cmd}, err
	case "getLogComponents":
		return docPlan{command: bson.D{{Key: "getParameter", Value: int32(1)}, {Key: "logComponentVerbosity", Value: int32(1)}}, admin: true,
			finish: valueReply(m, "logComponentVerbosity")}, nil
	case "commandHelp":
		name, err := named(0, "a command's name")
		if err != nil {
			return docPlan{}, err
		}
		return docPlan{command: bson.D{{Key: name, Value: int32(1)}, {Key: "help", Value: true}}}, nil
	case "getName":
		return docPlan{do: func(context.Context, *Doc, *mongo.Database) (DocResult, error) {
			return replyOf(bson.D{{Key: m, Value: database}})
		}}, nil
	case "version":
		return docPlan{do: func(ctx context.Context, d *Doc, _ *mongo.Database) (DocResult, error) {
			info, err := d.readBuildInfo(ctx)
			if err != nil {
				return DocResult{Affected: -1}, err
			}
			return replyOf(bson.D{{Key: m, Value: info.Lookup("version")}})
		}}, nil
	}
	return docPlan{}, fmt.Errorf("db.%s() is not supported here; run its command with db.runCommand()", m)
}

func planShow(op mongosh.Op) (docPlan, error) {
	topic, _ := docArg(op.Args, 0).(string)
	switch topic {
	case "dbs":
		return docPlan{command: bson.D{{Key: "listDatabases", Value: int32(1)}}, admin: true, finish: arrayAsCursor("databases")}, nil
	case "collections":
		return docPlan{do: func(ctx context.Context, d *Doc, db *mongo.Database) (DocResult, error) {
			colls, err := d.Collections(ctx, db.Name())
			if err != nil {
				return DocResult{Affected: -1}, err
			}
			docs := make([]any, len(colls))
			for i, c := range colls {
				docs[i] = bson.D{{Key: "name", Value: c.Name}, {Key: "type", Value: c.Type}}
			}
			return cursorOf(docs)
		}}, nil
	case "users":
		return docPlan{command: bson.D{{Key: "usersInfo", Value: int32(1)}}, finish: arrayAsCursor("users")}, nil
	case "roles":
		return docPlan{command: bson.D{{Key: "rolesInfo", Value: int32(1)}, {Key: "showBuiltinRoles", Value: true}}, finish: arrayAsCursor("roles")}, nil
	case "profile":
		// As mongosh: the five latest operations that took a millisecond
		// or more.
		return docPlan{command: bson.D{{Key: "find", Value: "system.profile"}, {Key: "filter", Value: bson.D{{Key: "millis", Value: bson.D{{Key: "$gt", Value: int32(0)}}}}},
			{Key: "sort", Value: bson.D{{Key: "$natural", Value: int32(-1)}}}, {Key: "limit", Value: int32(5)}}, cursor: true}, nil
	}
	return docPlan{}, fmt.Errorf("show %s is not supported", topic)
}

// planUse replies the database use names; the caller keeps it.
func planUse(op mongosh.Op) (docPlan, error) {
	name := op.Database
	if err := checkDatabaseName(name); err != nil {
		return docPlan{}, err
	}
	return docPlan{do: func(context.Context, *Doc, *mongo.Database) (DocResult, error) {
		return replyOf(bson.D{{Key: "database", Value: name}})
	}}, nil
}

// checkDatabaseName refuses a name the server would: empty, or holding a
// character MongoDB does not allow in one.
func checkDatabaseName(name string) error {
	if name == "" {
		return errors.New("a database name is required")
	}
	if i := strings.IndexAny(name, "/\\. \"$\x00"); i >= 0 {
		return fmt.Errorf("the database name %q cannot hold %q", name, name[i:i+1])
	}
	return nil
}
