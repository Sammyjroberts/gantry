import { describe, it, expect } from "vitest";
import {
  createLiveClient,
  createIngestClient,
  createExperimentClient,
  createQueryClient,
  createHardwareClient,
  createSourceClient,
  createTokenClient,
  bearerInterceptor,
} from "./clients";
import {
  ValueKind,
  SubscribeRequestSchema,
  LiveService,
  ExperimentService,
  QueryService,
  HardwareService,
  SourceService,
  StartExperimentRequestSchema,
  QueryRangeRequestSchema,
  UpsertHardwareRequestSchema,
} from "./index";

describe("api-client factories", () => {
  it("creates a LiveService client exposing the RPC methods", () => {
    const client = createLiveClient("http://localhost:4780");
    expect(typeof client.subscribe).toBe("function");
    expect(typeof client.listChannels).toBe("function");
  });

  it("creates an IngestService client exposing the RPC methods", () => {
    const client = createIngestClient("http://localhost:4780");
    expect(typeof client.publishBatch).toBe("function");
    expect(typeof client.registerChannels).toBe("function");
  });

  it("creates an ExperimentService client exposing the RPC methods", () => {
    const client = createExperimentClient("http://localhost:4780");
    expect(typeof client.startExperiment).toBe("function");
    expect(typeof client.stopExperiment).toBe("function");
    expect(typeof client.listExperiments).toBe("function");
    expect(typeof client.updateExperiment).toBe("function");
    expect(typeof client.deleteExperiment).toBe("function");
  });

  it("creates a QueryService client exposing the RPC methods", () => {
    const client = createQueryClient("http://localhost:4780");
    expect(typeof client.queryRange).toBe("function");
  });

  it("creates a HardwareService client exposing the RPC methods", () => {
    const client = createHardwareClient("http://localhost:4780");
    expect(typeof client.listHardware).toBe("function");
    expect(typeof client.getHardware).toBe("function");
    expect(typeof client.upsertHardware).toBe("function");
    expect(typeof client.deleteHardware).toBe("function");
  });

  it("creates a SourceService client exposing the RPC methods", () => {
    const client = createSourceClient("http://localhost:4780");
    expect(typeof client.listSources).toBe("function");
    expect(typeof client.upsertSource).toBe("function");
    expect(typeof client.deleteSource).toBe("function");
  });

  it("creates a TokenService client exposing the RPC methods", () => {
    const client = createTokenClient("http://localhost:4780");
    expect(typeof client.listTokens).toBe("function");
    expect(typeof client.createToken).toBe("function");
    expect(typeof client.deleteToken).toBe("function");
  });

  it("re-exports generated schemas and enums", () => {
    expect(ValueKind.F64).toBe(1);
    expect(SubscribeRequestSchema.typeName).toBe("gantry.v1.SubscribeRequest");
    expect(LiveService.typeName).toBe("gantry.v1.LiveService");
    expect(ExperimentService.typeName).toBe("gantry.v1.ExperimentService");
    expect(QueryService.typeName).toBe("gantry.v1.QueryService");
    expect(HardwareService.typeName).toBe("gantry.v1.HardwareService");
    expect(SourceService.typeName).toBe("gantry.v1.SourceService");
    expect(StartExperimentRequestSchema.typeName).toBe("gantry.v1.StartExperimentRequest");
    expect(QueryRangeRequestSchema.typeName).toBe("gantry.v1.QueryRangeRequest");
    expect(UpsertHardwareRequestSchema.typeName).toBe("gantry.v1.UpsertHardwareRequest");
  });
});

describe("bearerInterceptor", () => {
  // Minimal fakes: an interceptor is (next) => (req) => next(req); we only need
  // req.header (a Headers) and a next that echoes so we can inspect the header.
  const call = async (token: string | null | undefined) => {
    const req = { header: new Headers() };
    const next = async (r: typeof req) => r;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    await bearerInterceptor(() => token)(next as any)(req as any);
    return req.header;
  };

  it("attaches the Authorization header when a token is present", async () => {
    const header = await call("gtk_deadbeef_secret");
    expect(header.get("Authorization")).toBe("Bearer gtk_deadbeef_secret");
  });

  it("omits the header when the token is null/undefined/empty", async () => {
    expect((await call(null)).has("Authorization")).toBe(false);
    expect((await call(undefined)).has("Authorization")).toBe(false);
    expect((await call("")).has("Authorization")).toBe(false);
  });

  it("reads the token freshly on each request (getter, not captured)", async () => {
    let token = "gtk_first";
    const req1 = { header: new Headers() };
    const next = async (r: { header: Headers }) => r;
    const interceptor = bearerInterceptor(() => token);
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    await interceptor(next as any)(req1 as any);
    expect(req1.header.get("Authorization")).toBe("Bearer gtk_first");
    token = "gtk_second";
    const req2 = { header: new Headers() };
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    await interceptor(next as any)(req2 as any);
    expect(req2.header.get("Authorization")).toBe("Bearer gtk_second");
  });
});
