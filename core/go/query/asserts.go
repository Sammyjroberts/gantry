package query

import "github.com/Sammyjroberts/gantry/core/go/stream"

// Compile-time proof that the concrete stream bus satisfies the Replayer
// interface, so Bench (and later Cloud) can wire *stream.Bus in directly.
var _ Replayer = (*stream.Bus)(nil)
