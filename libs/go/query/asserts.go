package query

import "github.com/Sammyjroberts/gantry/libs/go/stream"

// Compile-time proof that the concrete stream bus satisfies the Replayer
// interface, so Edge (and later Backend) can wire *stream.Bus in directly.
var _ Replayer = (*stream.Bus)(nil)
