package foxglove

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeServer is a minimal FAKE `foxglove.websocket.v1` server for the client
// tests — the Go counterpart of adapters/foxglove/tests/fake_server.py. On
// connect it sends serverInfo then advertise; it records subscribe/unsubscribe;
// and it can push binary MessageData frames (scalars as JSON), advertise /
// unadvertise channels dynamically, and drop the active connection to simulate a
// server restart. No real Foxglove involved.
type fakeServer struct {
	ts *httptest.Server

	mu             sync.Mutex
	channels       []fakeChannel
	byTopic        map[string]int  // topic -> channel id
	conn           *websocket.Conn // latest client connection
	subs           map[int]uint32  // channel id -> subscription id
	subscribeCount int
	badSubprotocol bool // when set, Accept negotiates no subprotocol
}

type fakeChannel struct {
	ID         int    `json:"id"`
	Topic      string `json:"topic"`
	Encoding   string `json:"encoding"`
	SchemaName string `json:"schemaName"`
}

// defaultFakeChannels is the lerobot-shaped set: two scalar topics + one image
// topic (protobuf, which the client recognizes and skips).
func defaultFakeChannels() []fakeChannel {
	return []fakeChannel{
		{ID: 1, Topic: "/observation/state", Encoding: "json", SchemaName: "lerobot.Scalars"},
		{ID: 2, Topic: "/action/state", Encoding: "json", SchemaName: "lerobot.Scalars"},
		{ID: 3, Topic: "/observation/images/front", Encoding: "protobuf", SchemaName: "foxglove.CompressedImage"},
	}
}

func newFakeServer(t *testing.T, channels []fakeChannel) *fakeServer {
	t.Helper()
	if channels == nil {
		channels = defaultFakeChannels()
	}
	f := &fakeServer{
		channels: channels,
		byTopic:  make(map[string]int),
		subs:     make(map[int]uint32),
	}
	for _, c := range channels {
		f.byTopic[c.Topic] = c.ID
	}
	f.ts = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

// URL returns the ws:// URL of the fake server.
func (f *fakeServer) URL() string {
	return "ws" + strings.TrimPrefix(f.ts.URL, "http")
}

func (f *fakeServer) Close() {
	f.ts.Close()
}

func (f *fakeServer) setBadSubprotocol() {
	f.mu.Lock()
	f.badSubprotocol = true
	f.mu.Unlock()
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	bad := f.badSubprotocol
	f.mu.Unlock()
	subprotocols := []string{Subprotocol}
	if bad {
		subprotocols = nil // negotiate the empty subprotocol → client rejects
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: subprotocols})
	if err != nil {
		return
	}
	c.SetReadLimit(readLimit)
	defer c.CloseNow()

	ctx := r.Context()
	f.mu.Lock()
	f.conn = c
	f.subs = make(map[int]uint32)
	channels := append([]fakeChannel(nil), f.channels...)
	f.mu.Unlock()

	_ = c.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"op": "serverInfo", "name": "fake", "capabilities": []string{}}))
	_ = c.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"op": "advertise", "channels": channels}))

	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			f.mu.Lock()
			if f.conn == c {
				f.conn = nil
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
			SubscriptionIDs []int `json:"subscriptionIds"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		f.mu.Lock()
		switch msg.Op {
		case "subscribe":
			for _, s := range msg.Subscriptions {
				f.subs[s.ChannelID] = uint32(s.ID)
				f.subscribeCount++
			}
		case "unsubscribe":
			drop := make(map[uint32]bool, len(msg.SubscriptionIDs))
			for _, id := range msg.SubscriptionIDs {
				drop[uint32(id)] = true
			}
			for cid, sid := range f.subs {
				if drop[sid] {
					delete(f.subs, cid)
				}
			}
		}
		f.mu.Unlock()
	}
}

// isSubscribed reports whether the client has subscribed to topic.
func (f *fakeServer) isSubscribed(topic string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	cid, ok := f.byTopic[topic]
	if !ok {
		return false
	}
	_, ok = f.subs[cid]
	return ok
}

// waitForSubscription blocks until the client subscribes to topic (or fails).
func (f *fakeServer) waitForSubscription(t *testing.T, topic string) {
	t.Helper()
	if !waitUntil(func() bool { return f.isSubscribed(topic) }, 3*time.Second) {
		t.Fatalf("no subscription for %s within timeout", topic)
	}
}

// sendScalars pushes a scalar MessageData frame on topic. values map to
// {label,value} entries; iteration order is fine (each is emitted independently).
func (f *fakeServer) sendScalars(t *testing.T, topic string, values map[string]float64, logTime uint64) {
	t.Helper()
	scalars := make([]map[string]any, 0, len(values))
	for k, v := range values {
		scalars = append(scalars, map[string]any{"label": k, "value": v})
	}
	payload := mustJSON(map[string]any{"scalars": scalars})
	f.sendOnTopic(t, topic, payload, logTime)
}

func (f *fakeServer) sendOnTopic(t *testing.T, topic string, payload []byte, logTime uint64) {
	t.Helper()
	f.mu.Lock()
	cid, ok := f.byTopic[topic]
	sid, subbed := f.subs[cid]
	conn := f.conn
	f.mu.Unlock()
	if !ok || !subbed || conn == nil {
		t.Fatalf("topic %s not subscribed / no connection", topic)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, encodeMessageData(sid, logTime, payload)); err != nil {
		t.Fatalf("send on %s: %v", topic, err)
	}
}

// advertise announces a new channel mid-session.
func (f *fakeServer) advertise(t *testing.T, ch fakeChannel) {
	t.Helper()
	f.mu.Lock()
	f.channels = append(f.channels, ch)
	f.byTopic[ch.Topic] = ch.ID
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"op": "advertise", "channels": []fakeChannel{ch}}))
}

// unadvertise removes channels mid-session.
func (f *fakeServer) unadvertise(t *testing.T, channelIDs []int) {
	t.Helper()
	f.mu.Lock()
	kept := f.channels[:0:0]
	for _, c := range f.channels {
		drop := false
		for _, id := range channelIDs {
			if c.ID == id {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, c)
		}
	}
	f.channels = kept
	for _, id := range channelIDs {
		delete(f.subs, id)
	}
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, mustJSON(map[string]any{"op": "unadvertise", "channelIds": channelIDs}))
}

// dropClient closes the active connection to simulate a server restart.
func (f *fakeServer) dropClient() {
	f.mu.Lock()
	conn := f.conn
	f.conn = nil
	f.mu.Unlock()
	if conn != nil {
		_ = conn.Close(websocket.StatusGoingAway, "restart")
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fakeServer: marshal: %v", err))
	}
	return b
}

// waitUntil polls pred until true or timeout.
func waitUntil(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return pred()
}
