// Package auth is Bench's access-control layer: named, scoped bearer tokens plus
// the single HTTP middleware that guards every network surface. The product bar
// is "secure as fuck, simple simple simple": requests from loopback are fully
// trusted (the plug-in-and-go experience never sees auth), and anything else
// needs a bearer token minted once in the console. There are no accounts,
// passwords, or sessions — a token is created once, pasted once, done.
//
// This file owns the scope vocabulary and the route-family map. A token carries
// a subset of four scopes; each network route maps to exactly one required
// scope (or is open). The map is the security contract and is deliberately
// centralized here so there is one place to audit which surface needs which
// grant.
package auth

import (
	"net/http"
	"strings"
)

// The scopes. They name route families, not individual RPCs, so the set stays
// tiny and memorable:
//   - ingest:  publish/register telemetry (IngestService).
//   - read:    query/live/export/SQL/model-reads/MCP reads.
//   - operate: experiments, workspaces, video capture, eval runs/trials, MCP write tools.
//   - verify:  submit eval verdicts ONLY — the least-privilege grant for a
//     bring-your-own verifier (vision/agent), which scores trials but must never
//     drive hardware or promote a release.
//   - admin:   hardware config, model uploads, token management, baseline promotion.
const (
	ScopeIngest  = "ingest"
	ScopeRead    = "read"
	ScopeOperate = "operate"
	ScopeVerify  = "verify"
	ScopeAdmin   = "admin"
)

// allScopes is the full grant handed to fully-trusted (loopback) callers and the
// allowlist CreateToken validates against.
var allScopes = []string{ScopeIngest, ScopeRead, ScopeOperate, ScopeVerify, ScopeAdmin}

// AllScopes returns a fresh copy of the four valid scopes (stable order).
func AllScopes() []string {
	out := make([]string, len(allScopes))
	copy(out, allScopes)
	return out
}

// ValidScope reports whether s is one of the four known scopes.
func ValidScope(s string) bool {
	switch s {
	case ScopeIngest, ScopeRead, ScopeOperate, ScopeVerify, ScopeAdmin:
		return true
	default:
		return false
	}
}

// NormalizeScopes trims, de-duplicates, and canonically orders a scope set,
// rejecting any unknown scope. An empty result is an error at the call site
// (a token with no scopes can do nothing). Order is canonical (ingest, read,
// operate, admin) so stored/serialized scope strings are stable.
func NormalizeScopes(scopes []string) ([]string, error) {
	seen := map[string]bool{}
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !ValidScope(s) {
			return nil, &UnknownScopeError{Scope: s}
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for _, s := range allScopes { // canonical order
		if seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// UnknownScopeError is returned by NormalizeScopes for an unrecognized scope.
type UnknownScopeError struct{ Scope string }

func (e *UnknownScopeError) Error() string {
	return `unknown scope "` + e.Scope + `" (valid: ingest, read, operate, verify, admin)`
}

// EncodeScopes joins a scope set for storage/transport as a space-separated
// string (space-separated matches OAuth scope conventions and the internal MCP
// header).
func EncodeScopes(scopes []string) string { return strings.Join(scopes, " ") }

// DecodeScopes splits a space-separated scope string back into a slice. Unknown
// or empty tokens are dropped (defensive: the DB is trusted, but this keeps a
// hand-edited row from surfacing junk scopes).
func DecodeScopes(s string) []string {
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if ValidScope(f) {
			out = append(out, f)
		}
	}
	return out
}

// HasScope reports whether granted contains want.
func HasScope(granted []string, want string) bool {
	for _, g := range granted {
		if g == want {
			return true
		}
	}
	return false
}

// routeRule is one entry in the route-family map: a required scope for requests
// whose path has this prefix, optionally narrowed to a single HTTP method (for
// the mixed GET/PUT and GET/POST plain-HTTP surfaces). exact matches the whole
// path rather than a prefix.
type routeRule struct {
	prefix string
	exact  bool
	method string // "" = any method
	scope  string
}

// routeRules is THE security contract: which network surface needs which scope.
// It is ordered most-specific-first so the method-qualified /video and /models
// rules win over a bare prefix. Anything not matched here is OPEN (the SPA and
// its static assets — the app itself is not secret; the data behind these APIs
// is). Documented mapping:
//
//	IngestService/*                              → ingest
//	LiveService/*, QueryService/*, /export/*,    → read
//	  /sql, /video GET, /models GET, /mcp
//	ExperimentService/*, WorkspaceService/*,     → operate
//	  /video POST
//	HardwareService/*, TokenService/*,           → admin
//	  /models PUT
//
// /mcp is read-at-connect; its write tools (start/stop experiment) separately
// enforce operate inside the MCP handler via the scopes attached to the request
// (see Middleware and core/go/mcp).
var routeRules = []routeRule{
	{prefix: "/gantry.v1.IngestService/", scope: ScopeIngest},
	{prefix: "/gantry.v1.LiveService/", scope: ScopeRead},
	{prefix: "/gantry.v1.QueryService/", scope: ScopeRead},
	{prefix: "/gantry.v1.ExperimentService/", scope: ScopeOperate},
	{prefix: "/gantry.v1.WorkspaceService/", scope: ScopeOperate},
	// Evals: reads need read; mutations operate; a verdict submission is the
	// least-privilege verify scope (a BYO verifier can score but not drive
	// hardware); promoting a baseline is an admin release action. The exact
	// per-RPC rules must precede the service prefix so they win.
	{prefix: "/gantry.v1.EvalService/SubmitVerdict", exact: true, scope: ScopeVerify},
	{prefix: "/gantry.v1.EvalService/PromoteBaseline", exact: true, scope: ScopeAdmin},
	{prefix: "/gantry.v1.EvalService/ListSuites", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.EvalService/GetSuite", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.EvalService/ListRuns", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.EvalService/GetRun", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.EvalService/GetBaseline", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.EvalService/", scope: ScopeOperate},
	// Stations: discovery is read; register/lease/renew/release operate.
	{prefix: "/gantry.v1.StationService/ListStations", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.StationService/GetStation", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.StationService/CheckTarget", exact: true, scope: ScopeRead},
	{prefix: "/gantry.v1.StationService/", scope: ScopeOperate},
	{prefix: "/gantry.v1.HardwareService/", scope: ScopeAdmin},
	{prefix: "/gantry.v1.TokenService/", scope: ScopeAdmin},
	{prefix: "/export/", scope: ScopeRead},
	{prefix: "/sql", exact: true, scope: ScopeRead},
	{prefix: "/mcp", scope: ScopeRead},
	// Mixed-method plain-HTTP surfaces: a write needs a higher scope than a read.
	{prefix: "/video/", method: http.MethodPost, scope: ScopeOperate},
	{prefix: "/video/", scope: ScopeRead},
	{prefix: "/models/", method: http.MethodPut, scope: ScopeAdmin},
	{prefix: "/models/", scope: ScopeRead},
}

// RequiredScope classifies a request into the scope it requires. ok is false for
// OPEN routes (SPA/static) which need no scope at all. Method matters only for
// the /video and /models surfaces; everywhere else the prefix decides.
func RequiredScope(method, path string) (scope string, ok bool) {
	for _, r := range routeRules {
		if r.method != "" && r.method != method {
			continue
		}
		if r.exact {
			if path == r.prefix {
				return r.scope, true
			}
			continue
		}
		if strings.HasPrefix(path, r.prefix) {
			return r.scope, true
		}
	}
	return "", false
}
