import { describe, it, expect } from "vitest";
import {
  createLiveClient,
  createIngestClient,
  createExperimentClient,
  createQueryClient,
} from "./clients";
import {
  ValueKind,
  SubscribeRequestSchema,
  LiveService,
  ExperimentService,
  QueryService,
  StartExperimentRequestSchema,
  QueryRangeRequestSchema,
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

  it("re-exports generated schemas and enums", () => {
    expect(ValueKind.F64).toBe(1);
    expect(SubscribeRequestSchema.typeName).toBe("gantry.v1.SubscribeRequest");
    expect(LiveService.typeName).toBe("gantry.v1.LiveService");
    expect(ExperimentService.typeName).toBe("gantry.v1.ExperimentService");
    expect(QueryService.typeName).toBe("gantry.v1.QueryService");
    expect(StartExperimentRequestSchema.typeName).toBe("gantry.v1.StartExperimentRequest");
    expect(QueryRangeRequestSchema.typeName).toBe("gantry.v1.QueryRangeRequest");
  });
});
