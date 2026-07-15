import { createClient, type Client } from "@connectrpc/connect";
import {
  createConnectTransport,
  type ConnectTransportOptions,
} from "@connectrpc/connect-web";
import { LiveService } from "./gen/gantry/v1/live_pb";
import { IngestService } from "./gen/gantry/v1/ingest_pb";
import { ExperimentService } from "./gen/gantry/v1/experiment_pb";
import { QueryService } from "./gen/gantry/v1/query_pb";
import { HardwareService } from "./gen/gantry/v1/hardware_pb";
import { WorkspaceService } from "./gen/gantry/v1/workspace_pb";

/** Typed client for the live telemetry (streaming) service. */
export type LiveClient = Client<typeof LiveService>;
/** Typed client for the ingest service. */
export type IngestClient = Client<typeof IngestService>;
/** Typed client for the experiment (test-run) service. */
export type ExperimentClient = Client<typeof ExperimentService>;
/** Typed client for the historical range-query service. */
export type QueryClient = Client<typeof QueryService>;
/** Typed client for the hardware (device identity + config) service. */
export type HardwareClient = Client<typeof HardwareService>;
/** Typed client for the workspace (saved bench layout) service. */
export type WorkspaceClient = Client<typeof WorkspaceService>;

/**
 * Extra transport options, minus `baseUrl` which is passed positionally.
 * Lets callers tweak fetch, interceptors, credentials, etc.
 */
export type ClientOptions = Partial<Omit<ConnectTransportOptions, "baseUrl">>;

/**
 * Create a typed LiveService client bound to `baseUrl`.
 *
 * `baseUrl` is the API origin (e.g. `window.location.origin` when Edge serves
 * the embedded UI, or `http://localhost:4780` in dev). Connect appends the
 * `/gantry.v1.LiveService/...` RPC path.
 */
export function createLiveClient(
  baseUrl: string,
  options: ClientOptions = {},
): LiveClient {
  return createClient(LiveService, createConnectTransport({ baseUrl, ...options }));
}

/** Create a typed IngestService client bound to `baseUrl`. */
export function createIngestClient(
  baseUrl: string,
  options: ClientOptions = {},
): IngestClient {
  return createClient(IngestService, createConnectTransport({ baseUrl, ...options }));
}

/**
 * Create a typed ExperimentService client bound to `baseUrl`.
 *
 * Same origin/base contract as {@link createLiveClient}: Connect appends the
 * `/gantry.v1.ExperimentService/...` RPC path. CSV export is a plain HTTP GET
 * (see `/export/experiments/{id}.csv`), not an RPC — build that URL directly.
 */
export function createExperimentClient(
  baseUrl: string,
  options: ClientOptions = {},
): ExperimentClient {
  return createClient(ExperimentService, createConnectTransport({ baseUrl, ...options }));
}

/**
 * Create a typed QueryService client bound to `baseUrl`.
 *
 * Same origin/base contract as {@link createLiveClient}: Connect appends the
 * `/gantry.v1.QueryService/...` RPC path. Serves the console's time-range
 * navigation (historical `QueryRange` reads over the stream's retention window).
 */
export function createQueryClient(
  baseUrl: string,
  options: ClientOptions = {},
): QueryClient {
  return createClient(QueryService, createConnectTransport({ baseUrl, ...options }));
}

/**
 * Create a typed HardwareService client bound to `baseUrl`.
 *
 * Same origin/base contract as {@link createLiveClient}: Connect appends the
 * `/gantry.v1.HardwareService/...` RPC path. Serves the console's hardware page
 * (device identity + display names) and the server-side homes for the 3D
 * visualization config and per-device panel defaults.
 */
export function createHardwareClient(
  baseUrl: string,
  options: ClientOptions = {},
): HardwareClient {
  return createClient(HardwareService, createConnectTransport({ baseUrl, ...options }));
}

/**
 * Create a typed WorkspaceService client bound to `baseUrl`.
 *
 * Same origin/base contract as {@link createLiveClient}: Connect appends the
 * `/gantry.v1.WorkspaceService/...` RPC path. Serves the console's saved bench
 * layouts (the panel grid) — List (name + timestamps only), Get (full
 * layout_json), Upsert (create when id is empty, else update), Delete.
 */
export function createWorkspaceClient(
  baseUrl: string,
  options: ClientOptions = {},
): WorkspaceClient {
  return createClient(WorkspaceService, createConnectTransport({ baseUrl, ...options }));
}
