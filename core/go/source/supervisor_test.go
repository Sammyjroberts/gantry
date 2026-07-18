package source

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/ingest"
	"github.com/Sammyjroberts/gantry/core/go/registry"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// --- minimal fake foxglove server (source-package copy) ---------------------
//
// A compact `foxglove.websocket.v1` server that advertises one scalar topic,
// records the subscription, and pushes scalar MessageData frames. It exists so
// the supervisor tests can drive a real foxglove.Client against a controllable
// endpoint without depending on the foxglove package's test-only fake.

type fakeFox struct {
	ts *httptest.Server

	mu        sync.Mutex
	conn      *websocket.Conn
	subID     uint32 // subscription id for channel 1, 0 if not subscribed
	connCount int
}

func newFakeFox(t *testing.T) *fakeFox {
	t.Helper()
	f := &fakeFox{}
	f.ts = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeFox) url() string { return "ws" + strings.TrimPrefix(f.ts.URL, "http") }

func (f *fakeFox) handle(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"foxglove.websocket.v1"}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := r.Context()

	f.mu.Lock()
	f.conn = c
	f.subID = 0
	f.connCount++
	f.mu.Unlock()

	_ = c.Write(ctx, websocket.MessageText, mustMarshal(map[string]any{"op": "serverInfo", "name": "fake"}))
	_ = c.Write(ctx, websocket.MessageText, mustMarshal(map[string]any{
		"op": "advertise",
		"channels": []map[string]any{
			{"id": 1, "topic": "/observation/state", "encoding": "json", "schemaName": "lerobot.Scalars"},
		},
	}))

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			f.mu.Lock()
			if f.conn == c {
				f.conn = nil
				f.subID = 0
			}
			f.mu.Unlock()
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg struct {
			Op            string `json:"op"`
			Subscriptions []struct {
				ID        int `json:"id"`
				ChannelID int `json:"channelId"`
			} `json:"subscriptions"`
		}
		if json.Unmarshal(data, &msg) == nil && msg.Op == "subscribe" {
			f.mu.Lock()
			for _, s := range msg.Subscriptions {
				if s.ChannelID == 1 {
					f.subID = uint32(s.ID)
				}
			}
			f.mu.Unlock()
		}
	}
}

func (f *fakeFox) subscribed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn != nil && f.subID != 0
}

func (f *fakeFox) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connCount
}

func (f *fakeFox) sendPos(t *testing.T, joint string, value float64, logTime uint64) {
	t.Helper()
	f.mu.Lock()
	conn, sid := f.conn, f.subID
	f.mu.Unlock()
	if conn == nil || sid == 0 {
		t.Fatal("fakeFox: not subscribed")
	}
	payload := mustMarshal(map[string]any{"scalars": []map[string]any{{"label": joint + ".pos", "value": value}}})
	frame := make([]byte, 13+len(payload))
	frame[0] = 0x01
	binary.LittleEndian.PutUint32(frame[1:5], sid)
	binary.LittleEndian.PutUint64(frame[5:13], logTime)
	copy(frame[13:], payload)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("fakeFox send: %v", err)
	}
}

func (f *fakeFox) drop() {
	f.mu.Lock()
	conn := f.conn
	f.conn = nil
	f.subID = 0
	f.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusGoingAway, "restart")
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// --- capturing publisher + engine helpers -----------------------------------

type capturePub struct {
	mu      sync.Mutex
	batches []*gantryv1.FrameBatch
	seq     uint64
}

func (c *capturePub) Publish(_ context.Context, b *gantryv1.FrameBatch) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, b)
	c.seq++
	return c.seq, nil
}

func (c *capturePub) hasFrame(device, packet, channel string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.batches {
		if b.DeviceId != device {
			continue
		}
		for _, fr := range b.Frames {
			if fr.Packet == packet && fr.Channel == channel {
				return true
			}
		}
	}
	return false
}

func newSupTest(t *testing.T) (*Service, *Supervisor, *capturePub) {
	t.Helper()
	db, err := benchdb.Open(context.Background(), t.TempDir()+"/bench.db")
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := NewService(db)
	pub := &capturePub{}
	eng := ingest.New(pub, registry.New())
	sup := NewSupervisor(svc.Store(), eng, WithBackoff(10*time.Millisecond, 50*time.Millisecond), WithLogf(t.Logf))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sup.Stop(ctx)
	})
	return svc, sup, pub
}

func waitFor(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}

func statusOf(t *testing.T, svc *Service, sup *Supervisor, id string) *gantryv1.SourceStatus {
	t.Helper()
	rows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i, st := range sup.StatusFor(rows) {
		if rows[i].Id == id {
			return st
		}
	}
	t.Fatalf("no status for %s", id)
	return nil
}

// TestSupervisorEnableConnectsAndIngests: an enabled source connects, decodes a
// scalar, maps it via the lerobot profile, and ingests it into the real engine;
// live status reflects connected + frames_ingested + last_frame_ns.
func TestSupervisorEnableConnectsAndIngests(t *testing.T) {
	svc, sup, pub := newSupTest(t)
	srv := newFakeFox(t)
	ctx := context.Background()

	src, err := svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Name: "lab", Url: srv.url(), MappingJson: `{"profile":"lerobot"}`, Enabled: true})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !waitFor(srv.subscribed, 3*time.Second) {
		t.Fatal("source never subscribed")
	}
	srv.sendPos(t, "shoulder_pan", 12.5, 4242)

	if !waitFor(func() bool { return pub.hasFrame("so101-follower", "shoulder_pan", "pos") }, 3*time.Second) {
		t.Fatal("observation frame never ingested into the engine")
	}

	if !waitFor(func() bool { return statusOf(t, svc, sup, src.Id).FramesIngested > 0 }, 3*time.Second) {
		t.Fatal("status never reported ingested frames")
	}
	st := statusOf(t, svc, sup, src.Id)
	if st.State != "connected" {
		t.Errorf("state = %q, want connected", st.State)
	}
	if st.LastFrameNs != 4242 {
		t.Errorf("last_frame_ns = %d, want 4242 (the log_time)", st.LastFrameNs)
	}
}

// TestSupervisorDisableStops: toggling enabled=false and reconciling stops the
// client; status returns to "disabled" and the server connection is dropped.
func TestSupervisorDisableStops(t *testing.T) {
	svc, sup, _ := newSupTest(t)
	srv := newFakeFox(t)
	ctx := context.Background()

	src, _ := svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Url: srv.url(), MappingJson: `{"profile":"lerobot"}`, Enabled: true})
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !waitFor(srv.subscribed, 3*time.Second) {
		t.Fatal("source never subscribed")
	}

	// Disable + reconcile (what the handler does after an upsert).
	if _, err := svc.Upsert(ctx, &gantryv1.Source{Id: src.Id, Type: "foxglove", Url: srv.url(), MappingJson: `{"profile":"lerobot"}`, Enabled: false}); err != nil {
		t.Fatalf("disable upsert: %v", err)
	}
	if err := sup.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !waitFor(func() bool { return statusOf(t, svc, sup, src.Id).State == "disabled" }, 3*time.Second) {
		t.Fatalf("state did not return to disabled: %+v", statusOf(t, svc, sup, src.Id))
	}
	if !waitFor(func() bool { return !srv.subscribed() }, 3*time.Second) {
		t.Error("server still shows an active subscription after disable")
	}
}

// TestSupervisorReconnectsAfterDrop: when the server drops the connection, the
// supervisor reconnects (backoff), the reconnects counter advances, and frames
// flow again.
func TestSupervisorReconnectsAfterDrop(t *testing.T) {
	svc, sup, pub := newSupTest(t)
	srv := newFakeFox(t)
	ctx := context.Background()

	src, _ := svc.Upsert(ctx, &gantryv1.Source{Type: "foxglove", Url: srv.url(), MappingJson: `{"profile":"lerobot"}`, Enabled: true})
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !waitFor(srv.subscribed, 3*time.Second) {
		t.Fatal("source never subscribed (initial)")
	}
	firstConns := srv.connections()

	srv.drop()

	// The client reconnects: a second connection is accepted and re-subscribes.
	if !waitFor(func() bool { return srv.connections() > firstConns && srv.subscribed() }, 5*time.Second) {
		t.Fatal("source did not reconnect after server drop")
	}
	if !waitFor(func() bool { return statusOf(t, svc, sup, src.Id).Reconnects > 0 }, 3*time.Second) {
		t.Error("reconnects counter never advanced")
	}

	// Frames flow again on the new connection.
	srv.sendPos(t, "elbow_flex", 7, 9000)
	if !waitFor(func() bool { return pub.hasFrame("so101-follower", "elbow_flex", "pos") }, 3*time.Second) {
		t.Fatal("frames did not resume after reconnect")
	}
}
