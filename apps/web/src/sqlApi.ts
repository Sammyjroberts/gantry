/**
 * SQL transport against the Edge server's POST /sql endpoint (same origin as the
 * Connect RPC + /models + /video routes — see config.resolveBaseUrl).
 *
 *   POST /sql {"sql":"SELECT …"} → 200 {columns,rows,row_count,truncated}
 *     400 → query rejected / read-only violation (message is the body text)
 *     503 → DuckDB engine not installed (message in the body)
 *
 * The message on 400/503 is surfaced verbatim to the console (the panel renders
 * a friendlier hint for 503), so this thin wrapper throws a typed error carrying
 * the HTTP status and the server's message.
 */

import type { SqlResponse } from "./sql";

/** An error from POST /sql carrying the HTTP status and server message. */
export class SqlError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "SqlError";
    this.status = status;
  }
}

function trimBase(baseUrl: string): string {
  return baseUrl.replace(/\/$/, "");
}

/**
 * Run a query. Resolves the shaped {@link SqlResponse} on 200; throws
 * {@link SqlError} (with status + server message) on 400/503/other non-2xx.
 */
export async function runSql(
  baseUrl: string,
  sql: string,
  signal?: AbortSignal,
): Promise<SqlResponse> {
  let res: Response;
  try {
    res = await fetch(`${trimBase(baseUrl)}/sql`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ sql }),
      signal,
    });
  } catch (err) {
    throw new SqlError(0, err instanceof Error ? err.message : String(err));
  }
  if (!res.ok) {
    const text = (await res.text().catch(() => "")).trim();
    throw new SqlError(res.status, text || `HTTP ${res.status}`);
  }
  const body = (await res.json()) as Partial<SqlResponse>;
  return {
    columns: body.columns ?? [],
    rows: body.rows ?? [],
    row_count: body.row_count ?? (body.rows ? body.rows.length : 0),
    truncated: Boolean(body.truncated),
  };
}
