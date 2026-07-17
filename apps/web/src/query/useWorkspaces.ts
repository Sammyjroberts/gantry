import { useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createWorkspaceClient, type Workspace } from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import { apiClientOptions } from "../auth/authGate";
import { qk } from "./keys";

/** List all workspaces (name + timestamps only; layout_json is omitted). */
export function useWorkspaceList() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  return useQuery({
    queryKey: qk.workspaces,
    queryFn: async ({ signal }) => {
      const client = createWorkspaceClient(baseUrl, apiClientOptions());
      const res = await client.listWorkspaces({}, { signal });
      // Newest-updated first for the switcher.
      return [...res.workspaces].sort((a, b) => Number(b.updatedNs - a.updatedNs));
    },
    staleTime: 10_000,
  });
}

/** Fetch a single workspace's full document (incl. layout_json). */
export function useWorkspace(id: string | null) {
  const baseUrl = useMemo(resolveBaseUrl, []);
  return useQuery({
    queryKey: id ? qk.workspace(id) : ["workspace", "none"],
    enabled: !!id,
    queryFn: async ({ signal }): Promise<Workspace | null> => {
      const client = createWorkspaceClient(baseUrl, apiClientOptions());
      const res = await client.getWorkspace({ id: id! }, { signal });
      return res.workspace ?? null;
    },
    staleTime: 30_000,
  });
}

export interface UpsertArgs {
  /** Empty id creates; the server generates the id + stamps timestamps. */
  id: string;
  name: string;
  layoutJson: string;
}

/**
 * Create/update a workspace. On success the returned (authoritative) row is
 * pushed into the single-workspace cache and the list is invalidated so the
 * switcher and the loaded document converge.
 */
export function useUpsertWorkspace() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (args: UpsertArgs): Promise<Workspace> => {
      const client = createWorkspaceClient(baseUrl, apiClientOptions());
      const res = await client.upsertWorkspace({
        workspace: {
          id: args.id,
          name: args.name,
          layoutJson: args.layoutJson,
          createdNs: 0n,
          updatedNs: 0n,
        },
      });
      if (!res.workspace) throw new Error("upsert returned no workspace");
      return res.workspace;
    },
    onSuccess: (ws) => {
      qc.setQueryData(qk.workspace(ws.id), ws);
      void qc.invalidateQueries({ queryKey: qk.workspaces });
    },
  });
}

export function useDeleteWorkspace() {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<void> => {
      const client = createWorkspaceClient(baseUrl, apiClientOptions());
      await client.deleteWorkspace({ id });
    },
    onSuccess: (_v, id) => {
      qc.removeQueries({ queryKey: qk.workspace(id) });
      void qc.invalidateQueries({ queryKey: qk.workspaces });
    },
  });
}
