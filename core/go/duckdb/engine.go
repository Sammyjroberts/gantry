package duckdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Defaults for a bench-scale interactive SQL surface.
const (
	DefaultMaxRows  = 10_000
	DefaultTimeout  = 30 * time.Second
	viewName        = "tlm"
	catalogAttachAs = "catalog"
)

// Config parameterises an Engine.
type Config struct {
	// SegmentsGlob is the DuckDB read_parquet glob over the segment files, e.g.
	// "<blobroot>/segments/*/*.parquet". Required for the tlm view.
	SegmentsGlob string
	// CatalogPath is the SQLite catalog database to ATTACH read-only as
	// "catalog" (so experiments are joinable). Optional; attach is best-effort and
	// silently skipped if the engine's build lacks the sqlite scanner.
	CatalogPath string
	// MaxRows caps returned rows (0 → DefaultMaxRows). One extra row is fetched
	// internally to set Result.Truncated.
	MaxRows int
	// Timeout bounds a single query (0 → DefaultTimeout).
	Timeout time.Duration
}

// Engine runs read-only SQL by shelling out to a managed DuckDB binary. It holds
// no long-lived process: each Query execs the binary in an in-memory database,
// prepares the tlm view (and optional catalog attach) via a script on stdin, and
// parses the JSON result. Per-query exec keeps the surface stateless and
// crash-isolated; at bench query rates the process spin-up cost is negligible
// relative to the scan, and it sidesteps managing a long-lived subprocess's
// lifecycle/health. Safe for concurrent use.
type Engine struct {
	bin string
	cfg Config

	probe    sync.Once
	sqliteOK bool
}

// Result is a query result as ordered columns plus JSON-friendly row objects.
type Result struct {
	Columns   []string         `json:"columns"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int              `json:"row_count"`
	Truncated bool             `json:"truncated"`
}

// New builds an Engine, resolving the binary through the provider. It returns a
// wrapped ErrNotInstalled when no binary is available, so callers can present
// the install hint and keep the rest of the app running.
func New(p Provider, cfg Config) (*Engine, error) {
	bin, ok := p.Binary()
	if !ok {
		return nil, ErrNotInstalled
	}
	if cfg.MaxRows <= 0 {
		cfg.MaxRows = DefaultMaxRows
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Engine{bin: bin, cfg: cfg}, nil
}

// Available reports whether a binary was resolved (always true for a non-nil
// Engine, since New fails otherwise). Provided for symmetry with call sites that
// hold a possibly-nil *Engine.
func (e *Engine) Available() bool { return e != nil && e.bin != "" }

// Binary returns the resolved engine path (for status/version reporting).
func (e *Engine) Binary() string { return e.bin }

// Version returns the DuckDB engine version string (e.g. "v1.1.3").
func (e *Engine) Version(ctx context.Context) (string, error) {
	out, err := e.run(ctx, "", []string{"-version"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Query validates sql is read-only, prepares the tlm view over the segment glob
// (plus the catalog attach when supported), executes the wrapped+capped query,
// and returns the rows. The wrap — SELECT * FROM (<sql>) LIMIT n+1 — enforces
// single-statement, read-only execution and the row cap in one move.
func (e *Engine) Query(ctx context.Context, sql string) (*Result, error) {
	clean, err := validateReadOnly(sql)
	if err != nil {
		return nil, err
	}
	e.probe.Do(func() { e.sqliteOK = e.probeSQLite(ctx) })

	maxRows := e.cfg.MaxRows
	script := e.buildScript(clean, maxRows+1)

	cctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	out, err := e.run(cctx, script, []string{"-json"})
	if err != nil {
		return nil, err
	}
	rows, cols, err := parseJSONRows(out)
	if err != nil {
		return nil, err
	}
	res := &Result{Columns: cols, Rows: rows}
	if len(rows) > maxRows {
		res.Rows = rows[:maxRows]
		res.Truncated = true
	}
	res.RowCount = len(res.Rows)
	return res, nil
}

// buildScript assembles the stdin script: optional catalog attach, the tlm view
// (empty typed view when the glob currently matches no files so queries succeed
// before the first flush), then the wrapped user query.
func (e *Engine) buildScript(userSQL string, limit int) string {
	var b strings.Builder
	if e.sqliteOK && e.cfg.CatalogPath != "" {
		fmt.Fprintf(&b, "ATTACH '%s' AS %s (TYPE SQLITE, READ_ONLY);\n", sqlEscape(e.cfg.CatalogPath), catalogAttachAs)
	}
	if e.globHasFiles() {
		fmt.Fprintf(&b, "CREATE VIEW %s AS SELECT * FROM read_parquet('%s', union_by_name=true);\n", viewName, sqlEscape(e.cfg.SegmentsGlob))
	} else {
		b.WriteString(emptyTlmView)
	}
	fmt.Fprintf(&b, "SELECT * FROM (\n%s\n) AS _gantry_q LIMIT %d;\n", userSQL, limit)
	return b.String()
}

// emptyTlmView defines tlm with the segment schema but zero rows, so SELECTs
// against tlm succeed (returning nothing) before any segment has been flushed.
const emptyTlmView = "CREATE VIEW " + viewName + ` AS SELECT
  ''::VARCHAR AS device, ''::VARCHAR AS packet, ''::VARCHAR AS channel,
  0::INTEGER AS kind, 0::BIGINT AS ts_ns, 0::BIGINT AS received_ns,
  0::DOUBLE AS v_f64, 0::BIGINT AS v_i64, false AS v_bool, ''::VARCHAR AS v_str
WHERE false;
`

// probeSQLite checks whether this DuckDB build can ATTACH the SQLite catalog
// (the sqlite scanner extension). If not, the catalog join is silently omitted;
// the tlm parquet view (the primary feature) still works.
func (e *Engine) probeSQLite(ctx context.Context) bool {
	if e.cfg.CatalogPath == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	script := fmt.Sprintf("ATTACH '%s' AS _probe (TYPE SQLITE, READ_ONLY);\nSELECT 1;\n", sqlEscape(e.cfg.CatalogPath))
	_, err := e.run(cctx, script, []string{"-json"})
	return err == nil
}

// globHasFiles reports whether the segment glob currently matches any file.
func (e *Engine) globHasFiles() bool {
	if e.cfg.SegmentsGlob == "" {
		return false
	}
	matches, err := filepath.Glob(e.cfg.SegmentsGlob)
	return err == nil && len(matches) > 0
}

// run executes the binary with args, feeding script on stdin, and returns
// stdout. A non-zero exit is wrapped with stderr for a useful error.
func (e *Engine) run(ctx context.Context, script string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.bin, args...)
	if script != "" {
		cmd.Stdin = strings.NewReader(script)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("duckdb: query timed out after %s", e.cfg.Timeout)
		}
		if msg != "" {
			return nil, fmt.Errorf("duckdb: %s", firstLine(msg))
		}
		return nil, fmt.Errorf("duckdb: exec: %w", err)
	}
	return stdout.Bytes(), nil
}

// parseJSONRows parses DuckDB's -json output (a JSON array of row objects). The
// script's DDL statements produce no output, so the last JSON array in stdout is
// the query result. Column order is recovered from the first row's key order.
func parseJSONRows(out []byte) ([]map[string]any, []string, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return []map[string]any{}, nil, nil
	}
	// DuckDB emits one array; if any DDL slipped an extra array in, take the last.
	start := bytes.LastIndexByte(trimmed, '[')
	if start > 0 {
		// Only shift if everything before is prior-statement output ending in ']'.
		if bytes.HasSuffix(bytes.TrimSpace(trimmed[:start]), []byte("]")) {
			trimmed = trimmed[start:]
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, nil, fmt.Errorf("duckdb: parse result: %w", err)
	}
	var cols []string
	if len(rows) > 0 {
		cols = orderedKeys(trimmed)
	}
	return rows, cols, nil
}

// orderedKeys extracts the key order of the first object in a JSON array,
// preserving column order (Go maps do not).
func orderedKeys(arr []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(arr))
	// Consume '['.
	if _, err := dec.Token(); err != nil {
		return nil
	}
	// Consume '{'.
	if _, err := dec.Token(); err != nil {
		return nil
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				depth++
			case '}', ']':
				if depth == 0 {
					return keys
				}
				depth--
			}
		case string:
			if depth == 0 {
				keys = append(keys, t)
				// Skip this key's value.
				if err := skipValue(dec); err != nil {
					return keys
				}
			}
		}
	}
	return keys
}

// skipValue reads and discards one JSON value (used to step over an object
// field's value while collecting keys at the current depth).
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); ok && (d == '{' || d == '[') {
		depth := 1
		for depth > 0 {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			if dd, ok := t.(json.Delim); ok {
				if dd == '{' || dd == '[' {
					depth++
				} else {
					depth--
				}
			}
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// sqlEscape doubles single quotes so a path is safe inside a single-quoted SQL
// literal. Paths are engine-internal (never user input), but this keeps Windows
// paths and odd characters from breaking the script.
func sqlEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
