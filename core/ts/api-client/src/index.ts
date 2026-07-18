// Re-export the generated protobuf-es v2 schemas/types (single source of truth
// lives in proto/; these files are buf codegen output under src/gen).
export * from "./gen/gantry/v1/telemetry_pb";
export * from "./gen/gantry/v1/live_pb";
export * from "./gen/gantry/v1/ingest_pb";
export * from "./gen/gantry/v1/experiment_pb";
export * from "./gen/gantry/v1/query_pb";
export * from "./gen/gantry/v1/hardware_pb";
export * from "./gen/gantry/v1/workspace_pb";
export * from "./gen/gantry/v1/source_pb";
export * from "./gen/gantry/v1/auth_pb";

// Typed client factories over the connect-es v2 stack (incl. bearerInterceptor).
export * from "./clients";

// Connect error plumbing, re-exported so console code can classify RPC failures
// (e.g. NotFound vs. transient, Unauthenticated for the auth prompt) without a
// direct @connectrpc/connect dependency.
export { Code, ConnectError } from "@connectrpc/connect";
export type { Interceptor } from "@connectrpc/connect";
