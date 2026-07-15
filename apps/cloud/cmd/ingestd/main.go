// Command ingestd is the Cloud ingest service scaffold. It demonstrates the
// "thin assembly" idea: it wires the SAME shared ingest engine and Ingest
// ConnectRPC handler as Bench, but against an EXTERNAL NATS server instead of an
// embedded one. It is a placeholder for a later milestone — it must compile and
// stand up the service, but persistence, tenancy, and the ClickHouse/LKV sinks
// come later.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	connect "connectrpc.com/connect"
	"github.com/Sammyjroberts/gantry/core/go/ingest"
	"github.com/Sammyjroberts/gantry/core/go/registry"
	"github.com/Sammyjroberts/gantry/core/go/stream"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	natsURL := getenv("GANTRY_NATS_URL", "nats://localhost:4222")
	addr := getenv("GANTRY_LISTEN_ADDR", ":8080")

	bus, err := stream.Connect(natsURL)
	if err != nil {
		log.Fatalf("ingestd: connect nats %q: %v", natsURL, err)
	}
	defer bus.Close()
	if err := bus.EnsureStream(context.Background()); err != nil {
		log.Fatalf("ingestd: ensure stream: %v", err)
	}

	engine := ingest.New(bus, registry.New())

	mux := http.NewServeMux()
	path, handler := gantryv1connect.NewIngestServiceHandler(&ingestService{engine: engine})
	mux.Handle(path, handler)

	log.Printf("ingestd: serving IngestService on %s (nats %s)", addr, natsURL)
	if err := http.ListenAndServe(addr, h2c.NewHandler(mux, &http2.Server{})); err != nil {
		log.Fatalf("ingestd: serve: %v", err)
	}
}

// ingestService is the same handler shape Bench uses; Cloud simply assembles
// it against a different backbone.
type ingestService struct {
	gantryv1connect.UnimplementedIngestServiceHandler
	engine *ingest.Engine
}

func (s *ingestService) PublishBatch(ctx context.Context, req *connect.Request[gantryv1.PublishBatchRequest]) (*connect.Response[gantryv1.PublishBatchResponse], error) {
	acked, err := s.engine.PublishBatch(ctx, req.Msg.Batch)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&gantryv1.PublishBatchResponse{AckedSequence: acked}), nil
}

func (s *ingestService) RegisterChannels(_ context.Context, req *connect.Request[gantryv1.RegisterChannelsRequest]) (*connect.Response[gantryv1.RegisterChannelsResponse], error) {
	s.engine.RegisterChannels(req.Msg.DeviceId, req.Msg.Channels)
	return connect.NewResponse(&gantryv1.RegisterChannelsResponse{}), nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
