package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/Sammyjroberts/gantry/core/go/stream"
	"github.com/nats-io/nats.go/jetstream"
)

// BusStreamStater adapts a *stream.Bus into a StreamStater without modifying the
// stream package: it derives a JetStream context from the bus's exposed NATS
// connection and reads the TLM stream's state on demand. The JetStream context
// is created once and cached.
func BusStreamStater(bus *stream.Bus) StreamStater {
	return &busStater{bus: bus}
}

type busStater struct {
	bus  *stream.Bus
	mu   sync.Mutex
	js   jetstream.JetStream
	init bool
}

func (b *busStater) jetstream() (jetstream.JetStream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.init {
		return b.js, nil
	}
	js, err := jetstream.New(b.bus.Conn())
	if err != nil {
		return nil, fmt.Errorf("mcp: jetstream from bus conn: %w", err)
	}
	b.js, b.init = js, true
	return js, nil
}

func (b *busStater) StreamState(ctx context.Context) (StreamState, error) {
	js, err := b.jetstream()
	if err != nil {
		return StreamState{}, err
	}
	st, err := js.Stream(ctx, stream.StreamName)
	if err != nil {
		return StreamState{}, fmt.Errorf("mcp: lookup stream %s: %w", stream.StreamName, err)
	}
	info, err := st.Info(ctx)
	if err != nil {
		return StreamState{}, fmt.Errorf("mcp: stream info: %w", err)
	}
	s := info.State
	out := StreamState{
		Name:     info.Config.Name,
		Msgs:     s.Msgs,
		Bytes:    s.Bytes,
		FirstSeq: s.FirstSeq,
		LastSeq:  s.LastSeq,
	}
	if !s.FirstTime.IsZero() {
		out.FirstTsNs = s.FirstTime.UnixNano()
	}
	if !s.LastTime.IsZero() {
		out.LastTsNs = s.LastTime.UnixNano()
	}
	return out, nil
}
