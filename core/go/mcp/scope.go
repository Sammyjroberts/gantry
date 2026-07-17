package mcp

import (
	"fmt"

	"github.com/Sammyjroberts/gantry/core/go/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// requireToolScope enforces a scope on a write tool. The streamable-HTTP MCP
// transport does NOT propagate the per-request Go context into tool calls, so
// the auth middleware can't hand scopes down that way; instead it stamps the
// granted scopes onto the request header (auth.ScopesHeader), which the SDK
// surfaces to tools via RequestExtra.Header. We read them there.
//
// Absence of the header means the call did not pass through the authenticating
// HTTP middleware (a direct in-process call, or MCP mounted without auth): in
// that case we allow, matching the loopback "fully trusted" default. When the
// middleware IS engaged it always sets the header (all scopes for loopback, the
// token's scopes otherwise) after stripping any client-supplied value, so over
// the network the header is authoritative and un-spoofable.
func requireToolScope(req *mcpsdk.CallToolRequest, scope string) error {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return nil
	}
	raw := req.Extra.Header.Get(auth.ScopesHeader)
	if raw == "" {
		return nil
	}
	if !auth.HasScope(auth.DecodeScopes(raw), scope) {
		return fmt.Errorf("this tool requires the %q scope, which the presented bench token does not grant", scope)
	}
	return nil
}
