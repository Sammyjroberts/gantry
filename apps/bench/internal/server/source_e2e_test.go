package server_test

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

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"github.com/coder/websocket"
)

// fakeFoxServer is a minimal `foxglove.websocket.v1` server for the source e2e
// test: it advertises one scalar topic, records the subscription, and can push
// scalar MessageData frames. Kept alive across an App restart to prove the
// supervisor reconnects a persisted source.
type fakeFoxServer struct {
	ts *httptest.Server

	mu    sync.Mutex
	conn  *websocket.Conn
	subID uint32
}

func newFakeFoxServer(t *testing.T) *fakeFoxServer {
	t.Helper()
	f := &fakeFoxServer{}
	f.ts = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeFoxServer) url() string { return "ws" + strings.TrimPrefix(f.ts.URL, "http") }

func (f *fakeFoxServer) handle(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"foxglove.websocket.v1"}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := r.Context()

	f.mu.Lock()
	f.conn = c
	f.subID = 0
	f.mu.Unlock()

	_ = c.Write(ctx, websocket.MessageText, foxJSON(map[string]any{"op": "serverInfo", "name": "fake"}))
	_ = c.Write(ctx, websocket.MessageText, foxJSON(map[string]any{
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
				f.conn, f.subID = nil, 0
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

func (f *fakeFoxServer) subscribed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conn != nil && f.subID != 0
}

func (f *fakeFoxServer) sendPos(t *testing.T, joint string, value float64, logTime uint64) {
	t.Helper()
	f.mu.Lock()
	conn, sid := f.conn, f.subID
	f.mu.Unlock()
	if conn == nil || sid == 0 {
		t.Fatal("fakeFoxServer: not subscribed")
	}
	payload := foxJSON(map[string]any{"scalars": []map[string]any{{"label": joint + ".pos", "value": value}}})
	frame := make([]byte, 13+len(payload))
	frame[0] = 0x01
	binary.LittleEndian.PutUint32(frame[1:5], sid)
	binary.LittleEndian.PutUint64(frame[5:13], logTime)
	copy(frame[13:], payload)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("fakeFoxServer send: %v", err)
	}
}

func foxJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func waitUntilE2E(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return pred()
}

// TestSourceEndToEnd: create an ENABLED foxglove source via UpsertSource against
// a fake server, and prove the in-process supervisor connects, maps a scalar via
// the lerobot profile, and ingests it — visible through the LiveService channel
// registry — while ListSources reports live status. Then restart the App on the
// same data dir and confirm the source persisted and reconnects.
func TestSourceEndToEnd(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	fox := newFakeFoxServer(t)

	url1, app1 := startEdgeOnDir(t, dir)
	srcClient := gantryv1connect.NewSourceServiceClient(h2cClient(), url1)
	liveClient := gantryv1connect.NewLiveServiceClient(h2cClient(), url1)

	up, err := srcClient.UpsertSource(ctx, connect.NewRequest(&gantryv1.UpsertSourceRequest{
		Source: &gantryv1.Source{
			Type:        "foxglove",
			Name:        "lab bench",
			Url:         fox.url(),
			MappingJson: `{"profile":"lerobot"}`,
			Enabled:     true,
		},
	}))
	if err != nil {
		t.Fatalf("UpsertSource: %v", err)
	}
	srcID := up.Msg.Source.Id
	if srcID == "" {
		t.Fatal("UpsertSource returned no id")
	}

	// The supervisor connects promptly after the enabling upsert.
	if !waitUntilE2E(fox.subscribed, 5*time.Second) {
		t.Fatal("supervisor never connected+subscribed after enabling the source")
	}
	fox.sendPos(t, "shoulder_pan", 21.0, 7777)

	// The mapped follower device + channel appear in the live channel registry.
	if !waitUntilE2E(func() bool {
		lc, err := liveClient.ListChannels(ctx, connect.NewRequest(&gantryv1.ListChannelsRequest{DeviceId: "so101-follower"}))
		if err != nil {
			return false
		}
		for _, d := range lc.Msg.Devices {
			for _, ci := range d.Channels {
				if ci.Packet == "shoulder_pan" && ci.Name == "pos" {
					return true
				}
			}
		}
		return false
	}, 5*time.Second) {
		t.Fatal("mapped channel never reached the live registry (ingest path)")
	}

	// ListSources reports the persisted row + live status.
	if !waitUntilE2E(func() bool {
		ls, err := srcClient.ListSources(ctx, connect.NewRequest(&gantryv1.ListSourcesRequest{}))
		if err != nil {
			return false
		}
		for i, s := range ls.Msg.Sources {
			if s.Id == srcID && i < len(ls.Msg.Statuses) {
				st := ls.Msg.Statuses[i]
				return st.State == "connected" && st.FramesIngested > 0
			}
		}
		return false
	}, 5*time.Second) {
		t.Fatal("ListSources never reported connected + ingested status")
	}

	// ---- restart on the same data dir: the source persists and reconnects ----
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
	srcClient2 := gantryv1connect.NewSourceServiceClient(h2cClient(), url2)

	ls2, err := srcClient2.ListSources(ctx, connect.NewRequest(&gantryv1.ListSourcesRequest{}))
	if err != nil {
		t.Fatalf("ListSources after restart: %v", err)
	}
	found := false
	for _, s := range ls2.Msg.Sources {
		if s.Id == srcID {
			found = true
			if s.Name != "lab bench" || !s.Enabled || s.Url != fox.url() {
				t.Errorf("persisted source mismatch: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("source did not survive restart")
	}
	// The restarted supervisor auto-connects the enabled source.
	if !waitUntilE2E(fox.subscribed, 5*time.Second) {
		t.Fatal("supervisor did not reconnect the persisted source after restart")
	}
}

// TestSourceValidationRejected: UpsertSource enforces the type/url/mapping rules.
func TestSourceValidationRejected(t *testing.T) {
	baseURL := startEdge(t)
	srcClient := gantryv1connect.NewSourceServiceClient(h2cClient(), baseURL)
	ctx := context.Background()

	_, err := srcClient.UpsertSource(ctx, connect.NewRequest(&gantryv1.UpsertSourceRequest{
		Source: &gantryv1.Source{Type: "foxglove", Url: "http://not-ws:1"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument for a non-ws url, got %v", err)
	}
}
