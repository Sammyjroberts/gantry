package server_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolText returns the first text-content block of a tool result as a string.
// The SDK mirrors a handler's structured output into a JSON text block, so this
// is the raw JSON payload a client would see.
func toolText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool returned IsError; content=%v", res.Content)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatalf("no text content in result: %+v", res)
	return ""
}

// TestMCPOverStreamableHTTP starts a full Edge server, ingests telemetry through
// the ConnectRPC ingest client, then drives the /mcp endpoint as a real MCP
// client over streamable HTTP: initialize (via Connect), tools/list, and
// tools/call for every tool, asserting real payloads. This proves /mcp coexists
// with the ConnectRPC routes on the same port and that the tools read live data.
func TestMCPOverStreamableHTTP(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)

	ctx := context.Background()
	const device = "rover-1"
	const packet = "imu"

	// Register + ingest a dense pitch series (so get_window downsamples) plus a
	// sparse channel.
	if _, err := ingestClient.RegisterChannels(ctx, connect.NewRequest(&gantryv1.RegisterChannelsRequest{
		DeviceId: device,
		Channels: []*gantryv1.ChannelInfo{
			{Name: "pitch_deg", Packet: packet, Kind: gantryv1.ValueKind_VALUE_KIND_F64, Unit: "deg", Description: "pitch angle"},
		},
	})); err != nil {
		t.Fatalf("RegisterChannels: %v", err)
	}

	base := time.Now().Add(-2 * time.Second).UnixNano()
	const n = 1200
	frames := make([]*gantryv1.Frame, 0, n+1)
	for i := 0; i < n; i++ {
		frames = append(frames, f64FrameP(packet, "pitch_deg", base+int64(i)*1_000_000, float64(i%90)))
	}
	frames = append(frames, f64FrameP(packet, "roll_deg", time.Now().UnixNano(), 12.5)) // auto-registered, sparse
	if _, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: 1, Frames: frames},
	})); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}

	// ---- speak MCP over streamable HTTP ----
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "gantry-test", Version: "0"}, nil)
	transport := &mcpsdk.StreamableClientTransport{Endpoint: baseURL + "/mcp"}
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("MCP Connect (initialize): %v", err)
	}
	defer session.Close()

	// initialize handshake result.
	init := session.InitializeResult()
	if init.ServerInfo == nil || init.ServerInfo.Name != "gantry-core" {
		t.Fatalf("server name = %v, want gantry-core", init.ServerInfo)
	}
	t.Logf("initialize: server=%q version=%q protocol=%s", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion)

	// tools/list.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"list_channels", "get_window", "get_last", "edge_status"} {
		if !names[want] {
			t.Fatalf("missing tool %q; got %v", want, names)
		}
	}
	t.Logf("tools/list: %v", names)

	// list_channels.
	{
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_channels", Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("list_channels: %v", err)
		}
		txt := toolText(t, res)
		t.Logf("list_channels -> %s", txt)
		var got struct {
			Devices []struct {
				DeviceID string `json:"device_id"`
				Channels []struct {
					Name string `json:"name"`
					Kind string `json:"kind"`
					Unit string `json:"unit"`
				} `json:"channels"`
			} `json:"devices"`
		}
		if err := json.Unmarshal([]byte(txt), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Devices) != 1 || got.Devices[0].DeviceID != device {
			t.Fatalf("devices = %+v", got.Devices)
		}
		found := false
		for _, c := range got.Devices[0].Channels {
			if c.Name == "pitch_deg" && c.Kind == "f64" && c.Unit == "deg" {
				found = true
			}
		}
		if !found {
			t.Fatalf("pitch_deg not listed correctly: %+v", got.Devices[0].Channels)
		}
	}

	// get_window (dense -> downsampled buckets).
	{
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_window", Arguments: map[string]any{
			"device_id": device, "channels": []string{"pitch_deg"}, "seconds": 60, "max_points_per_channel": 200,
		}})
		if err != nil {
			t.Fatalf("get_window: %v", err)
		}
		txt := toolText(t, res)
		var got struct {
			Channels []struct {
				Channel     string `json:"channel"`
				RawCount    int    `json:"raw_count"`
				Downsampled bool   `json:"downsampled"`
				Buckets     []struct {
					TNs   int64   `json:"t_ns"`
					Min   float64 `json:"min"`
					Max   float64 `json:"max"`
					Mean  float64 `json:"mean"`
					Count int     `json:"count"`
				} `json:"buckets"`
			} `json:"channels"`
		}
		if err := json.Unmarshal([]byte(txt), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Channels) != 1 {
			t.Fatalf("channels = %+v", got.Channels)
		}
		c := got.Channels[0]
		t.Logf("get_window pitch_deg -> raw_count=%d downsampled=%v buckets=%d bucket[0]=%+v",
			c.RawCount, c.Downsampled, len(c.Buckets), firstBucket(c.Buckets))
		if c.RawCount != n {
			t.Fatalf("raw_count = %d, want %d", c.RawCount, n)
		}
		if !c.Downsampled || len(c.Buckets) == 0 || len(c.Buckets) > 200 {
			t.Fatalf("expected downsampling to <=200 buckets, got %d (downsampled=%v)", len(c.Buckets), c.Downsampled)
		}
		total := 0
		for _, b := range c.Buckets {
			total += b.Count
		}
		if total != n {
			t.Fatalf("bucket counts sum = %d, want %d", total, n)
		}
	}

	// get_last (pitch has data; roll has one point).
	{
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_last", Arguments: map[string]any{"device_id": device}})
		if err != nil {
			t.Fatalf("get_last: %v", err)
		}
		txt := toolText(t, res)
		t.Logf("get_last -> %s", txt)
		var got struct {
			Channels []struct {
				Channel    string   `json:"channel"`
				Value      *float64 `json:"value"`
				AgeSeconds *float64 `json:"age_seconds"`
				Stale      bool     `json:"stale"`
			} `json:"channels"`
		}
		if err := json.Unmarshal([]byte(txt), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		seen := map[string]bool{}
		for _, c := range got.Channels {
			seen[c.Channel] = true
			if c.Value != nil && (c.AgeSeconds == nil || c.Stale) {
				t.Errorf("channel %s has value but bad age/stale: %+v", c.Channel, c)
			}
		}
		if !seen["pitch_deg"] || !seen["roll_deg"] {
			t.Fatalf("get_last missing channels: %v", seen)
		}
	}

	// edge_status.
	{
		res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "edge_status", Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("edge_status: %v", err)
		}
		txt := toolText(t, res)
		t.Logf("edge_status -> %s", txt)
		var got struct {
			UptimeSeconds float64 `json:"uptime_seconds"`
			Stream        *struct {
				Name string `json:"name"`
				Msgs uint64 `json:"msgs"`
			} `json:"stream"`
			Devices []struct {
				DeviceID     string `json:"device_id"`
				ChannelCount int    `json:"channel_count"`
			} `json:"devices"`
		}
		if err := json.Unmarshal([]byte(txt), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Stream == nil || got.Stream.Msgs == 0 || got.Stream.Name != "TLM" {
			t.Fatalf("stream = %+v", got.Stream)
		}
		if len(got.Devices) != 1 || got.Devices[0].DeviceID != device {
			t.Fatalf("devices = %+v", got.Devices)
		}
	}
}

func firstBucket[T any](b []T) any {
	if len(b) == 0 {
		return nil
	}
	return b[0]
}
