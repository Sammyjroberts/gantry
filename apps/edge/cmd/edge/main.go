// Command edge is the self-contained Gantry Edge binary: embedded
// NATS/JetStream, ingest + live telemetry APIs, and the web UI, all on one port.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Sammyjroberts/gantry/apps/edge/internal/server"
)

func main() {
	var (
		port     = flag.Int("port", 4780, "HTTP port to serve on")
		dataDir  = flag.String("data-dir", filepath.Join(".", "data", "edge"), "JetStream data directory")
		shutWait = flag.Duration("shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
	)
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("edge: create data dir: %v", err)
	}

	ctx := context.Background()
	app, err := server.New(ctx, *dataDir)
	if err != nil {
		log.Fatalf("edge: start: %v", err)
	}

	addr := net.JoinHostPort("", strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("edge: listen %s: %v", addr, err)
	}
	log.Printf("edge: serving on http://localhost:%d (data dir %s)", *port, *dataDir)

	serveErr := make(chan error, 1)
	go func() { serveErr <- app.Serve(ln) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil {
			log.Fatalf("edge: %v", err)
		}
	case sig := <-stop:
		log.Printf("edge: received %s, shutting down", sig)
		shutCtx, cancel := context.WithTimeout(context.Background(), *shutWait)
		defer cancel()
		if err := app.Shutdown(shutCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("edge: shutdown: %v", err)
		}
		log.Printf("edge: stopped")
	}
}
