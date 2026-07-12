// Package server assembles the Edge HTTP server: embedded NATS/JetStream, the
// shared ingest engine, the Ingest + Live ConnectRPC handlers, and the embedded
// web UI, all on one port behind h2c so gRPC works over cleartext.
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/Sammyjroberts/gantry/apps/edge/internal/ui"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"github.com/Sammyjroberts/gantry/libs/go/ingest"
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// App is a fully wired Edge instance.
type App struct {
	bus     *stream.Bus
	engine  *ingest.Engine
	handler http.Handler
	srv     *http.Server
}

// New builds an Edge app with an embedded NATS server storing JetStream data in
// storeDir and provisions the TLM stream.
func New(ctx context.Context, storeDir string) (*App, error) {
	bus, err := stream.NewEmbedded(storeDir)
	if err != nil {
		return nil, err
	}
	if err := bus.EnsureStream(ctx); err != nil {
		bus.Close()
		return nil, err
	}

	reg := registry.New()
	engine := ingest.New(bus, reg)

	mux := http.NewServeMux()
	ingestPath, ingestHandler := gantryv1connect.NewIngestServiceHandler(&ingestService{engine: engine})
	livePath, liveHandler := gantryv1connect.NewLiveServiceHandler(&liveService{bus: bus, reg: reg})
	mux.Handle(ingestPath, ingestHandler)
	mux.Handle(livePath, liveHandler)

	// Static UI at "/" (ServeMux routes the more specific RPC prefixes first).
	mux.Handle("/", http.FileServer(http.FS(ui.FS())))

	// h2c so gRPC clients work over cleartext HTTP/2; Connect + gRPC-Web over
	// HTTP/1.1 continue to work too.
	handler := withCORS(mux)
	h2s := &http2.Server{}
	handler = h2c.NewHandler(handler, h2s)

	return &App{bus: bus, engine: engine, handler: handler}, nil
}

// Handler returns the root HTTP handler (used by tests).
func (a *App) Handler() http.Handler { return a.handler }

// Engine exposes the ingest engine (used by tests).
func (a *App) Engine() *ingest.Engine { return a.engine }

// Serve runs the HTTP server on ln until Shutdown is called.
func (a *App) Serve(ln net.Listener) error {
	a.srv = &http.Server{Handler: a.handler}
	if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("edge: serve: %w", err)
	}
	return nil
}

// Shutdown drains the HTTP server, then stops NATS.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	if a.srv != nil {
		err = a.srv.Shutdown(ctx)
	}
	a.bus.Close()
	return err
}
