import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Source, SourceStatus } from "@gantry/api-client";
import { SourcesCard } from "./SourcesCard";

function mkSource(over: Partial<Source>): Source {
  return {
    $typeName: "gantry.v1.Source",
    id: "",
    type: "foxglove",
    name: "",
    url: "ws://127.0.0.1:8765",
    mappingJson: `{"profile":"lerobot"}`,
    enabled: false,
    createdNs: 0n,
    updatedNs: 0n,
    ...over,
  } as Source;
}

function mkStatus(over: Partial<SourceStatus>): SourceStatus {
  return {
    $typeName: "gantry.v1.SourceStatus",
    id: "",
    state: "disabled",
    detail: "",
    lastFrameNs: 0n,
    framesIngested: 0n,
    reconnects: 0n,
    ...over,
  } as SourceStatus;
}

// Mutable server-side fixtures the mocked client reads from / records into.
let rows: Source[] = [];
let statuses: SourceStatus[] = [];
const client = {
  listSources: vi.fn(async () => ({ sources: rows, statuses })),
  upsertSource: vi.fn(async (req: { source: Partial<Source> }) => {
    const id = req.source.id || "gen-1";
    const merged = mkSource({ ...req.source, id });
    rows = [...rows.filter((r) => r.id !== id), merged];
    return { source: merged };
  }),
  deleteSource: vi.fn(async (req: { id: string }) => {
    rows = rows.filter((r) => r.id !== req.id);
    return {};
  }),
};

vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return { ...actual, createSourceClient: () => client };
});

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <SourcesCard />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  rows = [];
  statuses = [];
  vi.clearAllMocks();
});
afterEach(() => cleanup());

describe("SourcesCard", () => {
  it("adds a source via the form (lerobot profile, enabled)", async () => {
    renderCard();

    fireEvent.click(screen.getByTestId("source-add"));
    fireEvent.change(screen.getByTestId("source-name"), { target: { value: "lab bench" } });
    fireEvent.change(screen.getByTestId("source-url"), { target: { value: "ws://host:9000" } });
    fireEvent.click(screen.getByTestId("source-create"));

    await waitFor(() =>
      expect(client.upsertSource).toHaveBeenCalledWith({
        source: expect.objectContaining({
          id: "",
          type: "foxglove",
          name: "lab bench",
          url: "ws://host:9000",
          mappingJson: `{"profile":"lerobot"}`,
          enabled: true,
        }),
      }),
    );
  });

  it("shows a custom mapping textarea and rejects invalid JSON before the RPC", async () => {
    renderCard();
    fireEvent.click(screen.getByTestId("source-add"));
    fireEvent.change(screen.getByTestId("source-profile"), { target: { value: "custom" } });

    const mapping = screen.getByTestId("source-mapping");
    fireEvent.change(mapping, { target: { value: "{not json" } });
    fireEvent.click(screen.getByTestId("source-create"));

    expect(await screen.findByText("mapping must be valid JSON")).toBeTruthy();
    expect(client.upsertSource).not.toHaveBeenCalled();
  });

  it("toggling the enabled checkbox re-upserts the whole row", async () => {
    rows = [mkSource({ id: "s1", name: "rig", url: "ws://h:1", enabled: false })];
    statuses = [mkStatus({ id: "s1", state: "disabled" })];
    renderCard();

    const cb = await screen.findByTestId("source-enabled-s1");
    expect((cb as HTMLInputElement).checked).toBe(false);
    fireEvent.click(cb);

    await waitFor(() =>
      expect(client.upsertSource).toHaveBeenCalledWith({
        source: expect.objectContaining({ id: "s1", url: "ws://h:1", enabled: true }),
      }),
    );
  });

  it("renders the live status dot + frames counter from status", async () => {
    rows = [mkSource({ id: "s2", name: "bench", url: "ws://h:2", enabled: true })];
    statuses = [mkStatus({ id: "s2", state: "connected", framesIngested: 1234n })];
    renderCard();

    const dot = await screen.findByTestId("source-dot");
    expect(dot.getAttribute("data-state")).toBe("connected");
    expect(screen.getByTestId("source-frames-s2").textContent).toContain("1,234");
  });

  it("deletes a source after confirm", async () => {
    rows = [mkSource({ id: "s3", name: "old", url: "ws://h:3" })];
    statuses = [mkStatus({ id: "s3", state: "disabled" })];
    renderCard();

    fireEvent.click(await screen.findByTestId("source-delete-s3"));
    fireEvent.click(screen.getByTestId("source-confirm-s3"));

    await waitFor(() => expect(client.deleteSource).toHaveBeenCalledWith({ id: "s3" }));
  });
});
