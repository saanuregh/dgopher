package db

import "testing"

// The profiles of EXPLAIN (ANALYZE, FORMAT JSON) SELECT g, count(*) FROM it_plan WHERE id > 5
// GROUP BY g, by DuckDB 1.5 and by DuckDB 2.0, which renamed their fields and wrapped the
// operators in its own.
const (
	duckdb15AnalyzedPlan = `{"rows_returned":0,"query_name":"EXPLAIN (ANALYZE, FORMAT JSON) SELECT g, count(*) FROM it_plan WHERE id > 5 GROUP BY g","cpu_time":0.000181771,"extra_info":{},"latency":0.00083796,"children":[{"operator_timing":2.2e-07,"operator_type":"EXPLAIN_ANALYZE","extra_info":{},"operator_cardinality":0,"operator_name":"EXPLAIN_ANALYZE","children":[{"operator_timing":9.097e-06,"operator_type":"PROJECTION","extra_info":{"Projections":["__internal_decompress_integral_bigint(#0, 0)","#1"],"Estimated Cardinality":"1292"},"operator_cardinality":1000,"operator_name":"PROJECTION","children":[{"operator_timing":7.507999999999999e-05,"operator_type":"PERFECT_HASH_GROUP_BY","extra_info":{"Groups":"#0","Aggregates":"count_star()"},"operator_cardinality":1000,"operator_name":"PERFECT_HASH_GROUP_BY","children":[{"operator_timing":5.33e-07,"operator_type":"PROJECTION","extra_info":{"Projections":"g","Estimated Cardinality":"4000"},"operator_cardinality":19994,"operator_name":"PROJECTION","children":[{"operator_timing":2.0187e-05,"operator_type":"PROJECTION","extra_info":{"Projections":"__internal_compress_integral_usmallint(#0, 0)","Estimated Cardinality":"4000"},"operator_cardinality":19994,"operator_name":"PROJECTION","children":[{"operator_timing":7.6654e-05,"operator_type":"TABLE_SCAN","extra_info":{"Table":"memory.main.it_plan","Type":"Sequential Scan","Projections":"g","Filters":"id>5","Estimated Cardinality":"4000"},"operator_cardinality":19994,"operator_name":"SEQ_SCAN","children":[]}]}]}]}]}]}]}`
	duckdb20AnalyzedPlan = `{"operator":[{"extra_info":{},"intermediate_rows":0,"name":"RESULT_COLLECTOR","timing":0.0,"type":"RESULT_COLLECTOR","children":[{"extra_info":{},"intermediate_rows":0,"name":"EXECUTE","timing":0.0,"type":"EXECUTE","children":[{"extra_info":{},"intermediate_rows":0,"name":"EXPLAIN_ANALYZE","timing":9e-08,"type":"EXPLAIN_ANALYZE","children":[{"extra_info":{"Projections":["CAST(#0 AS BIGINT)","#1"],"Estimated Cardinality":"1313"},"intermediate_rows":1000,"name":"PROJECTION","timing":6.632e-06,"type":"PROJECTION","children":[{"extra_info":{"Groups":"#0","Aggregates":"count_star()","Estimated Cardinality":"1313"},"intermediate_rows":1000,"name":"PERFECT_HASH_GROUP_BY","timing":8.227400000000002e-05,"type":"PERFECT_HASH_GROUP_BY","children":[{"extra_info":{"Projections":"g","Estimated Cardinality":"4000"},"intermediate_rows":19994,"name":"PROJECTION","timing":4.85e-07,"type":"PROJECTION","children":[{"extra_info":{"Projections":"CAST(#0 AS SMALLINT)","Estimated Cardinality":"4000"},"intermediate_rows":19994,"name":"PROJECTION","timing":1.0918e-05,"type":"PROJECTION","children":[{"extra_info":{"Table":"memory.main.it_plan","Type":"Sequential Scan","Projections":"g","Filters":"(id > 5)","Estimated Cardinality":"4000"},"intermediate_rows":19994,"name":"SEQ_SCAN","timing":7.0251e-05,"type":"TABLE_SCAN"}]}]}]}]}]}]}]}],"query":{"cpu_time":0.00017065,"sql":"EXPLAIN (ANALYZE, FORMAT JSON) SELECT g, count(*) FROM it_plan WHERE id > 5 GROUP BY g","total_intermediate_rows":61982,"total_intermediate_size_bytes":265928,"total_row_groups_scanned":1,"total_row_groups_to_scan":1,"total_rows_scanned":20000,"total_time":0.000989283}}`
)

// Both profiles read as the same plan: the query, its EXPLAIN_ANALYZE step, and the
// operators below it with their times and rows.
func TestParseDuckDBAnalyzedPlan(t *testing.T) {
	for name, text := range map[string]string{"1.5": duckdb15AnalyzedPlan, "2.0": duckdb20AnalyzedPlan} {
		p, ok := parseDuckDBPlan("analyzed_plan " + text)
		if !ok || !p.Analyzed || p.Root.Op != "QUERY" || p.Root.Time <= 0 || len(p.Root.Children) != 1 {
			t.Fatalf("%s: plan %+v", name, p)
		}
		explain := p.Root.Children[0]
		if explain.Op != "EXPLAIN_ANALYZE" || len(explain.Children) != 1 || explain.Children[0].Op != "PROJECTION" {
			t.Fatalf("%s: below the query %+v", name, explain)
		}
		var scan *PlanNode
		p.Root.Walk(func(n *PlanNode, _ int) {
			if n.Op == "SEQ_SCAN" {
				scan = n
			}
		})
		if scan == nil || scan.Target != "memory.main.it_plan" || scan.Rows != 4000 || scan.ActualRows != 19994 || scan.Time <= 0 || scan.Time > p.Root.Time {
			t.Fatalf("%s: scan %+v", name, scan)
		}
	}
}
