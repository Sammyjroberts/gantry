/**
 * Resolve the API base URL.
 *
 * Priority: `?api=` query param → `VITE_API_BASE` env → `window.location.origin`.
 * In production the Edge binary serves the bundle, so same-origin (`origin`) is
 * correct. In dev the origin is the Vite server and requests to `/gantry.v1` are
 * proxied to the Edge (see vite.config.ts), so origin also works there.
 */
export function resolveBaseUrl(): string {
  if (typeof window === "undefined") return "http://localhost:4780";
  const q = new URLSearchParams(window.location.search).get("api");
  if (q) return q;
  const env = import.meta.env.VITE_API_BASE as string | undefined;
  if (env && env.length > 0) return env;
  return window.location.origin;
}

/** Seconds of JetStream history to replay on every (re)subscribe. */
export const REPLAY_SECONDS = 30;

/** Selectable visible-window sizes, in seconds. */
export const WINDOW_OPTIONS = [10, 30, 60, 120, 300] as const;
