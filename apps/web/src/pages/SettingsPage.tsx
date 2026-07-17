import { useState } from "react";
import { Check, Copy, Plus, Trash2, X } from "lucide-react";
import type { TokenInfo } from "@gantry/api-client";
import { useTokenList, useCreateToken, useDeleteToken } from "../query/useTokens";

/**
 * Settings → Access tokens. Manage the named, scoped machine credentials a
 * remote caller presents to this Bench (localhost is always trusted and needs
 * none). List / create / revoke against TokenService via the useTokens hooks.
 *
 * The create response carries the full secret exactly ONCE; we surface it in a
 * copy-once block and never keep it after dismiss (it only lives in this
 * component's state, never in the query cache).
 */

/** The four route families, with the one-line meaning shown next to each box. */
const SCOPES: ReadonlyArray<{ id: string; label: string; desc: string }> = [
  { id: "ingest", label: "ingest", desc: "publish / register telemetry" },
  { id: "read", label: "read", desc: "query, live, export, SQL, model reads, MCP reads" },
  { id: "operate", label: "operate", desc: "experiments, workspaces, video capture, MCP writes" },
  { id: "admin", label: "admin", desc: "hardware config, model uploads, token management" },
];

/** Format an epoch-ns bigint as a compact local datetime; 0 ⇒ "never". */
function fmtNs(ns: bigint): string {
  if (ns === 0n) return "never";
  const d = new Date(Number(ns / 1_000_000n));
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function SettingsPage() {
  const list = useTokenList();
  const createMut = useCreateToken();
  const deleteMut = useDeleteToken();

  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set(["read"]));
  const [secret, setSecret] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirmId, setConfirmId] = useState<string | null>(null);

  const toggleScope = (id: string) =>
    setScopes((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const resetForm = () => {
    setCreating(false);
    setName("");
    setScopes(new Set(["read"]));
  };

  const submitCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (name.trim().length === 0 || scopes.size === 0) return;
    const res = await createMut.mutateAsync({ name: name.trim(), scopes: [...scopes] });
    setSecret(res.secret); // shown exactly once
    setCopied(false);
    resetForm();
  };

  const copySecret = async () => {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
    } catch {
      /* clipboard blocked — the value is still selectable in the block */
    }
  };

  const rows: TokenInfo[] = list.data ?? [];

  return (
    <div className="page settings-page">
      <div className="page-body">
        <h1 className="page-title">Settings</h1>

        <div className="settings-section-head">
          <h2 className="hw-section">Access tokens</h2>
          {!creating && (
            <button className="tok-btn tok-btn--accent" onClick={() => setCreating(true)}>
              <Plus size={14} /> New token
            </button>
          )}
        </div>
        <p className="settings-hint">
          Machine credentials for reaching this bench from anywhere that isn't localhost. Localhost
          is always trusted and needs no token.
        </p>

        {/* Secret reveal — shown once, right after creation, then gone. */}
        {secret && (
          <div className="tok-secret" role="status" data-testid="token-secret">
            <div className="tok-secret-head">
              <strong>You won't see this again — copy it now.</strong>
              <button className="tok-icon-btn" title="dismiss" onClick={() => setSecret(null)}>
                <X size={14} />
              </button>
            </div>
            <code className="tok-secret-value">{secret}</code>
            <button className="tok-btn tok-btn--accent" onClick={copySecret} data-testid="token-copy">
              {copied ? <Check size={14} /> : <Copy size={14} />} {copied ? "Copied" : "Copy"}
            </button>
          </div>
        )}

        {/* New-token form. */}
        {creating && (
          <form className="tok-form" onSubmit={submitCreate}>
            <input
              className="tok-input"
              placeholder="token name (e.g. ci-runner)"
              value={name}
              autoFocus
              onChange={(e) => setName(e.target.value)}
              data-testid="token-name"
            />
            <div className="tok-scopes">
              {SCOPES.map((s) => (
                <label key={s.id} className="tok-scope" data-testid={`scope-${s.id}`}>
                  <input
                    type="checkbox"
                    checked={scopes.has(s.id)}
                    onChange={() => toggleScope(s.id)}
                  />
                  <span className="tok-scope-name">{s.label}</span>
                  <span className="tok-scope-desc">{s.desc}</span>
                </label>
              ))}
            </div>
            {createMut.isError && (
              <div className="page-error">
                {createMut.error instanceof Error ? createMut.error.message : "create failed"}
              </div>
            )}
            <div className="tok-form-actions">
              <button
                className="tok-btn tok-btn--accent"
                type="submit"
                disabled={name.trim().length === 0 || scopes.size === 0 || createMut.isPending}
              >
                {createMut.isPending ? "Creating…" : "Create token"}
              </button>
              <button className="tok-btn" type="button" onClick={resetForm}>
                Cancel
              </button>
            </div>
          </form>
        )}

        {list.isError && (
          <div className="page-error">
            {list.error instanceof Error ? list.error.message : "failed to load tokens"}
          </div>
        )}
        {!list.isError && rows.length === 0 && !list.isLoading && (
          <div className="page-empty">No tokens yet.</div>
        )}

        {rows.length > 0 && (
          <table className="tok-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Scopes</th>
                <th>Created</th>
                <th>Last used</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.id} data-testid={`token-row-${t.id}`}>
                  <td className="tok-name">{t.name}</td>
                  <td>
                    <span className="tok-chips">
                      {t.scopes.map((s) => (
                        <span key={s} className="tok-chip">
                          {s}
                        </span>
                      ))}
                    </span>
                  </td>
                  <td className="tok-time">{fmtNs(t.createdNs)}</td>
                  <td className="tok-time">{fmtNs(t.lastUsedNs)}</td>
                  <td className="tok-row-actions">
                    {confirmId === t.id ? (
                      <>
                        <button
                          className="tok-btn tok-btn--danger"
                          disabled={deleteMut.isPending}
                          onClick={async () => {
                            await deleteMut.mutateAsync(t.id);
                            setConfirmId(null);
                          }}
                          data-testid={`token-confirm-${t.id}`}
                        >
                          Confirm revoke
                        </button>
                        <button className="tok-btn" onClick={() => setConfirmId(null)}>
                          Cancel
                        </button>
                      </>
                    ) : (
                      <button
                        className="tok-icon-btn tok-icon-btn--danger"
                        title="revoke"
                        onClick={() => setConfirmId(t.id)}
                        data-testid={`token-revoke-${t.id}`}
                      >
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
