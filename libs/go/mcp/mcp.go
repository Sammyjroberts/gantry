// Package mcp exposes Gantry's live/recent telemetry over the Model Context
// Protocol so any MCP client (e.g. a Claude Code session) can ask "what did
// pitch do in the last 40 seconds" against a running engine.
//
// The package is transport- and app-agnostic on purpose. It takes narrow
// interfaces to the shared engine — a channel registry, a stream replayer, and
// (optionally) a JetStream state reporter — and builds an MCP server whose
// implementation name is "gantry-core". Edge mounts it today over the same HTTP
// port it already serves; Backend will mount the SAME package later behind a
// tenant-scoped OAuth 2.1 layer (see docs/MCP.md). The server here is
// deliberately tenancy-free: isolation is the mounting app's job.
//
// All tools are read-only.
package mcp

import (
	"context"
	"net/http"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the MCP implementation name shared by every app that mounts this
// package. It advertises the shared engine ("gantry-core") rather than the
// hosting app so clients see one logical server whether they reached it through
// Edge or Backend.
const ServerName = "gantry-core"

// Version is the reported server version. Bump on tool-surface changes.
const Version = "0.1.0"

// ChannelLister enumerates known channels per device. Satisfied directly by
// *registry.Registry.
type ChannelLister interface {
	// List returns known channels; deviceID == "" returns all devices.
	List(deviceID string) []*gantryv1.DeviceChannels
}

// Replayer opens a replay-then-live subscription over the telemetry backbone.
// Satisfied directly by *stream.Bus. The tools here only ever use the replay
// window and drain to a high-water mark, so the "live" tail is never consumed.
type Replayer interface {
	Subscribe(ctx context.Context, opts stream.SubscribeOptions) (<-chan stream.Delivered, error)
}

// StreamStater reports JetStream stream state for edge_status. It is optional:
// if a Deps has no StreamStater, edge_status omits stream stats and the replay
// collector falls back to idle-based termination instead of a sequence
// high-water mark. Edge supplies one via BusStreamStater.
type StreamStater interface {
	StreamState(ctx context.Context) (StreamState, error)
}

// StreamState is a transport-neutral snapshot of the telemetry stream.
type StreamState struct {
	Name      string `json:"name"`
	Msgs      uint64 `json:"msgs"`
	Bytes     uint64 `json:"bytes"`
	FirstSeq  uint64 `json:"first_seq"`
	LastSeq   uint64 `json:"last_seq"`
	FirstTsNs int64  `json:"first_ts_ns"`
	LastTsNs  int64  `json:"last_ts_ns"`
}

// Deps is everything the MCP tool surface needs from the hosting app.
type Deps struct {
	// Channels lists device/packet/channel metadata (required).
	Channels ChannelLister
	// Replay opens replay subscriptions for the window/last tools (required).
	Replay Replayer
	// Stream reports stream state for edge_status (optional but recommended).
	Stream StreamStater
	// StartedAt is when the hosting server came up, for uptime reporting. If
	// zero, uptime is reported as 0.
	StartedAt time.Time
}

// NewServer builds a configured "gantry-core" MCP server with the v1 read-only
// tool surface registered. The same *mcpsdk.Server can back many concurrent
// sessions, so a single instance is safe to reuse across HTTP requests.
func NewServer(d Deps) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    ServerName,
		Title:   "Gantry telemetry",
		Version: Version,
	}, nil)
	registerTools(s, d)
	return s
}

// NewHandler returns an http.Handler that serves streamable-HTTP MCP for the
// given deps. Mount it at "/mcp". A single server instance is shared across
// sessions (getServer returns the same server), which the SDK explicitly
// permits.
func NewHandler(d Deps) http.Handler {
	srv := NewServer(d)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
}
