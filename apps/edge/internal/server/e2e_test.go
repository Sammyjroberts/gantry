package server_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/Sammyjroberts/gantry/apps/edge/internal/server"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"golang.org/x/net/http2"
)

// h2cClient returns an HTTP/2-over-cleartext client. This matches how real gRPC
// clients and the browser talk to the h2c Edge server: a single connection
// multiplexes the long-lived Subscribe stream and unary PublishBatch calls (an
// HTTP/1.1 client would serialize them and deadlock).
func h2cClient() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// startEdge starts a full Edge server on a random free port with a temp
// JetStream dir and returns its base URL plus a cleanup func.
func startEdge(t *testing.T) string {
	t.Helper()
	app, err := server.New(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	})
	return "http://" + ln.Addr().String()
}

func f64Frame(ch string, ts int64, v float64) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: ch, TimestampNs: uint64(ts), Value: &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: v}}}
}

func f64FrameP(packet, ch string, ts int64, v float64) *gantryv1.Frame {
	f := f64Frame(ch, ts, v)
	f.Packet = packet
	return f
}

func TestEdgeEndToEnd(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	liveClient := gantryv1connect.NewLiveServiceClient(httpClient, baseURL)

	ctx := context.Background()
	const device = "rover-1"

	const packet = "drive"

	// 1. RegisterChannels (explicit metadata).
	_, err := ingestClient.RegisterChannels(ctx, connect.NewRequest(&gantryv1.RegisterChannelsRequest{
		DeviceId: device,
		Channels: []*gantryv1.ChannelInfo{
			{Name: "drive.speed", Packet: packet, Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "m/s", Description: "ground speed"},
		},
	}))
	if err != nil {
		t.Fatalf("RegisterChannels: %v", err)
	}

	// 2. PublishBatch several batches; assert acked_sequence echoes the per-device
	//    batch sequence, proving durable acceptance. Frames carry a packet and
	//    deliberately leave Frame.device_id empty (batch is authoritative).
	now := time.Now().UnixNano()
	const preBatches = 3
	for seq := uint64(1); seq <= preBatches; seq++ {
		batch := &gantryv1.FrameBatch{
			DeviceId: device,
			Sequence: seq,
			Frames: []*gantryv1.Frame{
				f64FrameP(packet, "drive.speed", now+int64(seq)*1000, float64(seq)),
				f64FrameP(packet, "drive.temp_c", now+int64(seq)*1000+1, 20+float64(seq)), // auto-registered
			},
		}
		resp, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{Batch: batch}))
		if err != nil {
			t.Fatalf("PublishBatch seq %d: %v", seq, err)
		}
		if resp.Msg.AckedSequence != seq {
			t.Fatalf("acked_sequence = %d, want %d", resp.Msg.AckedSequence, seq)
		}
	}

	// 3. Subscribe with replay_seconds > 0: the 3 already-published batches must
	//    replay, then a freshly published batch must arrive live.
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := liveClient.Subscribe(subCtx, connect.NewRequest(&gantryv1.SubscribeRequest{
		DeviceId:      device,
		ReplaySeconds: 60,
	}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Collect frames off the stream in the background, keyed by a marker value so
	// we can deterministically wait for replay and for the live frame without
	// sleeping. Speed values 1..3 are the replayed batches; 99 is the live one.
	type result struct {
		seen       map[float64]bool
		gotDevice  string
		gotPacket  string
		firstEmpty bool // the first response carried zero frames (stream-open contract)
		sawData    bool
		err        error
	}
	done := make(chan result, 1)
	go func() {
		seen := map[float64]bool{}
		r := result{seen: seen}
		first := true
		for stream.Receive() {
			frames := stream.Msg().Frames
			if first {
				r.firstEmpty = len(frames) == 0
				first = false
			}
			if len(frames) > 0 {
				r.sawData = true
			}
			for _, f := range frames {
				if f.Channel == "drive.speed" {
					seen[f.GetValue().GetF64()] = true
					// Server must stamp device_id and preserve packet on outbound frames.
					r.gotDevice = f.GetDeviceId()
					r.gotPacket = f.GetPacket()
				}
			}
			if seen[1] && seen[2] && seen[3] && seen[99] {
				done <- r
				return
			}
		}
		r.err = stream.Err()
		done <- r
	}()

	// Publish the live batch. The collector requires all four markers (1..3
	// replayed, 99 live), so ordering is exercised without a sleep: even if the
	// live frame lands before replay drains, it is delivered in stream order.
	liveBatch := &gantryv1.FrameBatch{
		DeviceId: device,
		Sequence: 4,
		Frames:   []*gantryv1.Frame{f64FrameP(packet, "drive.speed", time.Now().UnixNano(), 99)},
	}
	if _, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{Batch: liveBatch})); err != nil {
		t.Fatalf("live PublishBatch: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("stream error: %v", r.err)
		}
		for _, want := range []float64{1, 2, 3, 99} {
			if !r.seen[want] {
				t.Fatalf("missing frame with speed=%v; seen=%v", want, r.seen)
			}
		}
		// Stream-open contract: the first response was empty (a keepalive), and
		// data followed it.
		if !r.firstEmpty {
			t.Errorf("first SubscribeResponse was not empty (stream-open contract)")
		}
		if !r.sawData {
			t.Errorf("never observed a data-bearing response")
		}
		// device_id stamped by server, packet preserved end-to-end.
		if r.gotDevice != device {
			t.Errorf("outbound frame device_id = %q, want %q", r.gotDevice, device)
		}
		if r.gotPacket != packet {
			t.Errorf("outbound frame packet = %q, want %q", r.gotPacket, packet)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for replayed + live frames")
	}
	cancel()

	// 4. ListChannels returns registered + auto-registered channels.
	lc, err := liveClient.ListChannels(ctx, connect.NewRequest(&gantryv1.ListChannelsRequest{DeviceId: device}))
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(lc.Msg.Devices) != 1 {
		t.Fatalf("want 1 device, got %d", len(lc.Msg.Devices))
	}
	got := map[string]gantryv1.ValueKind{}
	gotPacket := map[string]string{}
	for _, ci := range lc.Msg.Devices[0].Channels {
		got[ci.Name] = ci.Kind
		gotPacket[ci.Name] = ci.Packet
	}
	if got["drive.speed"] != gantryv1.ValueKind_VALUE_KIND_F64 {
		t.Errorf("drive.speed missing/wrong kind: %v", got)
	}
	if got["drive.temp_c"] != gantryv1.ValueKind_VALUE_KIND_F64 {
		t.Errorf("auto-registered drive.temp_c missing/wrong kind: %v", got)
	}
	// Packet must round-trip on ListChannels for both explicit and auto-registered channels.
	if gotPacket["drive.speed"] != packet {
		t.Errorf("drive.speed packet = %q, want %q", gotPacket["drive.speed"], packet)
	}
	if gotPacket["drive.temp_c"] != packet {
		t.Errorf("auto-registered drive.temp_c packet = %q, want %q", gotPacket["drive.temp_c"], packet)
	}
}

// TestSubscribeStreamOpenContract asserts the stream-open contract from
// live.proto: the FIRST SubscribeResponse carries zero frames and arrives before
// any data — even when replayable history exists (so the empty response is a
// genuine open signal, not just an artifact of there being no data yet).
func TestSubscribeStreamOpenContract(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	liveClient := gantryv1connect.NewLiveServiceClient(httpClient, baseURL)
	ctx := context.Background()
	const device = "opener-1"

	// Pre-publish so a replay subscription has data waiting immediately.
	if _, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: 1, Frames: []*gantryv1.Frame{f64Frame("v", time.Now().UnixNano(), 5)}},
	})); err != nil {
		t.Fatalf("pre-publish: %v", err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := liveClient.Subscribe(subCtx, connect.NewRequest(&gantryv1.SubscribeRequest{DeviceId: device, ReplaySeconds: 60}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// The very first response must be empty.
	if !stream.Receive() {
		t.Fatalf("no first response: %v", stream.Err())
	}
	if n := len(stream.Msg().Frames); n != 0 {
		t.Fatalf("first SubscribeResponse had %d frames, want 0 (stream-open contract)", n)
	}

	// Data must follow the open signal (the replayed frame).
	deadline := time.After(15 * time.Second)
	got := make(chan bool, 1)
	go func() {
		for stream.Receive() {
			if len(stream.Msg().Frames) > 0 {
				got <- true
				return
			}
		}
		got <- false
	}()
	select {
	case ok := <-got:
		if !ok {
			t.Fatalf("stream ended before any data frame: %v", stream.Err())
		}
	case <-deadline:
		t.Fatal("timed out waiting for data after stream-open signal")
	}
	cancel()
}

// TestLiveOnlySubscribe verifies replay_seconds==0 delivers only frames
// published after subscribing (no history).
func TestLiveOnlySubscribe(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	liveClient := gantryv1connect.NewLiveServiceClient(httpClient, baseURL)
	ctx := context.Background()
	const device = "sensor-x"

	// Pre-publish a batch that must NOT appear in a live-only subscription.
	_, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: 1, Frames: []*gantryv1.Frame{f64Frame("v", time.Now().UnixNano(), 7)}},
	}))
	if err != nil {
		t.Fatalf("pre-publish: %v", err)
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := liveClient.Subscribe(subCtx, connect.NewRequest(&gantryv1.SubscribeRequest{DeviceId: device, ReplaySeconds: 0}))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	done := make(chan float64, 8)
	errc := make(chan error, 1)
	go func() {
		for stream.Receive() {
			for _, f := range stream.Msg().Frames {
				done <- f.GetValue().GetF64()
			}
		}
		errc <- stream.Err()
	}()

	// Publish a live marker after subscribing. Retry until it is observed so we
	// don't depend on subscription-establish timing.
	deadline := time.After(15 * time.Second)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-subCtx.Done():
				return
			default:
			}
			_, _ = ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
				Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: uint64(100 + i), Frames: []*gantryv1.Frame{f64Frame("v", time.Now().UnixNano(), 42)}},
			}))
			time.Sleep(100 * time.Millisecond)
		}
	}()

	for {
		select {
		case v := <-done:
			if v == 7 {
				t.Fatalf("live-only subscription replayed a historical frame (value 7)")
			}
			if v == 42 {
				return // got the live marker, and never saw the historical one
			}
		case err := <-errc:
			t.Fatalf("stream ended early: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for live frame")
		}
	}
}
