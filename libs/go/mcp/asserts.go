package mcp

import (
	"github.com/Sammyjroberts/gantry/libs/go/registry"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// Compile-time proof that the concrete engine types satisfy the MCP interfaces,
// so Edge (and later Backend) can wire *registry.Registry and *stream.Bus in
// directly without adapters. Stream state is adapted via BusStreamStater.
var (
	_ ChannelLister = (*registry.Registry)(nil)
	_ Replayer      = (*stream.Bus)(nil)
)
