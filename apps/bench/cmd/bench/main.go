// Command bench is the self-contained Gantry Bench binary: embedded
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

	"github.com/Sammyjroberts/gantry/apps/bench/internal/server"
)

func main() {
	var (
		port     = flag.Int("port", 4780, "HTTP port to serve on")
		dataDir  = flag.String("data-dir", filepath.Join(".", "data", "bench"), "JetStream data directory")
		shutWait = flag.Duration("shutdown-timeout", 10*time.Second, "graceful shutdown timeout")
		// requireAuth forces the bearer-token path even for loopback callers. Off
		// by default so localhost stays plug-in-and-go; turn it on to require a
		// token from every client (e.g. a shared/HIL bench, or to test denial).
		requireAuth = flag.Bool("require-auth", false, "require a bearer token even from localhost (default: localhost is fully trusted)")
	)
	flag.Parse()

	if err := adoptLegacyDataDir(*dataDir); err != nil {
		log.Fatalf("bench: migrate legacy data dir: %v", err)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("bench: create data dir: %v", err)
	}

	ctx := context.Background()
	app, err := server.New(ctx, *dataDir, server.WithRequireAuth(*requireAuth))
	if err != nil {
		log.Fatalf("bench: start: %v", err)
	}
	if *requireAuth {
		log.Printf("bench: -require-auth set; all clients (including localhost) must present a bearer token")
	}

	addr := net.JoinHostPort("", strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("bench: listen %s: %v", addr, err)
	}
	log.Printf("bench: serving on http://localhost:%d (data dir %s)", *port, *dataDir)

	serveErr := make(chan error, 1)
	go func() { serveErr <- app.Serve(ln) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		if err != nil {
			log.Fatalf("bench: %v", err)
		}
	case sig := <-stop:
		log.Printf("bench: received %s, shutting down", sig)
		shutCtx, cancel := context.WithTimeout(context.Background(), *shutWait)
		defer cancel()
		if err := app.Shutdown(shutCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("bench: shutdown: %v", err)
		}
		log.Printf("bench: stopped")
	}
}
