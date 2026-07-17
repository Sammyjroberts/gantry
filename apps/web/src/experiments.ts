import type { Experiment } from "@gantry/api-client";
import { withToken } from "./auth/token";

/**
 * Pure experiment helpers — name/duration formatting, running-region math, and
 * CSV-export URL construction. No RPC or React here so the tricky bits (a
 * running experiment's region extending to `now`, buffer clamping of the export
 * window) are unit tested directly (see experiments.test.ts).
 */

/** An experiment is running while its end timestamp is zero (proto contract). */
export function isRunning(exp: Experiment): boolean {
  return exp.endNs === 0n;
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/**
 * Default experiment name like `test-2026-07-12-17-42` (local time, minute
 * precision). Deterministic given a Date, so it is testable.
 */
export function defaultExperimentName(now: Date): string {
  const y = now.getFullYear();
  const mo = pad2(now.getMonth() + 1);
  const d = pad2(now.getDate());
  const h = pad2(now.getHours());
  const mi = pad2(now.getMinutes());
  return `test-${y}-${mo}-${d}-${h}-${mi}`;
}

/**
 * Duration of an experiment in whole seconds. For a running experiment the
 * end is `nowNs` (wall clock); for a finished one it is the recorded `end_ns`.
 * Never negative.
 */
export function durationSec(exp: Experiment, nowNs: bigint): number {
  const end = isRunning(exp) ? nowNs : exp.endNs;
  const ns = end - exp.startNs;
  if (ns <= 0n) return 0;
  return Number(ns / 1_000_000_000n);
}

/**
 * Format a second count as `M:SS` (or `H:MM:SS` past an hour) for the elapsed
 * timer and the per-row duration badge.
 */
export function formatDuration(totalSec: number): string {
  const s = Math.max(0, Math.floor(totalSec));
  const hrs = Math.floor(s / 3600);
  const mins = Math.floor((s % 3600) / 60);
  const secs = s % 60;
  if (hrs > 0) return `${hrs}:${pad2(mins)}:${pad2(secs)}`;
  return `${mins}:${pad2(secs)}`;
}

/** Newest-first by start time (ties broken by creation time), stable-ish. */
export function sortNewestFirst(experiments: Experiment[]): Experiment[] {
  return [...experiments].sort((a, b) => {
    if (a.startNs !== b.startNs) return a.startNs > b.startNs ? -1 : 1;
    if (a.createdNs !== b.createdNs) return a.createdNs > b.createdNs ? -1 : 1;
    return 0;
  });
}

/** A shaded time band drawn on the charts for one experiment. */
export interface ExperimentRegion {
  id: string;
  name: string;
  /** Band left edge, epoch seconds. */
  startSec: number;
  /** Band right edge, epoch seconds. Extends to `now` while running. */
  endSec: number;
  running: boolean;
}

/**
 * Project experiments onto chart-space time bands. A RUNNING experiment's band
 * extends to `nowSec` (the live edge) so it grows with the stream; a finished
 * one ends at its recorded `end_ns`. Returned oldest-first for stable draw order.
 */
export function experimentRegions(
  experiments: Experiment[],
  nowSec: number,
): ExperimentRegion[] {
  return experiments
    .map((exp): ExperimentRegion => {
      const running = isRunning(exp);
      const startSec = Number(exp.startNs) / 1e9;
      const endSec = running ? nowSec : Number(exp.endNs) / 1e9;
      return { id: exp.id, name: exp.name, startSec, endSec, running };
    })
    .sort((a, b) => a.startSec - b.startSec);
}

/**
 * Build the CSV-export href for an experiment. Plain same-origin HTTP GET (not
 * RPC): `GET /export/experiments/{id}.csv?channels=a,b&format=long|wide`.
 * `channels` empty ⇒ omitted ⇒ server exports all. `format` omitted ⇒ server
 * default (long).
 */
export function experimentCsvHref(
  baseUrl: string,
  id: string,
  channels: string[] = [],
  format?: "long" | "wide",
): string {
  const base = baseUrl.replace(/\/+$/, "");
  const url = new URL(`${base}/export/experiments/${encodeURIComponent(id)}.csv`);
  if (channels.length > 0) url.searchParams.set("channels", channels.join(","));
  if (format) url.searchParams.set("format", format);
  // This is an `<a download>` href (can't set an Authorization header), and
  // /export/ GETs are one of the two routes the server accepts a query token on.
  // No token ⇒ unchanged (localhost). See auth/token.ts::withToken.
  return withToken(url.toString());
}
