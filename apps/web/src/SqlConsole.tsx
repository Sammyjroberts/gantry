/**
 * SqlConsole — collapsible DuckDB query panel (lazy dock).
 *
 * A dense mono textarea (Ctrl/Cmd+Enter runs), a capped result grid, verbatim
 * error surfacing, and a localStorage query history with click-to-recall +
 * starter snippets. The transport is sqlApi.runSql; all shaping/persistence is
 * the pure sql.ts module (unit tested) so this file stays glue. A 503 (engine
 * absent) gets a friendlier install hint; a 400 (rejected / read-only violation)
 * shows the server message verbatim.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  DISPLAY_ROW_CAP,
  SQL_SNIPPETS,
  loadHistory,
  pushHistory,
  saveHistory,
  toGrid,
  type SqlGrid,
} from "./sql";
import { runSql, SqlError } from "./sqlApi";

export interface SqlConsoleProps {
  baseUrl: string;
  onClose: () => void;
}

const STARTER = "SELECT device, packet, channel, count(*) AS n\nFROM tlm\nGROUP BY 1, 2, 3\nORDER BY n DESC\nLIMIT 50;";

const DUCKDB_HINT =
  "DuckDB engine not installed — drop duckdb.exe into <data-dir>/duckdb/ and restart the Edge.";

export default function SqlConsole({ baseUrl, onClose }: SqlConsoleProps) {
  const [sql, setSql] = useState(STARTER);
  const [grid, setGrid] = useState<SqlGrid | null>(null);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [engineMissing, setEngineMissing] = useState(false);
  const [history, setHistory] = useState<string[]>([]);
  const [showHistory, setShowHistory] = useState(false);
  const acRef = useRef<AbortController | null>(null);

  useEffect(() => {
    setHistory(loadHistory());
  }, []);

  const run = useCallback(async () => {
    const query = sql.trim();
    if (query.length === 0 || running) return;
    acRef.current?.abort();
    const ac = new AbortController();
    acRef.current = ac;
    setRunning(true);
    setError(null);
    setEngineMissing(false);
    try {
      const res = await runSql(baseUrl, query, ac.signal);
      if (ac.signal.aborted) return;
      setGrid(toGrid(res));
      const next = pushHistory(history, query);
      setHistory(next);
      saveHistory(next);
    } catch (e) {
      if (ac.signal.aborted) return;
      if (e instanceof SqlError && e.status === 503) {
        setEngineMissing(true);
        setError(e.message);
      } else {
        setError(e instanceof Error ? e.message : String(e));
      }
      setGrid(null);
    } finally {
      if (!ac.signal.aborted) setRunning(false);
    }
  }, [baseUrl, sql, running, history]);

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>): void => {
    if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
      e.preventDefault();
      void run();
    }
  };

  const recall = (q: string): void => {
    setSql(q);
    setShowHistory(false);
  };

  return (
    <div className="sql-panel">
      <div className="sql-head">
        <span className="sql-title">▤ SQL</span>
        <div className="sql-snippets">
          {SQL_SNIPPETS.map((s) => (
            <button key={s.label} className="sql-chip" onClick={() => setSql(s.sql)} title="load snippet">
              {s.label}
            </button>
          ))}
        </div>
        <button
          className={`sql-chip ${showHistory ? "is-open" : ""}`}
          onClick={() => setShowHistory((v) => !v)}
          disabled={history.length === 0}
          title="query history"
        >
          history {history.length > 0 ? `(${history.length})` : ""}
        </button>
        <button className="sql-close" onClick={onClose} title="close SQL panel">
          ✕
        </button>
      </div>

      {showHistory && history.length > 0 && (
        <ul className="sql-history">
          {history.map((q, i) => (
            <li key={i}>
              <button className="sql-history-item" onClick={() => recall(q)} title="recall query">
                {q.replace(/\s+/g, " ").slice(0, 120)}
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="sql-editor">
        <textarea
          className="sql-textarea"
          spellCheck={false}
          wrap="off"
          value={sql}
          onChange={(e) => setSql(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="SELECT … FROM tlm  —  Ctrl/⌘+Enter to run"
        />
        <div className="sql-run-row">
          <button className="sql-run" onClick={() => void run()} disabled={running}>
            {running ? "running…" : "▶ run"}
          </button>
          <span className="sql-hint">Ctrl/⌘+Enter</span>
          {grid && (
            <span className="sql-rowcount">
              {grid.rowCount} rows
              {grid.serverTruncated && <span className="sql-flag" title="server row cap hit">truncated</span>}
              {grid.hiddenByDisplayCap > 0 && (
                <span className="sql-flag sql-flag--muted" title={`showing first ${DISPLAY_ROW_CAP}`}>
                  +{grid.hiddenByDisplayCap} hidden
                </span>
              )}
            </span>
          )}
        </div>
      </div>

      {error && (
        <div className={`sql-error ${engineMissing ? "sql-error--engine" : ""}`}>
          {engineMissing ? (
            <>
              <div className="sql-error-title">⚠ {DUCKDB_HINT}</div>
              <div className="sql-error-detail">{error}</div>
            </>
          ) : (
            <span>⚠ {error}</span>
          )}
        </div>
      )}

      {grid && !error && (
        <div className="sql-results">
          {grid.columns.length === 0 ? (
            <div className="sql-empty">no columns · {grid.rowCount} rows</div>
          ) : (
            <table className="sql-table">
              <thead>
                <tr>
                  {grid.columns.map((c) => (
                    <th key={c}>{c}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {grid.rows.map((row, ri) => (
                  <tr key={ri}>
                    {row.map((cell, ci) => (
                      <td key={ci} title={cell}>
                        {cell}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}
