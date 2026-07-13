import { describe, it, expect, vi, afterEach } from "vitest";
import { renderHook, waitFor, cleanup } from "@testing-library/react";
import { TimeSeriesStore } from "@gantry/timeseries";
import { useLiveStream } from "./useLiveStream";
import { channelKey } from "./channel";
import { REPLAY_SECONDS } from "./config";

// Controls what the mocked LiveService.Subscribe yields per test.
let subscribeImpl: () => AsyncIterable<{ frames: unknown[] }>;

vi.mock("@gantry/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@gantry/api-client")>();
  return {
    ...actual,
    createLiveClient: () => ({
      subscribe: () => subscribeImpl(),
    }),
  };
});

/** Never-resolving promise so a stream "stays open" after its scripted yields. */
const stayOpen = () => new Promise<void>(() => {});

afterEach(() => cleanup());

function frame(packet: string, channel: string, f64: number) {
  return {
    channel,
    packet,
    deviceId: "rover-01",
    timestampNs: BigInt(Date.now()) * 1_000_000n,
    value: { kind: { case: "f64" as const, value: f64 } },
  };
}

describe("useLiveStream keepalive contract", () => {
  it("treats an empty SubscribeResponse (stream-open / heartbeat) as LIVE, not data", async () => {
    subscribeImpl = async function* () {
      yield { frames: [] }; // stream-open keepalive: zero frames
      await stayOpen();
    };
    const store = new TimeSeriesStore(100);
    const { result } = renderHook(() =>
      useLiveStream({
        baseUrl: "http://x",
        store,
        deviceId: "",
        channels: ["temp"],
        replaySeconds: REPLAY_SECONDS,
      }),
    );

    await waitFor(() => expect(result.current.conn).toBe("live"));
    // Keepalive carried no data.
    expect(store.channels()).toEqual([]);
    expect(result.current.fps).toBe(0);
  });
});

describe("useLiveStream (packet, name) keying", () => {
  it("keys frames by (packet, name) so packet-siblings do not collide", async () => {
    subscribeImpl = async function* () {
      yield { frames: [] };
      // Same param name "temp" from two different packets on one subscription.
      yield { frames: [frame("imu", "temp", 21.5), frame("power", "temp", 42)] };
      await stayOpen();
    };
    const store = new TimeSeriesStore(100);
    renderHook(() =>
      useLiveStream({
        baseUrl: "http://x",
        store,
        deviceId: "",
        channels: ["temp"],
        replaySeconds: REPLAY_SECONDS,
      }),
    );

    await waitFor(() =>
      expect(store.channels().sort()).toEqual(
        [channelKey("imu", "temp"), channelKey("power", "temp")].sort(),
      ),
    );
    expect(store.get(channelKey("imu", "temp"))!.latest()!.value).toBe(21.5);
    expect(store.get(channelKey("power", "temp"))!.latest()!.value).toBe(42);
  });
});
