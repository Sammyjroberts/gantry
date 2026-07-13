import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  createExperimentClient,
  type Experiment,
  type ExperimentClient,
} from "@gantry/api-client";
import { isRunning, sortNewestFirst } from "./experiments";

export interface UseExperimentsArgs {
  baseUrl: string;
  /** Device filter for ListExperiments; "" = bench-wide (all devices). */
  deviceId: string;
  /** Background refresh cadence in ms (default 5s). Set 0 to disable polling. */
  pollMs?: number;
}

export interface StartArgs {
  name: string;
  notes?: string;
  deviceId?: string;
  /** 0 = now (server stamps it). */
  startNs?: bigint;
}

export interface UseExperimentsResult {
  experiments: Experiment[];
  /** Subset still running (end_ns == 0), newest-first. */
  running: Experiment[];
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
  start: (args: StartArgs) => Promise<Experiment | null>;
  stop: (id: string, endNs?: bigint) => Promise<void>;
  update: (id: string, name: string, notes: string) => Promise<void>;
  remove: (id: string) => Promise<void>;
}

/**
 * Experiment (test-run) store bound to ExperimentService. Owns the list, a
 * periodic refresh, and the start/stop/update/delete mutations. Every mutation
 * merges the server's returned Experiment locally for an immediate UI update and
 * then re-lists so concurrent bench activity (e.g. a run started elsewhere)
 * converges. The list is kept newest-first (see {@link sortNewestFirst}).
 *
 * Mirrors useLiveStream's transport shape: one client per baseUrl, an
 * AbortController scoping in-flight reads to the effect lifetime, errors surfaced
 * as strings. Mutations are user-initiated promises the caller can await.
 */
export function useExperiments(args: UseExperimentsArgs): UseExperimentsResult {
  const { baseUrl, deviceId, pollMs = 5000 } = args;

  const [experiments, setExperiments] = useState<Experiment[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const clientRef = useRef<ExperimentClient | null>(null);
  if (clientRef.current === null) clientRef.current = createExperimentClient(baseUrl);
  // Rebind the client when baseUrl changes (rare; keeps the ref honest).
  const baseUrlRef = useRef(baseUrl);
  if (baseUrlRef.current !== baseUrl) {
    baseUrlRef.current = baseUrl;
    clientRef.current = createExperimentClient(baseUrl);
  }

  // Merge one server-authoritative experiment into the list (upsert by id).
  const upsert = useCallback((exp: Experiment) => {
    setExperiments((prev) => {
      const next = prev.filter((e) => e.id !== exp.id);
      next.push(exp);
      return sortNewestFirst(next);
    });
  }, []);

  const refresh = useCallback(async (): Promise<void> => {
    const client = clientRef.current!;
    try {
      const res = await client.listExperiments({ deviceId });
      setExperiments(sortNewestFirst(res.experiments));
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [deviceId]);

  // Initial load + background poll. Skipped entirely when pollMs === 0 (still
  // does the one initial load).
  useEffect(() => {
    let stopped = false;
    void refresh();
    if (pollMs <= 0) return;
    const id = setInterval(() => {
      if (!stopped) void refresh();
    }, pollMs);
    return () => {
      stopped = true;
      clearInterval(id);
    };
  }, [refresh, pollMs]);

  const start = useCallback(
    async (a: StartArgs): Promise<Experiment | null> => {
      const client = clientRef.current!;
      try {
        const res = await client.startExperiment({
          name: a.name,
          notes: a.notes ?? "",
          deviceId: a.deviceId ?? deviceId,
          startNs: a.startNs ?? 0n,
        });
        setError(null);
        if (res.experiment) {
          upsert(res.experiment);
          return res.experiment;
        }
        return null;
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return null;
      }
    },
    [deviceId, upsert],
  );

  const stop = useCallback(
    async (id: string, endNs: bigint = 0n): Promise<void> => {
      const client = clientRef.current!;
      try {
        const res = await client.stopExperiment({ id, endNs });
        setError(null);
        if (res.experiment) upsert(res.experiment);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [upsert],
  );

  const update = useCallback(
    async (id: string, name: string, notes: string): Promise<void> => {
      const client = clientRef.current!;
      try {
        const res = await client.updateExperiment({ id, name, notes });
        setError(null);
        if (res.experiment) upsert(res.experiment);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [upsert],
  );

  const remove = useCallback(
    async (id: string): Promise<void> => {
      const client = clientRef.current!;
      try {
        await client.deleteExperiment({ id });
        setExperiments((prev) => prev.filter((e) => e.id !== id));
        setError(null);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [],
  );

  const running = useMemo(() => experiments.filter(isRunning), [experiments]);

  return { experiments, running, loading, error, refresh, start, stop, update, remove };
}
