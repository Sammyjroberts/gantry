import { describe, it, expect } from "vitest";
import { createLiveClient, createIngestClient } from "./clients";
import { ValueKind, SubscribeRequestSchema, LiveService } from "./index";

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

  it("re-exports generated schemas and enums", () => {
    expect(ValueKind.F64).toBe(1);
    expect(SubscribeRequestSchema.typeName).toBe("gantry.v1.SubscribeRequest");
    expect(LiveService.typeName).toBe("gantry.v1.LiveService");
  });
});
