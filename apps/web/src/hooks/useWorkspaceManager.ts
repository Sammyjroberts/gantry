import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createWorkspaceClient, type Workspace } from "@gantry/api-client";
import { resolveBaseUrl } from "../config";
import { apiClientOptions } from "../auth/authGate";
import {
  useWorkspaceList,
  useUpsertWorkspace,
  useDeleteWorkspace,
} from "../query/useWorkspaces";
import { useWorkspaceStore, readLastWorkspaceId, writeLastWorkspaceId } from "../store/workspaceStore";
import {
  parseLayout,
  serializeLayout,
  seedDefaultLayout,
  type Panel,
  type SeedChannel,
} from "../workspace/layout";
import { createDebounced, AUTOSAVE_DELAY_MS, type Debounced } from "../workspace/autosave";
import { isPlottable } from "../valueKind";
import type { CatalogResult } from "../query/useCatalog";

export interface WorkspaceManager {
  workspaces: Workspace[];
  currentId: string | null;
  name: string;
  dirty: boolean;
  saving: boolean;
  ready: boolean;
  switchTo: (id: string) => Promise<void>;
  create: (name?: string, panels?: Panel[]) => Promise<string | null>;
  rename: (name: string) => void;
  remove: (id: string) => Promise<void>;
  duplicate: () => Promise<string | null>;
  exportCurrent: () => void;
  importLayout: (file: File) => Promise<void>;
}

/** First few plottable catalogue channels, for seeding a fresh default. */
function seedChannelsFromCatalog(catalog: CatalogResult): SeedChannel[] {
  const out: SeedChannel[] = [];
  for (const d of catalog.devices) {
    for (const c of d.channels) {
      if (!isPlottable(c.kind)) continue;
      out.push({ deviceId: d.deviceId, packet: c.packet, channel: c.name });
      if (out.length >= 6) return out;
    }
  }
  return out;
}

export function useWorkspaceManager(catalog: CatalogResult): WorkspaceManager {
  const baseUrl = useMemo(resolveBaseUrl, []);
  const client = useMemo(() => createWorkspaceClient(baseUrl, apiClientOptions()), [baseUrl]);
  const listQuery = useWorkspaceList();
  const upsert = useUpsertWorkspace();
  const del = useDeleteWorkspace();

  const currentId = useWorkspaceStore((s) => s.currentId);
  const name = useWorkspaceStore((s) => s.name);
  const dirty = useWorkspaceStore((s) => s.dirty);
  const panels = useWorkspaceStore((s) => s.panels);
  const loadWorkspace = useWorkspaceStore((s) => s.loadWorkspace);
  const markSaved = useWorkspaceStore((s) => s.markSaved);
  const setName = useWorkspaceStore((s) => s.setName);

  const [ready, setReady] = useState(false);
  const bootstrapped = useRef(false);
  const catalogReady = !catalog.isLoading;

  const workspaces = listQuery.data ?? [];

  // ---- imperative load of a workspace's full document into the store ----
  const loadInto = useCallback(
    async (id: string) => {
      const res = await client.getWorkspace({ id });
      const ws = res.workspace;
      if (!ws) return;
      loadWorkspace({ id: ws.id, name: ws.name, panels: parseLayout(ws.layoutJson) });
      writeLastWorkspaceId(ws.id);
    },
    [client, loadWorkspace],
  );

  const create = useCallback(
    async (wsName = "default", panels: Panel[] = []): Promise<string | null> => {
      try {
        const ws = await upsert.mutateAsync({
          id: "",
          name: wsName,
          layoutJson: serializeLayout(panels),
        });
        loadWorkspace({ id: ws.id, name: ws.name, panels: parseLayout(ws.layoutJson) });
        writeLastWorkspaceId(ws.id);
        return ws.id;
      } catch {
        return null;
      }
    },
    [upsert, loadWorkspace],
  );

  // ---- bootstrap: open last / first, else seed a default ----
  useEffect(() => {
    if (bootstrapped.current) return;
    if (listQuery.isLoading || !catalogReady) return;
    bootstrapped.current = true;
    void (async () => {
      const list = listQuery.data ?? [];
      const lastId = readLastWorkspaceId();
      const target =
        (lastId && list.find((w) => w.id === lastId)?.id) || list[0]?.id || null;
      if (target) {
        await loadInto(target);
      } else {
        await create("default", seedDefaultLayout(seedChannelsFromCatalog(catalog)));
      }
      setReady(true);
    })();
  }, [listQuery.isLoading, listQuery.data, catalogReady, catalog, create, loadInto]);

  // ---- autosave: debounced Upsert on dirty edits ----
  const saverRef = useRef<Debounced<[]> | null>(null);
  if (saverRef.current === null) {
    saverRef.current = createDebounced(() => {
      const s = useWorkspaceStore.getState();
      if (!s.currentId) return;
      void upsert
        .mutateAsync({ id: s.currentId, name: s.name, layoutJson: serializeLayout(s.panels) })
        .then((ws) => markSaved(ws.id))
        .catch(() => {
          /* leave dirty so a later edit retries */
        });
    }, AUTOSAVE_DELAY_MS);
  }
  useEffect(() => {
    if (dirty && currentId) saverRef.current!.call();
  }, [dirty, currentId, name, panels]);
  // Flush on unmount so a pending save isn't lost on navigation.
  useEffect(() => () => saverRef.current?.flush(), []);

  const switchTo = useCallback(
    async (id: string) => {
      saverRef.current?.flush();
      await loadInto(id);
    },
    [loadInto],
  );

  const rename = useCallback((n: string) => setName(n), [setName]);

  const remove = useCallback(
    async (id: string) => {
      await del.mutateAsync(id);
      if (id === useWorkspaceStore.getState().currentId) {
        const remaining = (listQuery.data ?? []).filter((w) => w.id !== id);
        if (remaining[0]) await loadInto(remaining[0].id);
        else await create("default", seedDefaultLayout(seedChannelsFromCatalog(catalog)));
      }
    },
    [del, listQuery.data, loadInto, create, catalog],
  );

  const duplicate = useCallback(async (): Promise<string | null> => {
    const s = useWorkspaceStore.getState();
    return create(`${s.name || "workspace"} copy`, s.panels);
  }, [create]);

  const exportCurrent = useCallback(() => {
    const s = useWorkspaceStore.getState();
    const doc = { v: 1, name: s.name, panels: s.panels };
    const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${(s.name || "workspace").replace(/[^\w.-]+/g, "_")}.json`;
    a.click();
    URL.revokeObjectURL(url);
  }, []);

  const importLayout = useCallback(
    async (file: File) => {
      const text = await file.text();
      let doc: { name?: unknown; panels?: unknown } = {};
      try {
        doc = JSON.parse(text);
      } catch {
        return;
      }
      const panels = parseLayout(JSON.stringify({ v: 1, panels: doc.panels ?? [] }));
      const wsName = typeof doc.name === "string" && doc.name ? `${doc.name} (imported)` : "imported";
      await create(wsName, panels);
    },
    [create],
  );

  return {
    workspaces,
    currentId,
    name,
    dirty,
    saving: upsert.isPending,
    ready,
    switchTo,
    create,
    rename,
    remove,
    duplicate,
    exportCurrent,
    importLayout,
  };
}
