/**
 * Auth gate — central detection of "this Bench is challenging us for a token".
 *
 * A remote Bench answers an un-credentialed (or wrong-scope) request with 401
 * (Unauthenticated). We classify that ONE condition here from both transports —
 * ConnectRPC (ConnectError with Code.Unauthenticated) and plain fetch (res
 * .status === 401) — and flip a global `needsAuth` flag that App renders as the
 * "Connect to bench" prompt.
 *
 * Localhost NEVER sees this: the Bench fully trusts loopback and never returns
 * 401 to it, so the flag simply never flips there. No client-side origin check
 * is needed — the server contract guarantees it.
 *
 * A 403 (PermissionDenied / insufficient scope) is deliberately NOT gated: the
 * caller IS authenticated, just under-scoped, so re-prompting for a token is the
 * wrong move. Those surface as ordinary errors where they happen.
 */

import { Code, ConnectError, bearerInterceptor, type Interceptor } from "@gantry/api-client";
import { getToken } from "./token";

let needsAuth = false;
const listeners = new Set<() => void>();

/** True once a 401 has been observed and not yet resolved. */
export function getNeedsAuth(): boolean {
  return needsAuth;
}

/** Subscribe to needsAuth changes; returns an unsubscribe. */
export function subscribeNeedsAuth(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

function setNeedsAuth(v: boolean): void {
  if (needsAuth === v) return;
  needsAuth = v;
  for (const cb of listeners) cb();
}

/** Clear the gate (e.g. after the user submits a token and we retry). */
export function clearNeedsAuth(): void {
  setNeedsAuth(false);
}

/** True if `err` is a 401/Unauthenticated from either transport. */
export function isAuthChallenge(err: unknown): boolean {
  if (err instanceof ConnectError) return err.code === Code.Unauthenticated;
  return false;
}

/**
 * Report a value that might be an auth challenge. RPC callers pass the caught
 * ConnectError; fetch wrappers call {@link reportFetchStatus} instead. Flips the
 * gate on when it recognises a 401; otherwise a no-op.
 */
export function reportAuthChallenge(err: unknown): void {
  if (isAuthChallenge(err)) setNeedsAuth(true);
}

/** Flip the gate on for a plain-fetch 401. Any other status is ignored. */
export function reportFetchStatus(status: number): void {
  if (status === 401) setNeedsAuth(true);
}

/**
 * Transport interceptor that watches RPC responses for a 401 and flips the gate.
 * Paired with the bearer interceptor in {@link apiClientOptions} so every RPC
 * both presents the token and reports a challenge centrally.
 */
const authReportInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req);
  } catch (err) {
    reportAuthChallenge(err);
    throw err;
  }
};

/**
 * Standard client options every console RPC client spreads in: attach the
 * bearer token (read freshly per request) and report 401s to the gate. Call
 * sites stay simple — `createXClient(baseUrl, apiClientOptions())`.
 */
export function apiClientOptions() {
  return { interceptors: [bearerInterceptor(getToken), authReportInterceptor] };
}
