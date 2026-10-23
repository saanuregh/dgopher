package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	agent "github.com/saanuregh/agent-cli-connector/bindings/go"

	"dgopher/internal/ui/dataview"
)

// chartRows is how many of a chart's rows the agent is told of.
const chartRows = 10

// chartWait is how long a chart's run may take, past the user's answer.
const chartWait = 5 * time.Minute

var chartSchema = json.RawMessage(`{"type":"object","properties":{
	"sql":{"type":"string","description":"One statement that only reads, for the editor's connection, whose rows the chart draws."},
	"kind":{"type":"string","enum":` + string(mustJSON(dataview.ChartKinds())) + `,"description":"The kind of chart."},
	"x":{"type":"string","description":"The column along the x axis, or the labels of a pie."},
	"series":{"type":"array","items":{"type":"string"},"description":"The numeric columns drawn as series."}},
	"required":["sql","kind"]}`)

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

type chartArgs struct {
	SQL    string   `json:"sql"`
	Kind   string   `json:"kind"`
	X      string   `json:"x"`
	Series []string `json:"series"`
}

// chartTool puts a read in the editor and, once the user allows it,
// runs it there and charts its rows with the editor's chart.
func (t *Chat) chartTool() agent.Tool {
	return agent.NewTool("chart_in_editor", "Charts a read-only statement in the user's SQL editor, once the user allows it: the statement goes in the editor, runs on the editor's connection, and its rows show as the editor's chart, of a kind, an x column and numeric series. Returns the columns and the first rows. Refused on production, while a transaction is open in the editor, and for any statement that changes something.", chartSchema, t.chartInEditor)
}

func (t *Chat) chartInEditor(ctx context.Context, args chartArgs) (agent.ToolResult, error) {
	sql := strings.TrimSpace(args.SQL)
	if !slices.Contains(dataview.ChartKinds(), args.Kind) {
		return agent.ErrorResult("kind is one of " + strings.Join(dataview.ChartKinds(), ", ")), nil
	}
	r := &request{id: fmt.Sprintf("chart-%d", t.nextID.Add(1)), sql: sql, answer: make(chan decision, 1)}
	noRows := false
	refusal, err := onMain(ctx, t.a.Post, func() string {
		cn := t.conn()
		if cn == nil || !cn.Config.Engine.IsSQL() {
			return "the editor has no SQL connection"
		}
		if why := readRefusal(&cn.Config, sql); why != "" {
			t.a.RecordBlocked(cn, "AI chat: "+why, sql)
			return why
		}
		r.title = "Chart in the editor on " + cn.Config.Name
		r.sql = formatSQL(&cn.Config, sql)
		noRows = cn.Config.NoRowsToChat
		r.detail = chartDetail(args, noRows)
		t.requests = append(t.requests, r)
		return ""
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	if refusal != "" {
		return agent.ErrorResult("Not charted: " + refusal), nil
	}
	select {
	case d := <-r.answer:
		if d == deny {
			return agent.ErrorResult("The user did not allow the chart."), nil
		}
	case <-ctx.Done():
		t.a.Post(func() { t.answer(r, deny, "") })
		return agent.ToolResult{}, ctx.Err()
	}
	got := make(chan ChartResult, 1)
	why, err := onMain(ctx, t.a.Post, func() string {
		return t.ed.ChartSQL(sql, dataview.ChartSettings{Kind: args.Kind, X: args.X, Series: args.Series}, func(res ChartResult) { got <- res })
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	if why != "" {
		return agent.ErrorResult("Not charted: " + why), nil
	}
	wait, cancel := context.WithTimeout(ctx, chartWait)
	defer cancel()
	select {
	case res := <-got:
		if res.Err != "" {
			if noRows {
				return agent.ErrorResult("Not charted: the statement failed; its message stays with the user, as the connection keeps its data from you."), nil
			}
			return agent.ErrorResult("Not charted: " + res.Err), nil
		}
		head := fmt.Sprintf("Charted in the editor as a %s chart of %d rows read.", strings.ToLower(args.Kind), len(res.Rows))
		if noRows {
			return agent.TextResult(head + " Its columns: " + strings.Join(res.Columns, ", ") + ". The connection keeps its rows from you."), nil
		}
		rows := res.Rows[:min(len(res.Rows), chartRows)]
		if len(rows) < len(res.Rows) {
			head += fmt.Sprintf(" The first %d:", len(rows))
		}
		return agent.TextResult(head + "\n" + rowsText(res.Columns, rows, false)), nil
	case <-wait.Done():
		return agent.ErrorResult("The chart's statement had not ended after " + chartWait.String() + "; it may still show in the editor."), nil
	}
}

// chartDetail is what a chart's card says it does.
func chartDetail(args chartArgs, noRows bool) string {
	what := strings.ToLower(args.Kind) + " chart"
	if len(args.Series) > 0 {
		what += " of " + strings.Join(args.Series, ", ")
	}
	if args.X != "" {
		what += " by " + args.X
	}
	tell := " The first rows go to the agent."
	if noRows {
		tell = " The agent hears its columns and how many rows, no data."
	}
	return "Puts the statement in the editor and runs it there, then shows its rows as a " + what + "." + tell
}
