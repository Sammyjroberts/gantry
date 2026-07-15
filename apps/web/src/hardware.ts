/**
 * Hardware — pure helpers behind the device identity layer (HardwareService).
 *
 * A device is "configured" when it has a Hardware row: a display name plus two
 * opaque JSON config documents the console owns and the server stores verbatim.
 * Their schemas are versioned INSIDE the JSON (a `{v, ...}` envelope) so the
 * server never needs to understand them:
 *
 *   - viz_config_json     → { v: 1, bindings: PoseBindings }   (3D viewer)
 *   - panel_defaults_json → { v: 1, channels: string[] }       (sidebar defaults)
 *
 * This module is framework-free and unit-tested (hardware.test.ts): the
 * envelope encode/decode, the display-name fallback, and the ONE-TIME
 * localStorage→server migration for legacy pose bindings. No RPC or React here.
 */
import { defaultBindings, mergeBindings, type PoseBindings } from "./pose";
import type { Hardware } from "@gantry/api-client";

// ---- viz config envelope (3D bindings) ------------------------------------

/** Current viz_config_json envelope version. */
export const VIZ_CONFIG_VERSION = 1;

export interface VizConfigEnvelope {
  v: number;
  bindings: PoseBindings;
}

/** Encode pose bindings into the versioned viz_config_json string. */
export function encodeVizConfig(bindings: PoseBindings): string {
  const env: VizConfigEnvelope = { v: VIZ_CONFIG_VERSION, bindings };
  return JSON.stringify(env);
}

/**
 * Decode viz_config_json into pose bindings, tolerantly. Returns null when the
 * document is empty/absent (so a caller can tell "server has no config" from
 * "server has defaults") — a present-but-garbage document falls back to
 * defaults via {@link mergeBindings}. A bare (unversioned) bindings blob is
 * accepted too, for forward/backward tolerance.
 */
export function decodeVizConfig(json: string | undefined | null): PoseBindings | null {
  if (!json) return null;
  try {
    const parsed: unknown = JSON.parse(json);
    if (parsed && typeof parsed === "object" && "bindings" in (parsed as object)) {
      return mergeBindings((parsed as VizConfigEnvelope).bindings);
    }
    // Unversioned / legacy shape: treat the whole doc as a bindings blob.
    return mergeBindings(parsed);
  } catch {
    return null;
  }
}

// ---- panel defaults envelope (sidebar channel selection) ------------------

/** Current panel_defaults_json envelope version. */
export const PANEL_DEFAULTS_VERSION = 1;

export interface PanelDefaultsEnvelope {
  v: number;
  /** Selected channel keys (packet|name), see channel.ts. */
  channels: string[];
}

/** Encode a channel-key selection into the versioned panel_defaults_json string. */
export function encodePanelDefaults(channels: string[]): string {
  const env: PanelDefaultsEnvelope = { v: PANEL_DEFAULTS_VERSION, channels };
  return JSON.stringify(env);
}

/** Decode panel_defaults_json into a channel-key list (empty when absent/garbage). */
export function decodePanelDefaults(json: string | undefined | null): string[] {
  if (!json) return [];
  try {
    const parsed: unknown = JSON.parse(json);
    if (parsed && typeof parsed === "object" && Array.isArray((parsed as PanelDefaultsEnvelope).channels)) {
      return (parsed as PanelDefaultsEnvelope).channels.filter((c): c is string => typeof c === "string");
    }
    // Tolerate a bare array.
    if (Array.isArray(parsed)) return parsed.filter((c): c is string => typeof c === "string");
    return [];
  } catch {
    return [];
  }
}

// ---- display-name resolution ----------------------------------------------

/**
 * The human label for a device: its configured display_name when set, else the
 * raw device_id. This is the single fallback rule surfaced everywhere a device
 * id shows today (sidebar headers, chart labels).
 */
export function deviceDisplayName(deviceId: string, byId: Map<string, Hardware>): string {
  const name = byId.get(deviceId)?.displayName?.trim();
  return name && name.length > 0 ? name : deviceId;
}

// ---- one-time legacy migration (localStorage → server) --------------------

/**
 * The legacy localStorage key prefix that pose.ts used to persist bindings under
 * before they were re-homed to the server. Retained ONLY for the one-time
 * migration below; nothing writes here anymore.
 */
export const LEGACY_POSE_PREFIX = "gantry-pose-";

function legacyKey(device: string): string {
  return `${LEGACY_POSE_PREFIX}${device || "_"}`;
}

/**
 * Read (without removing) any legacy localStorage bindings for a device, or null
 * if none / storage unavailable. Used by the migration path: push to the server
 * first, then {@link clearLegacyBindings} once the push succeeds.
 */
export function readLegacyBindings(device: string): PoseBindings | null {
  if (typeof localStorage === "undefined") return null;
  try {
    const raw = localStorage.getItem(legacyKey(device));
    if (!raw) return null;
    return mergeBindings(JSON.parse(raw));
  } catch {
    return null;
  }
}

/** Remove the legacy localStorage entry for a device (after a successful push). */
export function clearLegacyBindings(device: string): void {
  if (typeof localStorage === "undefined") return;
  try {
    localStorage.removeItem(legacyKey(device));
  } catch {
    /* disabled storage — non-fatal */
  }
}

/** Fresh default bindings, re-exported so callers need not reach into pose.ts. */
export { defaultBindings };
