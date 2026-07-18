import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createSourceClient,
  type Source,
  type SourceStatus,
} from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import { apiClientOptions } from "../auth/authGate";
import { qk } from "./keys";

/**
 * Telemetry-source queries for the Hardware page's "Telemetry sources" card.
 * Mirrors the useWorkspaces/useTokens pattern (one client per call, react-query
 * cache + invalidation). The list carries the persisted rows AND the
 * supervisor's live status (state/detail/frames/reconnects), so it polls on a
 * modest interval to keep the status dots current.
 */

/** Status-dot refresh cadence — brisk enough that a connect/backoff flip is
 *  visible, slow enough not to spam the bench. */
export const SOURCE_POLL_MS = 2_000;

export interface SourcesResult {
  sources: Source[];
  /** Live status keyed by source id (may be missing briefly for a new row). */
  statusById: Map<string, SourceStatus>;
}

/** List sources + live status, polling so the status dots live-update. */
export function useSourceList() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  return useQuery({
    queryKey: qk.sources,
    queryFn: async ({ signal }): Promise<SourcesResult> => {
      const client = createSourceClient(baseUrl, apiClientOptions());
      const res = await client.listSources({}, { signal });
      const statusById = new Map<string, SourceStatus>();
      for (const st of res.statuses) statusById.set(st.id, st);
      // Stable order: by name then id (the server already sorts this way).
      const sources = [...res.sources];
      return { sources, statusById };
    },
    refetchInterval: SOURCE_POLL_MS,
    staleTime: 0,
  });
}

export interface UpsertSourceArgs {
  /** Empty id creates; the server generates the id + stamps timestamps. */
  id: string;
  type: string;
  name: string;
  url: string;
  mappingJson: string;
  enabled: boolean;
}

/**
 * Create/update a source. On success the sources list is invalidated so the new
 * row and its (soon-to-arrive) status appear; the server reconciles the
 * supervisor synchronously, so a toggle takes effect before the next poll.
 */
export function useUpsertSource() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (args: UpsertSourceArgs): Promise<Source> => {
      const client = createSourceClient(baseUrl, apiClientOptions());
      const res = await client.upsertSource({
        source: {
          id: args.id,
          type: args.type,
          name: args.name,
          url: args.url,
          mappingJson: args.mappingJson,
          enabled: args.enabled,
          createdNs: 0n,
          updatedNs: 0n,
        },
      });
      if (!res.source) throw new Error("upsert returned no source");
      return res.source;
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sources });
    },
  });
}

/** Delete a source by id. */
export function useDeleteSource() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<void> => {
      const client = createSourceClient(baseUrl, apiClientOptions());
      await client.deleteSource({ id });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sources });
    },
  });
}
