package server

import (
	"context"
	"errors"
	"time"

	connect "connectrpc.com/connect"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"github.com/Sammyjroberts/gantry/libs/go/ingest"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// liveFlushInterval bounds how often batched frames are flushed to a subscriber
// so the browser isn't hammered with one message per frame.
const liveFlushInterval = 50 * time.Millisecond

// liveMaxBatch caps frames per SubscribeResponse to bound message size.
const liveMaxBatch = 500

// ---- IngestService ----

type ingestService struct {
	gantryv1connect.UnimplementedIngestServiceHandler
	engine *ingest.Engine
}

func (s *ingestService) PublishBatch(ctx context.Context, req *connect.Request[gantryv1.PublishBatchRequest]) (*connect.Response[gantryv1.PublishBatchResponse], error) {
	acked, err := s.engine.PublishBatch(ctx, req.Msg.Batch)
	if err != nil {
		if errors.Is(err, ingest.ErrInvalidBatch) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&gantryv1.PublishBatchResponse{AckedSequence: acked}), nil
}

func (s *ingestService) RegisterChannels(_ context.Context, req *connect.Request[gantryv1.RegisterChannelsRequest]) (*connect.Response[gantryv1.RegisterChannelsResponse], error) {
	s.engine.RegisterChannels(req.Msg.DeviceId, req.Msg.Channels)
	return connect.NewResponse(&gantryv1.RegisterChannelsResponse{}), nil
}

// ---- LiveService ----

type liveService struct {
	gantryv1connect.UnimplementedLiveServiceHandler
	bus *stream.Bus
	reg *registry.Registry
}

func (s *liveService) ListChannels(_ context.Context, req *connect.Request[gantryv1.ListChannelsRequest]) (*connect.Response[gantryv1.ListChannelsResponse], error) {
	return connect.NewResponse(&gantryv1.ListChannelsResponse{
		Devices: s.reg.List(req.Msg.DeviceId),
	}), nil
}

func (s *liveService) Subscribe(ctx context.Context, req *connect.Request[gantryv1.SubscribeRequest], out *connect.ServerStream[gantryv1.SubscribeResponse]) error {
	ch, err := s.bus.Subscribe(ctx, stream.SubscribeOptions{
		DeviceID:      req.Msg.DeviceId,
		Channels:      req.Msg.Channels,
		ReplaySeconds: req.Msg.ReplaySeconds,
	})
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}

	// Flush response headers immediately by sending an empty response, so the
	// client's stream opens right away instead of blocking until the first
	// frame. Without this a live-only subscription (no replay, no data yet)
	// would deadlock: the client waits for headers, the server waits for data.
	if err := out.Send(&gantryv1.SubscribeResponse{}); err != nil {
		return err
	}

	ticker := time.NewTicker(liveFlushInterval)
	defer ticker.Stop()

	var buf []*gantryv1.Frame
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		err := out.Send(&gantryv1.SubscribeResponse{Frames: buf})
		buf = nil
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-ch:
			if !ok {
				_ = flush()
				return nil
			}
			buf = append(buf, d.Frame)
			if len(buf) >= liveMaxBatch {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
