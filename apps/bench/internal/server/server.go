// Package server assembles the Bench HTTP server: embedded NATS/JetStream, the
// shared ingest engine, the Ingest + Live ConnectRPC handlers, and the embedded
// web UI, all on one port behind h2c so gRPC works over cleartext.
package server

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Sammyjroberts/gantry/apps/bench/internal/ui"
	"github.com/Sammyjroberts/gantry/core/go/auth"
	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/blob"
	"github.com/Sammyjroberts/gantry/core/go/eval"
	"github.com/Sammyjroberts/gantry/core/go/experiments"
	"github.com/Sammyjroberts/gantry/core/go/hardware"
	"github.com/Sammyjroberts/gantry/core/go/ingest"
	"github.com/Sammyjroberts/gantry/core/go/mcp"
	"github.com/Sammyjroberts/gantry/core/go/models"
	"github.com/Sammyjroberts/gantry/core/go/registry"
	"github.com/Sammyjroberts/gantry/core/go/source"
	"github.com/Sammyjroberts/gantry/core/go/stream"
	"github.com/Sammyjroberts/gantry/core/go/video"
	"github.com/Sammyjroberts/gantry/core/go/workspace"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// App is a fully wired Bench instance.
type App struct {
	bus         *stream.Bus
	engine      *ingest.Engine
	db          *sql.DB
	persistence *Persistence
	sources     *source.Supervisor
	cancelBg    context.CancelFunc
	handler     http.Handler
	srv         *http.Server
}

// Option configures optional server behavior (functional-options so existing
// callers and tests keep the two-arg New).
type Option func(*options)

type options struct {
	requireAuth bool
}

// WithRequireAuth forces the bearer-token path even for loopback callers
// (paranoid mode). Off by default: localhost is fully trusted (plug-in-and-go).
// The bench binary exposes this as -require-auth; tests use it to exercise the
// token/denial paths without binding a non-loopback socket.
func WithRequireAuth(v bool) Option {
	return func(o *options) { o.requireAuth = v }
}

// New builds an Bench app with an embedded NATS server storing JetStream data in
// storeDir, provisions the TLM stream, and opens (migrating on first boot) the
// persistent SQLite store at <storeDir>/bench.db.
func New(ctx context.Context, storeDir string, opts ...Option) (*App, error) {
	var cfg options
	for _, o := range opts {
		o(&cfg)
	}
	bus, err := stream.NewEmbedded(storeDir)
	if err != nil {
		return nil, err
	}
	if err := bus.EnsureStream(ctx); err != nil {
		bus.Close()
		return nil, err
	}

	db, err := benchdb.Open(ctx, filepath.Join(storeDir, "bench.db"))
	if err != nil {
		bus.Close()
		return nil, err
	}

	reg := registry.New()
	engine := ingest.New(bus, reg)
	expSvc := experiments.NewService(db)
	expReplayer := experiments.NewReplayer(bus)
	// Hardware: the operator-authored identity layer over telemetry devices. Its
	// unconfigured-device set comes from the channel registry (devices seen live
	// but not yet configured), injected via the narrow DeviceLister interface.
	hwSvc := hardware.NewService(db, registryDeviceLister{reg})

	// Durable tier: blob store + Parquet segment writer + optional DuckDB SQL.
	// Background work (segment flusher, video janitor) runs on its own context
	// so Shutdown can stop it before closing the bus/db underneath it.
	bgCtx, cancelBg := context.WithCancel(context.Background())
	p, err := NewPersistence(ctx, storeDir, db, bus)
	if err != nil {
		cancelBg()
		_ = db.Close()
		bus.Close()
		return nil, err
	}
	if err := p.Start(bgCtx); err != nil {
		cancelBg()
		_ = db.Close()
		bus.Close()
		return nil, err
	}
	blobStore, err := blob.NewFS(filepath.Join(storeDir, "blobs"))
	if err != nil {
		cancelBg()
		_ = db.Close()
		bus.Close()
		return nil, err
	}
	videoSvc := video.NewService(video.NewStore(db), blobStore)
	videoSvc.StartJanitor(bgCtx, video.DefaultRetention, video.DefaultJanitorInterval)
	modelsSvc := models.NewService(blobStore)

	// Shared JetStream state reporter (last sequence + first/last timestamps),
	// used by both the QueryService (range bounds + retention) and MCP.
	stater := mcp.BusStreamStater(bus)

	mux := http.NewServeMux()
	ingestPath, ingestHandler := gantryv1connect.NewIngestServiceHandler(&ingestService{engine: engine})
	livePath, liveHandler := gantryv1connect.NewLiveServiceHandler(&liveService{bus: bus, reg: reg})
	expPath, expHandler := gantryv1connect.NewExperimentServiceHandler(experiments.NewHandler(expSvc))
	queryPath, queryHandler := gantryv1connect.NewQueryServiceHandler(&queryService{bus: bus, stater: stater, segments: p.SegmentReader()})
	hwPath, hwHandler := gantryv1connect.NewHardwareServiceHandler(hardware.NewHandler(hwSvc))
	// Workspaces: named, persistent console layouts (the panel grid + per-panel
	// config), stored opaquely as a versioned JSON document. Same Store shape as
	// experiments/hardware.
	wsSvc := workspace.NewService(db)
	wsPath, wsHandler := gantryv1connect.NewWorkspaceServiceHandler(workspace.NewHandler(wsSvc))
	// Telemetry sources: bench-managed in-process Foxglove clients (connect,
	// decode, map, ingest, reconnect). The supervisor shares the ingest engine as
	// its sink and its lifecycle runs on the background context (started below,
	// stopped in Shutdown), like the segment flusher and video janitor.
	srcSvc := source.NewService(db)
	srcSup := source.NewSupervisor(srcSvc.Store(), engine)
	srcPath, srcHandler := gantryv1connect.NewSourceServiceHandler(source.NewHandler(srcSvc, srcSup))
	// Access tokens: named, scoped bearer credentials for non-loopback callers.
	// The Service and the middleware Verifier share ONE Store so the last-used
	// throttle and clock are consistent across creation and verification.
	authStore := auth.NewStore(db)
	authSvc := auth.NewServiceWithStore(authStore)
	tokPath, tokHandler := gantryv1connect.NewTokenServiceHandler(auth.NewHandler(authSvc))
	// Evals & release gating: reusable test suites → runs → trials. Each trial is
	// bracketed as an experiment via the shared expSvc, so a trial's telemetry
	// range is a first-class experiment (listable, exportable, SQL-queryable).
	evalSvc := eval.NewService(db, expSvc)
	evalPath, evalHandler := gantryv1connect.NewEvalServiceHandler(eval.NewHandler(evalSvc))
	mux.Handle(ingestPath, ingestHandler)
	mux.Handle(livePath, liveHandler)
	mux.Handle(expPath, expHandler)
	mux.Handle(queryPath, queryHandler)
	mux.Handle(hwPath, hwHandler)
	mux.Handle(wsPath, wsHandler)
	mux.Handle(srcPath, srcHandler)
	mux.Handle(tokPath, tokHandler)
	mux.Handle(evalPath, evalHandler)

	// Chunked video catalog + per-device model files (plain HTTP; see the
	// register funcs for the URL surface). SQL over the segment store when a
	// DuckDB engine is present (503 with install hint otherwise).
	RegisterVideo(mux, videoSvc)
	RegisterModels(mux, modelsSvc)
	mux.Handle(sqlRoute, NewSQLHandler(p.SQL))

	// CSV export over plain HTTP (browser- and script-friendly): see
	// proto/gantry/v1/experiment.proto. Streams the experiment's stream-replay
	// window as CSV.
	mux.Handle(exportRoute, &exportHandler{svc: expSvc, replayer: expReplayer})

	// MCP over streamable HTTP at /mcp, on this same port. It shares the engine
	// (registry + stream bus) read-only with the ConnectRPC handlers; the
	// exact-match "/mcp" pattern keeps it clear of both the RPC service prefixes
	// and the "/" UI fallback. The shared "gantry-core" server package is mounted
	// here by Bench and later by Cloud behind tenancy (see docs/MCP.md).
	mux.Handle("/mcp", mcp.NewHandler(mcp.Deps{
		Channels:    reg,
		Replay:      bus,
		Stream:      stater,
		Experiments: expSvc,
		SQL:         NewSQLRunner(p.SQL),
		StartedAt:   time.Now(),
	}))

	// Static UI at "/" (ServeMux routes the more specific RPC prefixes first).
	// The console is a client-routed SPA, so this handler serves real embedded
	// assets and falls back to index.html for deep links (see newSPAHandler).
	spa, err := newSPAHandler(ui.FS())
	if err != nil {
		cancelBg()
		_ = db.Close()
		bus.Close()
		return nil, err
	}
	mux.Handle("/", spa)

	// Connect every enabled telemetry source now; the supervisor keeps them alive
	// (reconnect with backoff) on the background context until Shutdown.
	if err := srcSup.Start(bgCtx); err != nil {
		cancelBg()
		_ = db.Close()
		bus.Close()
		return nil, err
	}

	// Auth guards every network surface. CORS is OUTERMOST so that (a) a browser
	// preflight OPTIONS is answered without a token, and (b) 401/403 responses
	// still carry CORS headers a cross-origin dev client needs to READ the status
	// and show the "connect to bench" prompt. Loopback callers are fully trusted
	// unless -require-auth is set. SPA/static routes are open (see auth.RequiredScope).
	authed := auth.Middleware(authStore, cfg.requireAuth, mux)

	// h2c so gRPC clients work over cleartext HTTP/2; Connect + gRPC-Web over
	// HTTP/1.1 continue to work too.
	handler := withCORS(authed)
	h2s := &http2.Server{}
	handler = h2c.NewHandler(handler, h2s)

	return &App{bus: bus, engine: engine, db: db, persistence: p, sources: srcSup, cancelBg: cancelBg, handler: handler}, nil
}

// registryDeviceLister adapts the channel registry to hardware.DeviceLister:
// the distinct device_ids the registry has seen in telemetry. Kept here (not in
// the hardware package) so hardware stays free of a registry dependency.
type registryDeviceLister struct{ reg *registry.Registry }

func (r registryDeviceLister) SeenDeviceIDs() []string {
	devs := r.reg.List("")
	ids := make([]string, 0, len(devs))
	for _, d := range devs {
		if d.DeviceId != "" {
			ids = append(ids, d.DeviceId)
		}
	}
	return ids
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

// Shutdown drains the HTTP server, then closes the persistent store and NATS.
func (a *App) Shutdown(ctx context.Context) error {
	var err error
	if a.srv != nil {
		err = a.srv.Shutdown(ctx)
	}
	// Stop the source supervisor before tearing down the ingest engine it feeds:
	// this cancels every client goroutine and waits for them to drain.
	if a.sources != nil {
		if serr := a.sources.Stop(ctx); serr != nil && err == nil {
			err = serr
		}
	}
	if a.persistence != nil {
		if perr := a.persistence.Stop(ctx); perr != nil && err == nil {
			err = perr
		}
	}
	if a.cancelBg != nil {
		a.cancelBg()
	}
	if a.db != nil {
		if cerr := a.db.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	a.bus.Close()
	return err
}
