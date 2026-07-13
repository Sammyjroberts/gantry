import { createClient, type Client } from "@connectrpc/connect";
import {
  createConnectTransport,
  type ConnectTransportOptions,
} from "@connectrpc/connect-web";
import { LiveService } from "./gen/gantry/v1/live_pb";
import { IngestService } from "./gen/gantry/v1/ingest_pb";
import { ExperimentService } from "./gen/gantry/v1/experiment_pb";

/** Typed client for the live telemetry (streaming) service. */
export type LiveClient = Client<typeof LiveService>;
/** Typed client for the ingest service. */
export type IngestClient = Client<typeof IngestService>;
/** Typed client for the experiment (test-run) service. */
export type ExperimentClient = Client<typeof ExperimentService>;

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
