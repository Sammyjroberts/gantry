package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SQLResult is a transport-neutral SQL result the query_sql tool returns. It
// mirrors duckdb.Result without importing the duckdb package, keeping mcp
// decoupled from the engine (the hosting app supplies an adapter).
type SQLResult struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Truncated bool             `json:"truncated"`
}

// SQLRunner runs a single read-only SQL query over the segment store. It is the
// narrow seam the query_sql tool needs; the hosting app wires an adapter around
// the DuckDB engine (which enforces read-only + row caps). Optional: a Deps
// without one registers no query_sql tool. When the engine binary is absent the
// adapter returns an error whose message is the install hint.
type SQLRunner interface {
	Query(ctx context.Context, sql string) (*SQLResult, error)
}

// registerSQLTool wires the query_sql tool when a runner is present.
func registerSQLTool(s *mcpsdk.Server, d Deps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name: "query_sql",
		Description: "Run a read-only SQL SELECT over the durable telemetry segment store using an embedded DuckDB engine. " +
			"The view `tlm` exposes columns (device, packet, channel, kind, ts_ns, received_ns, v_f64, v_i64, v_bool, v_str) — " +
			"one row per stored sample; the value is in the typed column matching `kind`. " +
			"Use this for aggregations and historical analysis over large ranges (e.g. \"SELECT channel, avg(v_f64) FROM tlm WHERE device='rover-1' GROUP BY channel\"). " +
			"Only SELECT/WITH queries are permitted; results are row-capped. For recent live values prefer get_window/get_last.",
	}, d.querySQL)
}

type querySQLArgs struct {
	SQL string `json:"sql" jsonschema:"a single read-only SQL SELECT/WITH query over the tlm view"`
}

func (d Deps) querySQL(ctx context.Context, _ *mcpsdk.CallToolRequest, args querySQLArgs) (*mcpsdk.CallToolResult, SQLResult, error) {
	if d.SQL == nil {
		return nil, SQLResult{}, fmt.Errorf("query_sql is not available on this server")
	}
	res, err := d.SQL.Query(ctx, args.SQL)
	if err != nil {
		return nil, SQLResult{}, err
	}
	return nil, *res, nil
}
