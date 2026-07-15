package stream

import (
	"context"
	"fmt"
	"sync"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

// Stream retention defaults. Reasonable for a bench box today; tuned later.
const (
	defaultMaxBytes = int64(1) << 30 // 1 GiB
	defaultMaxAge   = 24 * time.Hour
)

// Bus is the JetStream backbone. It is created either with an in-process
// (embedded) NATS server for Bench, or by connecting to an external NATS URL for
// Cloud. Both paths expose the same publish/subscribe surface.
type Bus struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	srv *natsserver.Server // non-nil only for the embedded path
}

// NewEmbedded starts an in-process NATS server with JetStream enabled, storing
// data under storeDir, and returns a Bus wired to an in-process client
// connection (no TCP socket is opened). This is how Bench runs.
func NewEmbedded(storeDir string) (*Bus, error) {
	opts := &natsserver.Options{
		ServerName:         "gantry-edge",
		JetStream:          true,
		StoreDir:           storeDir,
		DontListen:         true, // in-process only; no TCP listener
		NoSigs:             true,
		JetStreamMaxMemory: 64 << 20,
	}
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("stream: new embedded server: %w", err)
	}
	srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("stream: embedded server not ready")
	}
	nc, err := nats.Connect("", nats.InProcessServer(srv))
	if err != nil {
		srv.Shutdown()
		return nil, fmt.Errorf("stream: in-process connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return nil, fmt.Errorf("stream: jetstream: %w", err)
	}
	return &Bus{nc: nc, js: js, srv: srv}, nil
}

// Connect dials an external NATS server (the Cloud path). The remote server
// is expected to have JetStream enabled.
func Connect(url string) (*Bus, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("stream: connect %q: %w", url, err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("stream: jetstream: %w", err)
	}
	return &Bus{nc: nc, js: js}, nil
}

// EnsureStream idempotently provisions the TLM stream (file storage, bound to
// tlm.>). Safe to call on every startup.
func (b *Bus) EnsureStream(ctx context.Context) error {
	_, err := b.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "Gantry telemetry write-ahead log",
		Subjects:    []string{StreamSubject},
		Storage:     jetstream.FileStorage,
		Retention:   jetstream.LimitsPolicy,
		MaxBytes:    defaultMaxBytes,
		MaxAge:      defaultMaxAge,
	})
	if err != nil {
		return fmt.Errorf("stream: ensure %s: %w", StreamName, err)
	}
	return nil
}

// Publish writes a FrameBatch to JetStream and returns only after every message
// has been durably acked by the stream. The returned sequence is the highest
// JetStream stream sequence written for this batch.
//
// Design tradeoff: a batch may carry frames for several channels, but the
// subject scheme encodes the channel (tlm.<device>.<channel>) so that live
// subscribers can filter by subject cheaply. We therefore split the batch by
// channel and publish one message per (device, channel) group. This keeps the
// throughput win of batching (frames on the same channel travel together, the
// common case for a single sensor) while preserving subject-level routing.
// Ordering within a channel is preserved; cross-channel ordering within a batch
// is not meaningful because subscribers filter per channel anyway.
func (b *Bus) Publish(ctx context.Context, batch *gantryv1.FrameBatch) (uint64, error) {
	if batch == nil || len(batch.Frames) == 0 {
		return 0, nil
	}
	groups := groupByChannel(batch)
	var maxSeq uint64
	for channel, frames := range groups {
		sub := &gantryv1.FrameBatch{
			DeviceId: batch.DeviceId,
			Sequence: batch.Sequence,
			Frames:   frames,
			// Carry the ingest-stamped arrival time onto every per-channel
			// sub-batch so the segment store persists received_ns end-to-end
			// (telemetry.proto FrameBatch.received_ns). Without this the split
			// would drop it and segments would record a zero arrival time.
			ReceivedNs: batch.ReceivedNs,
		}
		data, err := proto.Marshal(sub)
		if err != nil {
			return maxSeq, fmt.Errorf("stream: marshal: %w", err)
		}
		ack, err := b.js.Publish(ctx, Subject(batch.DeviceId, channel), data)
		if err != nil {
			return maxSeq, fmt.Errorf("stream: publish %s: %w", channel, err)
		}
		if ack.Sequence > maxSeq {
			maxSeq = ack.Sequence
		}
	}
	return maxSeq, nil
}

func groupByChannel(batch *gantryv1.FrameBatch) map[string][]*gantryv1.Frame {
	groups := make(map[string][]*gantryv1.Frame)
	for _, f := range batch.Frames {
		groups[f.Channel] = append(groups[f.Channel], f)
	}
	return groups
}

// Delivered is one frame handed back from a subscription, tagged with the
// device it came from (recovered from the decoded batch, not the subject).
type Delivered struct {
	DeviceID  string
	Frame     *gantryv1.Frame
	StreamSeq uint64
}

// SubscribeOptions selects what a subscription sees and how far back it replays.
type SubscribeOptions struct {
	DeviceID string
	Channels []string
	// ReplaySeconds > 0 replays the last N seconds from the stream before going
	// live; 0 delivers only frames published after subscribing.
	ReplaySeconds uint32
}

// Subscribe opens an ephemeral ordered consumer and streams decoded frames to
// the returned channel. With ReplaySeconds > 0 the consumer starts at
// now-ReplaySeconds (replay), otherwise it delivers new messages only. The
// channel is closed when ctx is cancelled; callers should range over it or
// select on ctx.Done.
func (b *Bus) Subscribe(ctx context.Context, opts SubscribeOptions) (<-chan Delivered, error) {
	ccfg := jetstream.OrderedConsumerConfig{
		FilterSubjects: SubjectFilters(opts.DeviceID, opts.Channels),
	}
	if opts.ReplaySeconds > 0 {
		startTime := time.Now().Add(-time.Duration(opts.ReplaySeconds) * time.Second)
		ccfg.DeliverPolicy = jetstream.DeliverByStartTimePolicy
		ccfg.OptStartTime = &startTime
	} else {
		ccfg.DeliverPolicy = jetstream.DeliverNewPolicy
	}

	cons, err := b.js.OrderedConsumer(ctx, StreamName, ccfg)
	if err != nil {
		return nil, fmt.Errorf("stream: ordered consumer: %w", err)
	}

	out := make(chan Delivered, 256)
	var mu sync.Mutex
	closed := false

	// send delivers on out unless the subscription is shutting down. Holding mu
	// across the select is safe: the ctx.Done case guarantees the select cannot
	// block forever, so the closer can always acquire mu.
	send := func(d Delivered) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		select {
		case out <- d:
		case <-ctx.Done():
		}
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		var fb gantryv1.FrameBatch
		if err := proto.Unmarshal(msg.Data(), &fb); err != nil {
			return // skip corrupt message; ordered consumers need no ack
		}
		var streamSeq uint64
		if meta, err := msg.Metadata(); err == nil {
			streamSeq = meta.Sequence.Stream
		}
		for _, f := range fb.Frames {
			send(Delivered{DeviceID: fb.DeviceId, Frame: f, StreamSeq: streamSeq})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("stream: consume: %w", err)
	}

	go func() {
		<-ctx.Done()
		cc.Stop()
		mu.Lock()
		closed = true
		close(out)
		mu.Unlock()
	}()

	return out, nil
}

// Conn exposes the underlying NATS connection (for advanced callers/tests).
func (b *Bus) Conn() *nats.Conn { return b.nc }

// Close drains the client connection and, for the embedded path, shuts the
// in-process server down.
func (b *Bus) Close() {
	if b.nc != nil {
		_ = b.nc.Drain()
		b.nc.Close()
	}
	if b.srv != nil {
		b.srv.Shutdown()
		b.srv.WaitForShutdown()
	}
}
