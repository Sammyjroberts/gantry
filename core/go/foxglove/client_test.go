package foxglove

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// captureIngestor records everything the client publishes, standing in for the
// real ingest engine in the client tests.
type captureIngestor struct {
	mu       sync.Mutex
	batches  []*gantryv1.FrameBatch
	channels map[string]map[[2]string]*gantryv1.ChannelInfo // device -> (packet,name) -> info
}

func newCaptureIngestor() *captureIngestor {
	return &captureIngestor{channels: make(map[string]map[[2]string]*gantryv1.ChannelInfo)}
}

func (c *captureIngestor) RegisterChannels(deviceID string, chans []*gantryv1.ChannelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dev := c.channels[deviceID]
	if dev == nil {
		dev = make(map[[2]string]*gantryv1.ChannelInfo)
		c.channels[deviceID] = dev
	}
	for _, ci := range chans {
		dev[[2]string{ci.Packet, ci.Name}] = ci
	}
}

func (c *captureIngestor) PublishBatch(_ context.Context, batch *gantryv1.FrameBatch) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, batch)
	return batch.Sequence, nil
}

// find returns the latest frame for (device, packet, channel), or nil.
func (c *captureIngestor) find(device, packet, channel string) *gantryv1.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out *gantryv1.Frame
	for _, b := range c.batches {
		if b.DeviceId != device {
			continue
		}
		for _, f := range b.Frames {
			if f.Packet == packet && f.Channel == channel {
				out = f
			}
		}
	}
	return out
}

func (c *captureIngestor) unit(device, packet, channel string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dev := c.channels[device]; dev != nil {
		if ci := dev[[2]string{packet, channel}]; ci != nil {
			return ci.Unit
		}
	}
	return ""
}

func runClient(t *testing.T, url string, mapping *Mapping, ing Ingestor) (*Client, context.CancelFunc) {
	t.Helper()
	c := NewClient(url, mapping, ing, Options{FlushInterval: 10 * time.Millisecond, Logf: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = c.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("client Run did not return after cancel")
		}
	})
	return c, cancel
}

func TestClientRejectsWrongSubprotocol(t *testing.T) {
	srv := newFakeServer(t, nil)
	srv.setBadSubprotocol()
	ing := newCaptureIngestor()
	c := NewClient(srv.URL(), lerobotProfile(), ing, Options{Logf: t.Logf})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "subprotocol") {
		t.Fatalf("expected subprotocol rejection, got %v", err)
	}
}

func TestClientSubscribesToScalarTopicsSkipsImages(t *testing.T) {
	srv := newFakeServer(t, nil)
	ing := newCaptureIngestor()
	runClient(t, srv.URL(), lerobotProfile(), ing)

	srv.waitForSubscription(t, "/observation/state")
	srv.waitForSubscription(t, "/action/state")

	// The protobuf image channel is recognized and skipped: never subscribed.
	if srv.isSubscribed("/observation/images/front") {
		t.Error("client subscribed to a protobuf image channel; it must be skipped")
	}
}

func TestClientScalarDecodeAndLerobotMapping(t *testing.T) {
	srv := newFakeServer(t, nil)
	ing := newCaptureIngestor()
	runClient(t, srv.URL(), lerobotProfile(), ing)

	srv.waitForSubscription(t, "/observation/state")
	srv.waitForSubscription(t, "/action/state")

	// Observation establishes the follower pos; action fans out and derives track_err.
	srv.sendScalars(t, "/observation/state", map[string]float64{"shoulder_pan.pos": 10}, 500)
	srv.sendScalars(t, "/action/state", map[string]float64{"shoulder_pan.pos": 13}, 2000)

	if !waitUntil(func() bool { return ing.find("so101-follower", "shoulder_pan", "track_err") != nil }, 3*time.Second) {
		t.Fatal("never observed a derived track_err frame")
	}

	// Observation -> follower pos @ log_time 500.
	if f := ing.find("so101-follower", "shoulder_pan", "pos"); f == nil || f.GetValue().GetF64() != 10 || f.TimestampNs != 500 {
		t.Errorf("follower pos frame = %+v", f)
	}
	// Action fans out to leader pos AND follower cmd.
	if f := ing.find("so101-leader", "shoulder_pan", "pos"); f == nil || f.GetValue().GetF64() != 13 {
		t.Errorf("leader pos frame = %+v", f)
	}
	if f := ing.find("so101-follower", "shoulder_pan", "cmd"); f == nil || f.GetValue().GetF64() != 13 {
		t.Errorf("follower cmd frame = %+v", f)
	}
	// track_err = cmd(13) - pos(10) = 3, stamped at the action's log_time.
	if f := ing.find("so101-follower", "shoulder_pan", "track_err"); f == nil || f.GetValue().GetF64() != 3 || f.TimestampNs != 2000 {
		t.Errorf("track_err frame = %+v", f)
	}

	// Units registered on first appearance (all deg for lerobot).
	for _, tc := range []struct{ device, packet, channel string }{
		{"so101-follower", "shoulder_pan", "pos"},
		{"so101-leader", "shoulder_pan", "pos"},
		{"so101-follower", "shoulder_pan", "cmd"},
		{"so101-follower", "shoulder_pan", "track_err"},
	} {
		if u := ing.unit(tc.device, tc.packet, tc.channel); u != "deg" {
			t.Errorf("unit(%s/%s/%s) = %q, want deg", tc.device, tc.packet, tc.channel, u)
		}
	}
}

func TestClientDynamicAdvertiseSubscribes(t *testing.T) {
	// Start with only the scalar observation topic; advertise a second scalar
	// topic mid-session and confirm the client subscribes and maps it.
	srv := newFakeServer(t, []fakeChannel{
		{ID: 1, Topic: "/observation/state", Encoding: "json", SchemaName: "lerobot.Scalars"},
	})
	ing := newCaptureIngestor()
	runClient(t, srv.URL(), lerobotProfile(), ing)
	srv.waitForSubscription(t, "/observation/state")

	srv.advertise(t, fakeChannel{ID: 2, Topic: "/action/state", Encoding: "json", SchemaName: "lerobot.Scalars"})
	srv.waitForSubscription(t, "/action/state")

	srv.sendScalars(t, "/action/state", map[string]float64{"gripper.pos": 4}, 100)
	if !waitUntil(func() bool { return ing.find("so101-leader", "gripper", "pos") != nil }, 3*time.Second) {
		t.Fatal("dynamically-advertised topic was not mapped")
	}
}

func TestClientUnadvertiseDropsSubscription(t *testing.T) {
	srv := newFakeServer(t, []fakeChannel{
		{ID: 1, Topic: "/observation/state", Encoding: "json", SchemaName: "lerobot.Scalars"},
	})
	ing := newCaptureIngestor()
	runClient(t, srv.URL(), lerobotProfile(), ing)
	srv.waitForSubscription(t, "/observation/state")

	// Unadvertise the only channel; the client drops the subscription. A later
	// re-advertise of the same channel id re-subscribes (proving the client
	// cleared its per-channel state rather than treating it as still-subscribed).
	srv.unadvertise(t, []int{1})
	if !waitUntil(func() bool { return !srv.isSubscribed("/observation/state") }, 3*time.Second) {
		t.Fatal("server still shows a subscription after unadvertise")
	}
	srv.advertise(t, fakeChannel{ID: 1, Topic: "/observation/state", Encoding: "json", SchemaName: "lerobot.Scalars"})
	srv.waitForSubscription(t, "/observation/state")
}

func TestClientReturnsOnServerDrop(t *testing.T) {
	srv := newFakeServer(t, nil)
	ing := newCaptureIngestor()
	c := NewClient(srv.URL(), lerobotProfile(), ing, Options{FlushInterval: 10 * time.Millisecond, Logf: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	srv.waitForSubscription(t, "/observation/state")
	srv.dropClient() // simulate a server restart

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected a non-nil error when the server drops the connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client Run did not return after the server dropped the connection")
	}
}
