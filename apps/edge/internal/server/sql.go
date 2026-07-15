package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Sammyjroberts/gantry/libs/go/duckdb"
	"github.com/Sammyjroberts/gantry/libs/go/mcp"
)

// sqlRoute is the method+path pattern for the embedded SQL endpoint. It is a
// new, exact route that does not collide with the ConnectRPC service prefixes or
// the "/" UI fallback.
const sqlRoute = "POST /sql"

// sqlRequest is the POST /sql body: a single read-only SQL query.
type sqlRequest struct {
	SQL string `json:"sql"`
}

// sqlHandler serves POST /sql: it runs a read-only DuckDB query over the segment
// store and returns JSON rows. The engine enforces read-only (SELECT/WITH only),
// a row cap, and a timeout. When no DuckDB binary is installed it responds 503
// with the install hint so the surface degrades gracefully.
type sqlHandler struct {
	engine *duckdb.Engine
}

// NewSQLHandler builds the POST /sql handler over a DuckDB engine (which may be
// nil when no binary is installed — the handler then reports it clearly).
func NewSQLHandler(engine *duckdb.Engine) http.Handler {
	return &sqlHandler{engine: engine}
}

func (h *sqlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		writeSQLError(w, http.StatusServiceUnavailable, duckdb.ErrNotInstalled.Error())
		return
	}
	var req sqlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeSQLError(w, http.StatusBadRequest, "invalid JSON body: expected {\"sql\": \"...\"}")
		return
	}
	if req.SQL == "" {
		writeSQLError(w, http.StatusBadRequest, "missing sql")
		return
	}

	res, err := h.engine.Query(r.Context(), req.SQL)
	if err != nil {
		writeSQLError(w, sqlErrorStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

// sqlErrorStatus maps engine errors to HTTP status: a rejected (non-read-only)
// statement is a client error; a missing binary is unavailable; the rest are
// treated as bad requests (usually a SQL syntax error the caller can fix).
func sqlErrorStatus(err error) int {
	var roErr duckdb.ErrNotReadOnly
	switch {
	case errors.As(err, &roErr):
		return http.StatusBadRequest
	case errors.Is(err, duckdb.ErrNotInstalled):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

func writeSQLError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// sqlRunner adapts *duckdb.Engine to mcp.SQLRunner so the MCP query_sql tool and
// the HTTP endpoint share one engine and one set of read-only guards.
type sqlRunner struct {
	engine *duckdb.Engine
}

// NewSQLRunner adapts a DuckDB engine to mcp.SQLRunner. It returns nil when the
// engine is nil, so wiring `mcp.Deps.SQL = NewSQLRunner(p.SQL)` leaves query_sql
// unregistered when no binary is installed.
func NewSQLRunner(engine *duckdb.Engine) mcp.SQLRunner {
	if engine == nil {
		return nil
	}
	return &sqlRunner{engine: engine}
}

func (r *sqlRunner) Query(ctx context.Context, sql string) (*mcp.SQLResult, error) {
	res, err := r.engine.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return &mcp.SQLResult{
		Columns:   res.Columns,
		Rows:      res.Rows,
		RowCount:  res.RowCount,
		Truncated: res.Truncated,
	}, nil
}
