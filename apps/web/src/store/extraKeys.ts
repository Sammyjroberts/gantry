/**
 * Extra subscription keys contributed by panels.
 *
 * Most panels declare their channels in their config (see panelBindings), so the
 * subscription union is derived from the layout. The 3D panel is the exception:
 * its pose bindings live in the device's server-side viz_config_json, not the
 * panel config, so it reports the channel keys it needs here (keyed by panel id)
 * and the LiveProvider folds them into the one workspace subscription. This is
 * the panelized form of the old App `onBoundChannelsChange` plumbing.
 */
import { useMemo } from "react";
import { create } from "zustand";

interface ExtraKeysState {
  byPanel: Record<string, string[]>;
  set: (panelId: string, keys: string[]) => void;
  clear: (panelId: string) => void;
}

export const useExtraKeysStore = create<ExtraKeysState>((set) => ({
  byPanel: {},
  set: (panelId, keys) =>
    set((s) => {
      const prev = s.byPanel[panelId];
      if (prev && prev.length === keys.length && prev.every((k, i) => k === keys[i])) {
        return {};
      }
      return { byPanel: { ...s.byPanel, [panelId]: keys } };
    }),
  clear: (panelId) =>
    set((s) => {
      if (!(panelId in s.byPanel)) return {};
      const next = { ...s.byPanel };
      delete next[panelId];
      return { byPanel: next };
    }),
}));

/** The deduped union of all panel-contributed extra keys. */
export function useExtraKeysUnion(): string[] {
  const byPanel = useExtraKeysStore((s) => s.byPanel);
  return useMemo(() => {
    const set = new Set<string>();
    for (const keys of Object.values(byPanel)) for (const k of keys) set.add(k);
    return [...set];
  }, [byPanel]);
}
