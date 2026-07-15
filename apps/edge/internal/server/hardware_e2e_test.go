package server_test

import (
	"context"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// containsID reports whether ids contains want.
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestHardwarePromoteFlow drives the full slice: publish frames as device X so
// it appears "seen but unconfigured" in ListHardware, then Upsert a hardware row
// for it and confirm it becomes configured (and drops out of the unconfigured
// set). Proves the registry -> hardware unconfigured merge is wired in server.New.
func TestHardwarePromoteFlow(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	hwClient := gantryv1connect.NewHardwareServiceClient(httpClient, baseURL)
	ctx := context.Background()

	const device = "rover-x"

	// Publish a batch so the device is seen in the registry.
	now := time.Now().UnixNano()
	_, err := ingestClient.PublishBatch(ctx, connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{
			DeviceId: device,
			Sequence: 1,
			Frames:   []*gantryv1.Frame{f64FrameP("imu", "imu.pitch", now, 1.5)},
		},
	}))
	if err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}

	// ListHardware: no rows, device appears unconfigured.
	list1, err := hwClient.ListHardware(ctx, connect.NewRequest(&gantryv1.ListHardwareRequest{}))
	if err != nil {
		t.Fatalf("ListHardware: %v", err)
	}
	if len(list1.Msg.Hardware) != 0 {
		t.Fatalf("expected no configured hardware, got %+v", list1.Msg.Hardware)
	}
	if !containsID(list1.Msg.UnconfiguredDeviceIds, device) {
		t.Fatalf("device %q not in unconfigured set %v", device, list1.Msg.UnconfiguredDeviceIds)
	}

	// Upsert (promote) with a display name + a viz config envelope.
	up, err := hwClient.UpsertHardware(ctx, connect.NewRequest(&gantryv1.UpsertHardwareRequest{
		Hardware: &gantryv1.Hardware{
			DeviceId:      device,
			DisplayName:   "Rover X",
			VizConfigJson: `{"v":1,"bindings":{"pitch":{"channelKey":"imu|imu.pitch","unit":"deg","sign":1}}}`,
		},
	}))
	if err != nil {
		t.Fatalf("UpsertHardware: %v", err)
	}
	if up.Msg.Hardware.CreatedNs == 0 || up.Msg.Hardware.DisplayName != "Rover X" {
		t.Fatalf("upsert result = %+v", up.Msg.Hardware)
	}

	// ListHardware now: device configured, gone from unconfigured.
	list2, err := hwClient.ListHardware(ctx, connect.NewRequest(&gantryv1.ListHardwareRequest{}))
	if err != nil {
		t.Fatalf("ListHardware 2: %v", err)
	}
	if len(list2.Msg.Hardware) != 1 || list2.Msg.Hardware[0].DeviceId != device {
		t.Fatalf("expected device configured, got %+v", list2.Msg.Hardware)
	}
	if containsID(list2.Msg.UnconfiguredDeviceIds, device) {
		t.Fatalf("configured device %q still in unconfigured set %v", device, list2.Msg.UnconfiguredDeviceIds)
	}

	// Get returns the stored viz config verbatim.
	got, err := hwClient.GetHardware(ctx, connect.NewRequest(&gantryv1.GetHardwareRequest{DeviceId: device}))
	if err != nil {
		t.Fatalf("GetHardware: %v", err)
	}
	if got.Msg.Hardware.VizConfigJson != up.Msg.Hardware.VizConfigJson {
		t.Fatalf("viz config not round-tripped: %q", got.Msg.Hardware.VizConfigJson)
	}
}

// TestHardwareSurvivesRestart proves metadata persistence: a hardware row
// configured against one Edge instance is still present after the process
// restarts on the same data dir (a new App over the same edge.db).
func TestHardwareSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	url1, app1 := startEdgeOnDir(t, dir)
	hw1 := gantryv1connect.NewHardwareServiceClient(h2cClient(), url1)
	if _, err := hw1.UpsertHardware(ctx, connect.NewRequest(&gantryv1.UpsertHardwareRequest{
		Hardware: &gantryv1.Hardware{
			DeviceId:          "persisted-dev",
			DisplayName:       "Persisted Rig",
			Notes:             "survives restart",
			PanelDefaultsJson: `{"v":1,"channels":["imu|imu.pitch"]}`,
		},
	})); err != nil {
		t.Fatalf("UpsertHardware: %v", err)
	}
	shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := app1.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	url2, app2 := startEdgeOnDir(t, dir)
	t.Cleanup(func() {
		c, cn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cn()
		_ = app2.Shutdown(c)
	})
	hw2 := gantryv1connect.NewHardwareServiceClient(h2cClient(), url2)
	got, err := hw2.GetHardware(ctx, connect.NewRequest(&gantryv1.GetHardwareRequest{DeviceId: "persisted-dev"}))
	if err != nil {
		t.Fatalf("GetHardware after restart: %v", err)
	}
	if got.Msg.Hardware.DisplayName != "Persisted Rig" || got.Msg.Hardware.Notes != "survives restart" {
		t.Fatalf("hardware did not survive restart: %+v", got.Msg.Hardware)
	}
	if got.Msg.Hardware.PanelDefaultsJson != `{"v":1,"channels":["imu|imu.pitch"]}` {
		t.Fatalf("panel defaults not persisted: %q", got.Msg.Hardware.PanelDefaultsJson)
	}
}
