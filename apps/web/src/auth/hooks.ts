/**
 * React bindings over the framework-free auth stores (token.ts + authGate.ts).
 * useSyncExternalStore keeps components in step with credential + gate changes
 * without a Context provider.
 */

import { useSyncExternalStore } from "react";
import { getToken, subscribeToken } from "./token";
import { getNeedsAuth, subscribeNeedsAuth } from "./authGate";

/** Re-render when the stored token changes; returns the current token or null. */
export function useToken(): string | null {
  return useSyncExternalStore(subscribeToken, getToken, getToken);
}

/** Re-render when the auth gate flips; true once a 401 has been seen. */
export function useNeedsAuth(): boolean {
  return useSyncExternalStore(subscribeNeedsAuth, getNeedsAuth, getNeedsAuth);
}
