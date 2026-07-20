package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/Sammyjroberts/gantry/core/go/duckdb"
	"github.com/Sammyjroberts/gantry/core/go/eval"
)

// duckdbSampler adapts the DuckDB engine to eval.Sampler: it aggregates one
// channel's typed value column over a device/time window against the segment
// store's `tlm` view (columns device, channel, ts_ns, v_f64/v_i64/v_bool). It is
// how a suite's telemetry verifier scores a trial straight from telemetry.
type duckdbSampler struct {
	engine *duckdb.Engine
}

// NewDuckDBSampler adapts a DuckDB engine to eval.Sampler, or returns nil when
// the engine is nil (no binary installed) so eval.WithSampler leaves auto-scoring
// off and the rest of the app keeps running.
func NewDuckDBSampler(engine *duckdb.Engine) eval.Sampler {
	if engine == nil {
		return nil
	}
	return &duckdbSampler{engine: engine}
}

// allowlists keep agg/col out of the SQL string as injection vectors (device and
// channel are single-quote escaped instead).
var (
	allowedAgg = map[string]bool{"max": true, "min": true, "avg": true, "sum": true, "count": true}
	allowedCol = map[string]bool{"v_f64": true, "v_i64": true, "v_bool": true}
)

func (d *duckdbSampler) Aggregate(ctx context.Context, device, channel, col, agg string, startNs, endNs uint64) (float64, bool, error) {
	if !allowedAgg[agg] {
		return 0, false, fmt.Errorf("duckdbSampler: unsupported agg %q", agg)
	}
	if !allowedCol[col] {
		return 0, false, fmt.Errorf("duckdbSampler: unsupported col %q", col)
	}
	// device may be empty to span all devices for the channel.
	where := fmt.Sprintf("channel = '%s' AND ts_ns BETWEEN %d AND %d", esc(channel), startNs, endNs)
	if device != "" {
		where = fmt.Sprintf("device = '%s' AND %s", esc(device), where)
	}
	sql := fmt.Sprintf("SELECT %s(%s::DOUBLE) AS v, count(*) AS n FROM tlm WHERE %s", agg, col, where)

	res, err := d.engine.Query(ctx, sql)
	if err != nil {
		return 0, false, fmt.Errorf("duckdbSampler: query: %w", err)
	}
	if len(res.Rows) == 0 {
		return 0, false, nil
	}
	row := res.Rows[0]
	n := toFloat(row["n"])
	if n == 0 {
		return 0, false, nil
	}
	return toFloat(row["v"]), true, nil
}

// esc single-quote-escapes a SQL string literal.
func esc(s string) string { return strings.ReplaceAll(s, "'", "''") }

// toFloat coerces DuckDB's JSON scalar (float64, or numeric string) to a float.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}
