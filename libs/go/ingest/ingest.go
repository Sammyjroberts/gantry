// Package ingest is the shared ingest engine used by both Edge and Backend. It
// validates FrameBatches, updates the channel registry, and publishes to the
// JetStream backbone. Its ack semantics are the contract from ingest.proto: a
// batch is "accepted" only once it is durably written to the stream.
package ingest

import (
	"context"
	"errors"
	"fmt"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// ErrInvalidBatch is returned (wrapped) for any batch that fails validation.
var ErrInvalidBatch = errors.New("invalid frame batch")

// Publisher is the subset of the stream backbone the engine needs. It lets
// tests substitute a fake and keeps the engine transport-agnostic.
type Publisher interface {
	Publish(ctx context.Context, batch *gantryv1.FrameBatch) (uint64, error)
}

// Engine ties validation + registry + publishing together.
type Engine struct {
	pub Publisher
	reg *registry.Registry
}

// New builds an ingest engine over a publisher and a registry.
func New(pub Publisher, reg *registry.Registry) *Engine {
	return &Engine{pub: pub, reg: reg}
}

// Validate checks a batch against the ingest contract: non-nil, non-empty
// device_id, and every frame must have a non-empty channel, a non-zero
// timestamp, and a value.
func Validate(batch *gantryv1.FrameBatch) error {
	if batch == nil {
		return fmt.Errorf("%w: nil batch", ErrInvalidBatch)
	}
	if batch.DeviceId == "" {
		return fmt.Errorf("%w: empty device_id", ErrInvalidBatch)
	}
	for i, f := range batch.Frames {
		if f == nil {
			return fmt.Errorf("%w: frame %d is nil", ErrInvalidBatch, i)
		}
		if f.Channel == "" {
			return fmt.Errorf("%w: frame %d has empty channel", ErrInvalidBatch, i)
		}
		if f.TimestampNs == 0 {
			return fmt.Errorf("%w: frame %d (channel %q) has zero timestamp", ErrInvalidBatch, i, f.Channel)
		}
		if f.Value == nil {
			return fmt.Errorf("%w: frame %d (channel %q) has no value", ErrInvalidBatch, i, f.Channel)
		}
	}
	return nil
}

// PublishBatch validates, auto-registers channels, then durably publishes. It
// returns the batch's per-device sequence as the acked sequence ONLY after the
// JetStream publish has been acked, satisfying the ingest.proto ack contract.
func (e *Engine) PublishBatch(ctx context.Context, batch *gantryv1.FrameBatch) (uint64, error) {
	if err := Validate(batch); err != nil {
		return 0, err
	}
	// FrameBatch.device_id is authoritative (ingest.proto): emitters may leave
	// Frame.device_id empty, and any per-frame value that disagrees with the
	// batch is overwritten — batch wins, no error. This normalization happens
	// before observe/publish so the registry and the stream both see the
	// canonical device.
	for _, f := range batch.Frames {
		if f != nil {
			f.DeviceId = batch.DeviceId
		}
	}
	// Registry update first so ListChannels reflects auto-registered channels
	// even for frames on brand-new channels.
	e.reg.ObserveBatch(batch)

	if _, err := e.pub.Publish(ctx, batch); err != nil {
		return 0, fmt.Errorf("ingest: publish: %w", err)
	}
	return batch.Sequence, nil
}

// RegisterChannels merges explicit channel metadata into the registry.
func (e *Engine) RegisterChannels(deviceID string, channels []*gantryv1.ChannelInfo) {
	e.reg.Register(deviceID, channels)
}

// Registry exposes the underlying registry (used by the LiveService handler).
func (e *Engine) Registry() *registry.Registry { return e.reg }

// Compile-time assertion that *stream.Bus satisfies Publisher.
var _ Publisher = (*stream.Bus)(nil)
