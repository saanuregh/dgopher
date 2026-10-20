package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// sendMethods are the methods that send a statement or command to a server.
var sendMethods = []string{"Exec", "ExecContext", "ExecTransactionControl", "Query", "QueryContext", "QueryRowContext",
	"Do", "DoMulti", "Apply", "ApplyEdits", "CopyFrom"}

// recordCalls are the calls that audit what was sent. RecordBlocked is not
// one: it says a statement was refused, which is no record of one sent.
var recordCalls = []string{"Record", "RecordRun", "RecordTyped", "audited"}

// notAudited are the functions that send without recording, by
// "path:function" ("Type.method" for a method; the path from internal/), each
// with why that is not a hole in the audit log. An entry whose function no
// longer sends is an error too, so the list stays true.
var notAudited = map[string]string{
	"app/activity.go:activityTab.refresh":  "lists the server's running statements: server activity, no user rows",
	"app/activity.go:redisActivity":        "reads INFO and CLIENT LIST: server activity, no user rows",
	"app/app.go:App.StartJob":              "sync.Once.Do, not a server call",
	"app/sample.go:makeSample":             "fills a new local SQLite file of sample data, which no server holds",
	"ui/chat/chat.go:Chat.Resume":          "agent.Transcript.Apply folds an earlier conversation's items, read from the agent's store, into its transcript: no server call",
	"ui/chat/chat.go:Chat.apply":           "agent.Transcript.Apply folds a chat's event into its transcript: no server call",
	"ui/chat/tools.go:fetch":               "its caller, read, records the AI chat's read",
	"ui/dashboard/run.go:run":              "its caller, audited in tab.go, records the run, with the typed text and parameters",
	"ui/dataview/exportui.go:exportRows":   "reads an export's rows; exportOne, which calls it, records src.SQL as the export's entry",
	"ui/query/query.go:mysqlTransactional": "probes whether a MySQL table's engine keeps changes, from information_schema",
	"ui/query/query.go:mysqlKeptChanges":   "SHOW WARNINGS after a statement the editor already audited",
}

// TestEverySendIsAudited fails when a function of the app or its views sends a
// statement or command, by a call to one of sendMethods, and calls none of
// recordCalls: a new way to reach a server must say in the audit log what it
// sent, or be listed in notAudited with why not. It is a heuristic by name:
// function literals count as part of the function that holds them, and a
// call whose receiver text says it is the app's own state ("state", "st.",
// "Local") or a catalog read is not a send.
func TestEverySendIsAudited(t *testing.T) {
	fset := token.NewFileSet()
	var unrecorded []string
	found := map[string]bool{}
	for _, root := range []string{".", "../ui"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := pathFromInternal(path) + ":" + funcName(fn)
				var sends, records bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					var callee string
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						callee = f.Sel.Name
						if slices.Contains(sendMethods, callee) && !isOwnState(f.X) {
							sends = true
						}
					case *ast.Ident:
						callee = f.Name
					}
					if slices.Contains(recordCalls, callee) {
						records = true
					}
					return true
				})
				if !sends {
					continue
				}
				found[name] = true
				if _, ok := notAudited[name]; !records && !ok {
					unrecorded = append(unrecorded, name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range unrecorded {
		t.Errorf("%s sends a statement or command and records none: audit it, or list it in notAudited with why not", name)
	}
	for name, why := range notAudited {
		switch {
		case why == "":
			t.Errorf("notAudited %s has no reason", name)
		case !found[name]:
			t.Errorf("notAudited %s no longer sends: remove it", name)
		}
	}
}

// pathFromInternal is a file's path from internal/, as notAudited writes it:
// the walk starts in internal/app and goes to ../ui.
func pathFromInternal(path string) string {
	path = filepath.ToSlash(path)
	if rest, ok := strings.CutPrefix(path, "../"); ok {
		return rest
	}
	return "app/" + path
}

// funcName is a function's name, with its receiver's type for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if idx, ok := typ.(*ast.IndexExpr); ok {
		typ = idx.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fmt.Sprint(typ) + "." + fn.Name.Name
}

// isOwnState reports whether a call's receiver reads as the app's own
// store, or a catalog read, not a server's statement: by a whole name in
// it, such as p.Local or d.Catalog().
func isOwnState(x ast.Expr) bool {
	own := false
	ast.Inspect(x, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			switch id.Name {
			case "st", "state", "Local", "Catalog":
				own = true
			}
		}
		return !own
	})
	return own
}
