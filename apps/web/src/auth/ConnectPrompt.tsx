/**
 * "Connect to bench" prompt — the minimal gate shown when a remote Bench
 * challenges us for a token (a 401 flipped the auth gate; see authGate.ts).
 *
 * Localhost NEVER sees this: the Bench trusts loopback and never returns 401 to
 * it, so `useNeedsAuth()` stays false there and the overlay never mounts.
 *
 * Deliberately tiny: one token input + Connect. On submit we store the token and
 * reload — the simplest correct retry, since every in-flight query re-runs with
 * the new credential from a clean slate (no partial re-wiring of live streams).
 */

import { useState } from "react";
import { KeyRound } from "lucide-react";
import { setToken } from "./token";
import { clearNeedsAuth } from "./authGate";
import { useNeedsAuth } from "./hooks";

export function ConnectPrompt() {
  const needsAuth = useNeedsAuth();
  const [value, setValue] = useState("");

  if (!needsAuth) return null;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const t = value.trim();
    if (!t) return;
    setToken(t);
    clearNeedsAuth();
    // Full reload: cleanest retry — all clients/streams re-init with the token.
    if (typeof window !== "undefined") window.location.reload();
  };

  return (
    <div className="auth-overlay" role="dialog" aria-modal="true" aria-label="Connect to bench">
      <form className="auth-card" onSubmit={submit}>
        <div className="auth-card-head">
          <KeyRound size={18} className="auth-card-icon" aria-hidden />
          <span className="auth-card-title">Connect to bench</span>
        </div>
        <p className="auth-card-sub">
          This bench needs an access token. Paste one, or create it on the bench under
          Settings → Access tokens.
        </p>
        <input
          className="auth-input"
          type="password"
          autoFocus
          spellCheck={false}
          autoComplete="off"
          placeholder="gtk_…"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          data-testid="auth-token-input"
        />
        <button className="auth-connect" type="submit" disabled={value.trim().length === 0}>
          Connect
        </button>
      </form>
    </div>
  );
}
