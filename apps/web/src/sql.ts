/**
 * SQL console — pure result-shaping, history persistence, and starter snippets.
 *
 * The DuckDB-backed /sql endpoint returns columns + row objects; this module
 * normalizes that into a flat, display-ready grid (capped for rendering) and
 * owns the localStorage-backed query history. All of it is React/DOM-free so the
 * cell formatting, the truncation cap, and the history de-dup/cap/persistence
 * are unit tested directly (see sql.test.ts). The thin POST transport lives in
 * sqlApi.ts; the panel component composes the two.
 */

/** Raw success payload from POST /sql. */
export interface SqlResponse {
  columns: string[];
  rows: Array<Record<string, unknown>>;
  row_count: number;
  /** Server truncated the result set to its row cap. */
  truncated: boolean;
}

/** Max rows the grid renders regardless of how many came back. */
export const DISPLAY_ROW_CAP = 500;
/** Query-history entries kept in localStorage. */
export const HISTORY_LIMIT = 25;
const HISTORY_KEY = "gantry-sql-history";

/** A display-ready result: header + string cells, with capping metadata. */
export interface SqlGrid {
  columns: string[];
  /** Rows as pre-formatted cell strings, aligned to `columns`. */
  rows: string[][];
  /** Rows the server reported (may exceed `rows.length`). */
  rowCount: number;
  /** Rows hidden by the display cap (0 when nothing was clipped here). */
  hiddenByDisplayCap: number;
  /** The server itself truncated to its own row cap. */
  serverTruncated: boolean;
}

/** Format one cell value for the grid: null/undefined explicit, objects JSON. */
export function formatCell(v: unknown): string {
  if (v === null || v === undefined) return "∅";
  if (typeof v === "boolean") return v ? "true" : "false";
  if (typeof v === "number") return Number.isFinite(v) ? String(v) : "∅";
  if (typeof v === "bigint") return v.toString();
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

/**
 * Shape a raw /sql response into a capped, string-celled grid. Rows beyond
 * {@link DISPLAY_ROW_CAP} are dropped from the render (reported via
 * `hiddenByDisplayCap`); `rowCount` preserves the server's total.
 */
export function toGrid(res: SqlResponse): SqlGrid {
  const cols = res.columns ?? [];
  const all = res.rows ?? [];
  const shown = all.slice(0, DISPLAY_ROW_CAP);
  const rows = shown.map((r) => cols.map((c) => formatCell(r[c])));
  return {
    columns: cols,
    rows,
    rowCount: res.row_count ?? all.length,
    hiddenByDisplayCap: Math.max(0, all.length - shown.length),
    serverTruncated: Boolean(res.truncated),
  };
}

/**
 * Prepend `sql` to the history list: trimmed, de-duplicated (an existing copy
 * moves to the front rather than duplicating), and capped at
 * {@link HISTORY_LIMIT}. Blank queries are ignored (returns the list unchanged).
 * Pure — persistence is a separate call so it is trivially testable.
 */
export function pushHistory(history: string[], sql: string): string[] {
  const q = sql.trim();
  if (q.length === 0) return history;
  const deduped = history.filter((h) => h !== q);
  return [q, ...deduped].slice(0, HISTORY_LIMIT);
}

/** Load the persisted query history (newest first). Tolerates absent/corrupt storage. */
export function loadHistory(): string[] {
  try {
    const raw = globalThis.localStorage?.getItem(HISTORY_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x): x is string => typeof x === "string").slice(0, HISTORY_LIMIT);
  } catch {
    return [];
  }
}

/** Persist the query history. Silently no-ops if storage is unavailable. */
export function saveHistory(history: string[]): void {
  try {
    globalThis.localStorage?.setItem(HISTORY_KEY, JSON.stringify(history.slice(0, HISTORY_LIMIT)));
  } catch {
    // storage disabled / quota — history is best-effort.
  }
}

/** A ready-to-run example query for the console. */
export interface SqlSnippet {
  label: string;
  sql: string;
}

/**
 * Starter snippets over the DuckDB schema: `tlm(device,packet,channel,kind,
 * ts_ns,received_ns,v_f64,v_i64,v_bool,v_str)` with the SQLite experiment
 * catalog attached as `catalog`.
 */
export const SQL_SNIPPETS: readonly SqlSnippet[] = [
  {
    label: "per-channel stats",
    sql: `SELECT device, packet, channel,
       count(*)      AS n,
       avg(v_f64)    AS mean,
       min(v_f64)    AS lo,
       max(v_f64)    AS hi
FROM tlm
WHERE ts_ns > (epoch_ns(now()) - 3600 * 1000000000)
GROUP BY device, packet, channel
ORDER BY n DESC
LIMIT 100;`,
  },
  {
    label: "latency by channel",
    sql: `SELECT device, packet, channel,
       count(*)                        AS n,
       avg(received_ns - ts_ns) / 1e6  AS mean_latency_ms,
       max(received_ns - ts_ns) / 1e6  AS max_latency_ms
FROM tlm
GROUP BY device, packet, channel
ORDER BY mean_latency_ms DESC
LIMIT 100;`,
  },
  {
    label: "experiment join",
    sql: `SELECT e.name AS experiment,
       t.channel,
       count(*)   AS samples,
       avg(t.v_f64) AS mean
FROM catalog.experiments e
JOIN tlm t
  ON t.ts_ns >= e.start_ns
 AND (e.end_ns = 0 OR t.ts_ns <= e.end_ns)
GROUP BY e.name, t.channel
ORDER BY e.name, samples DESC
LIMIT 100;`,
  },
];
