/**
 * Bench access token — the PER-BROWSER credential this tab presents to a remote
 * Bench (non-loopback callers need a bearer token; localhost is always trusted).
 *
 * The token lives in localStorage on purpose. This does NOT violate the repo's
 * "no-cheats persistence" rule: that rule governs durable/roaming CONFIG, which
 * must live server-side (edge.db / Postgres) so it follows the user across
 * machines. A bench token is the opposite — a machine-local CREDENTIAL that
 * should stay pinned to THIS browser and never roam. localStorage is the right
 * home for it.
 *
 * A tiny subscribe mechanism lets React re-render when the token changes (via
 * the useToken hook), and the fetch/href helpers below thread it into the plain
 * HTTP routes that can't run the Connect bearer interceptor.
 */

const STORAGE_KEY = "gantry-bench-token";

// In-memory mirror so getToken() is cheap and works even if storage is disabled
// (private mode / blocked). Seeded lazily from localStorage on first read.
let cached: string | null | undefined;
const listeners = new Set<() => void>();

function readStorage(): string | null {
  if (typeof localStorage === "undefined") return null;
  try {
    return localStorage.getItem(STORAGE_KEY);
  } catch {
    return null; // storage disabled — fall back to the in-memory mirror
  }
}

/** The current token for this browser, or null when none is set. */
export function getToken(): string | null {
  if (cached === undefined) cached = readStorage();
  return cached ?? null;
}

/** Store a token (trimmed). Empty/whitespace clears it. Notifies subscribers. */
export function setToken(token: string): void {
  const t = token.trim();
  if (t.length === 0) {
    clearToken();
    return;
  }
  cached = t;
  try {
    localStorage?.setItem(STORAGE_KEY, t);
  } catch {
    /* storage disabled — keep the in-memory value */
  }
  emit();
}

/** Forget the token for this browser. Notifies subscribers. */
export function clearToken(): void {
  cached = null;
  try {
    localStorage?.removeItem(STORAGE_KEY);
  } catch {
    /* storage disabled — nothing to remove */
  }
  emit();
}

/** Subscribe to token changes; returns an unsubscribe. */
export function subscribeToken(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

function emit(): void {
  for (const cb of listeners) cb();
}

/**
 * Auth header for the plain-fetch routes (/sql, /models, /video). Returns
 * `{ Authorization: "Bearer <t>" }` when a token is set, else `{}` so a trusted
 * localhost caller sends nothing (the Bench never challenges loopback).
 */
export function authHeaders(): Record<string, string> {
  const t = getToken();
  return t ? { Authorization: "Bearer " + t } : {};
}

/**
 * Append `?token=<t>` (or `&token=`) to a GET download URL when a token is set.
 * This is ONLY for `<a download>` / `<video src>` targets under /export/ and
 * /video/ that cannot set an Authorization header — the server accepts the
 * query token exclusively on those read-scoped GET routes. Every other request
 * uses the bearer header. No token ⇒ URL returned unchanged (localhost path).
 */
export function withToken(url: string): string {
  const t = getToken();
  if (!t) return url;
  const sep = url.includes("?") ? "&" : "?";
  return `${url}${sep}token=${encodeURIComponent(t)}`;
}
