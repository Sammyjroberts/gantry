package foxglove

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Ingestor is the sink the client hands mapped frames to: the shared ingest
// engine's PublishBatch path (server-stamped received_ns; frame timestamps stay
// the producer's log_time) plus lazy channel registration with units.
// *ingest.Engine satisfies it.
type Ingestor interface {
	RegisterChannels(deviceID string, channels []*gantryv1.ChannelInfo)
	PublishBatch(ctx context.Context, batch *gantryv1.FrameBatch) (uint64, error)
}

// DefaultFlushInterval bounds how often batched frames are flushed to the
// ingest engine, so a busy scalar stream is coalesced into device batches rather
// than one PublishBatch per message.
const DefaultFlushInterval = 100 * time.Millisecond

// readLimit caps a single inbound WebSocket message. Scalar payloads are tiny;
// this is a generous safety bound (image channels are skipped, not subscribed).
const readLimit = 16 << 20

// Options configures a Client. The zero value is usable; callbacks are optional.
type Options struct {
	// FlushInterval is the batch flush cadence (DefaultFlushInterval if <= 0).
	FlushInterval time.Duration
	// OnConnect fires once per session, immediately after a successful handshake.
	OnConnect func()
	// OnIngest fires after each successful PublishBatch with the number of frames
	// accepted and the max log_time (ns) in that batch. Used by the supervisor to
	// maintain live status (frames_ingested, last_frame_ns).
	OnIngest func(frames int, lastLogNs uint64)
	// Logf logs a single line (defaults to log.Printf). Used for skipped image
	// channels and publish errors.
	Logf func(format string, args ...any)
}

func (o Options) flushInterval() time.Duration {
	if o.FlushInterval <= 0 {
		return DefaultFlushInterval
	}
	return o.FlushInterval
}

// Client is an in-process Foxglove WebSocket subscriber for one source. Each
// Run is a single connection: dial, negotiate the subprotocol, track advertised
// channels, decode MessageData frames, map scalar payloads, and flush batches to
// the ingestor until the context is cancelled or the connection drops. Reconnect
// with backoff is the supervisor's job (one Run == one session).
type Client struct {
	url      string
	mapping  *Mapping
	ingestor Ingestor
	opts     Options

	mu       sync.Mutex
	batchers map[string]*batcher // device_id -> pending batch
}

// batcher accumulates frames and newly-seen channels for one device between
// flushes, carrying a per-device sequence that advances on every published
// batch.
type batcher struct {
	deviceID    string
	sequence    uint64
	frames      []*gantryv1.Frame
	newChannels []*gantryv1.ChannelInfo
	seen        map[[2]string]bool // (packet, channel) already registered
	maxLog      uint64
}

// NewClient builds a client for url with a resolved mapping and an ingest sink.
func NewClient(url string, mapping *Mapping, ingestor Ingestor, opts Options) *Client {
	return &Client{
		url:      url,
		mapping:  mapping,
		ingestor: ingestor,
		opts:     opts,
		batchers: make(map[string]*batcher),
	}
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil {
		c.opts.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Run performs one session: connect, subscribe to matching scalar channels,
// pump MessageData frames into the ingestor, and return when ctx is cancelled or
// the connection errors/closes. Any buffered frames are flushed before it
// returns. The returned error is the reason the session ended (nil only if it
// ended without a transport error, which for a long-lived subscriber is
// unusual).
func (c *Client) Run(ctx context.Context) error {
	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		Subprotocols: []string{Subprotocol},
	})
	if err != nil {
		return fmt.Errorf("foxglove: dial %s: %w", c.url, err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if got := conn.Subprotocol(); got != Subprotocol {
		return fmt.Errorf("foxglove: server did not select subprotocol %q (got %q)", Subprotocol, got)
	}
	conn.SetReadLimit(readLimit)
	if c.opts.OnConnect != nil {
		c.opts.OnConnect()
	}

	// The flush loop runs until the session ends; a final flush (on a fresh
	// context, since ctx may be cancelled) drains whatever is left.
	flushCtx, cancelFlush := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.flushLoop(flushCtx)
	}()
	defer func() {
		cancelFlush()
		wg.Wait()
		c.flushAll(context.Background())
	}()

	s := &session{
		c:         c,
		conn:      conn,
		channels:  make(map[int]Channel),
		subs:      make(map[uint32]Channel),
		chanToSub: make(map[int]uint32),
		nextSub:   1,
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ == websocket.MessageText {
			if err := s.onJSON(ctx, data); err != nil {
				return err
			}
		} else {
			s.onBinary(data)
		}
	}
}

// session holds the per-connection channel/subscription bookkeeping. All of its
// methods run on the single Run read-loop goroutine, so it needs no locking; the
// shared batchers (touched here and by the flush loop) are guarded by Client.mu.
type session struct {
	c         *Client
	conn      *websocket.Conn
	channels  map[int]Channel
	subs      map[uint32]Channel // subscription id -> channel
	chanToSub map[int]uint32     // channel id -> subscription id
	nextSub   uint32
}

func (s *session) onJSON(ctx context.Context, text []byte) error {
	msg, err := decodeJSON(text)
	if err != nil {
		s.c.logf("%v", err)
		return nil // a bad control message is not fatal to the session
	}
	switch msg.Op {
	case opAdvertise:
		return s.onAdvertise(ctx, msg.Channels)
	case opUnadvertise:
		s.onUnadvertise(msg.ChannelIDs)
	case opServerInfo, opStatus:
		// serverInfo/status are informational; nothing to do.
	}
	return nil
}

// onAdvertise subscribes to every newly-advertised channel a mapping rule
// matches and whose encoding is json. Protobuf image channels are recognized
// and skipped with a one-line log (video stays with the Python tap).
func (s *session) onAdvertise(ctx context.Context, channels []Channel) error {
	var newSubs []subEntry
	for _, ch := range channels {
		s.channels[ch.ID] = ch
		if _, ok := s.chanToSub[ch.ID]; ok {
			continue // already subscribed
		}
		rule := s.c.mapping.match(ch.Topic)
		if rule == nil {
			continue
		}
		if rule.kindOrDefault() == "image" || ch.Encoding != EncodingJSON {
			s.c.logf("foxglove: skipping non-json channel topic=%s encoding=%s (image tee stays with the python tap)", ch.Topic, ch.Encoding)
			continue
		}
		subID := s.nextSub
		s.nextSub++
		s.subs[subID] = ch
		s.chanToSub[ch.ID] = subID
		newSubs = append(newSubs, subEntry{ID: int(subID), ChannelID: ch.ID})
	}
	if len(newSubs) == 0 {
		return nil
	}
	payload, err := encodeSubscribe(newSubs)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, payload)
}

func (s *session) onUnadvertise(channelIDs []int) {
	for _, cid := range channelIDs {
		delete(s.channels, cid)
		if subID, ok := s.chanToSub[cid]; ok {
			delete(s.chanToSub, cid)
			delete(s.subs, subID)
		}
	}
}

func (s *session) onBinary(data []byte) {
	msg, err := decodeBinary(data)
	if err != nil {
		s.c.logf("%v", err)
		return
	}
	if msg == nil {
		return // Time frame or unknown opcode
	}
	ch, ok := s.subs[msg.SubscriptionID]
	if !ok {
		return
	}
	rule := s.c.mapping.match(ch.Topic)
	if rule == nil || rule.kindOrDefault() == "image" {
		return
	}
	s.handleScalars(rule, msg)
}

func (s *session) handleScalars(rule *Rule, msg *MessageData) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return
	}
	raw, ok := payload[rule.scalarsField()]
	if !ok {
		return
	}
	var scalars []scalarItem
	if err := json.Unmarshal(raw, &scalars); err != nil {
		return
	}
	for _, f := range s.c.mapping.mapScalars(rule, scalars, msg.LogTime) {
		s.c.add(f, s.c.mapping.unitFor(rule, f.Device, f.Channel))
	}
}

// add appends a mapped frame to its device batcher, registering the (packet,
// channel) with its unit on first appearance.
func (c *Client) add(f Frame, unit string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.batchers[f.Device]
	if b == nil {
		b = &batcher{deviceID: f.Device, seen: make(map[[2]string]bool)}
		c.batchers[f.Device] = b
	}
	key := [2]string{f.Packet, f.Channel}
	if !b.seen[key] {
		b.seen[key] = true
		b.newChannels = append(b.newChannels, &gantryv1.ChannelInfo{
			Name:   f.Channel,
			Kind:   gantryv1.ValueKind_VALUE_KIND_F64,
			Unit:   unit,
			Packet: f.Packet,
		})
	}
	b.frames = append(b.frames, &gantryv1.Frame{
		Channel:     f.Channel,
		Packet:      f.Packet,
		TimestampNs: f.LogTime,
		Value:       &gantryv1.Value{Kind: &gantryv1.Value_F64{F64: f.Value}},
	})
	if f.LogTime > b.maxLog {
		b.maxLog = f.LogTime
	}
}

func (c *Client) flushLoop(ctx context.Context) {
	t := time.NewTicker(c.opts.flushInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.flushAll(ctx)
		}
	}
}

// pendingBatch is a device's snapshot taken under the lock and published
// off-lock.
type pendingBatch struct {
	deviceID    string
	frames      []*gantryv1.Frame
	newChannels []*gantryv1.ChannelInfo
	sequence    uint64
	maxLog      uint64
}

// flushAll registers any newly-seen channels and publishes each device's
// buffered frames as one batch, advancing that device's sequence. Registration
// happens even for a frames-empty flush so units land before the first batch; a
// sequence is consumed only when frames are actually published.
func (c *Client) flushAll(ctx context.Context) {
	c.mu.Lock()
	var work []pendingBatch
	for _, b := range c.batchers {
		if len(b.frames) == 0 && len(b.newChannels) == 0 {
			continue
		}
		p := pendingBatch{deviceID: b.deviceID, newChannels: b.newChannels}
		if len(b.frames) > 0 {
			b.sequence++
			p.frames = b.frames
			p.sequence = b.sequence
			p.maxLog = b.maxLog
		}
		b.frames = nil
		b.newChannels = nil
		b.maxLog = 0
		work = append(work, p)
	}
	c.mu.Unlock()

	// Deterministic device order keeps behaviour stable under test.
	sort.Slice(work, func(i, j int) bool { return work[i].deviceID < work[j].deviceID })

	for _, w := range work {
		if len(w.newChannels) > 0 {
			c.ingestor.RegisterChannels(w.deviceID, w.newChannels)
		}
		if len(w.frames) == 0 {
			continue
		}
		batch := &gantryv1.FrameBatch{DeviceId: w.deviceID, Sequence: w.sequence, Frames: w.frames}
		if _, err := c.ingestor.PublishBatch(ctx, batch); err != nil {
			c.logf("foxglove: publish device=%s: %v", w.deviceID, err)
			continue
		}
		if c.opts.OnIngest != nil {
			c.opts.OnIngest(len(w.frames), w.maxLog)
		}
	}
}
