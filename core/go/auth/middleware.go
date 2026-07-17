package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
)

// ErrInvalidToken is returned by a Verifier when a bearer string is malformed,
// unknown, or has the wrong secret — all indistinguishable to the caller so
// there is no oracle. The middleware maps it to 401.
var ErrInvalidToken = errors.New("invalid token")

// ScopesHeader is an INTERNAL, server-set request header carrying the granted
// scopes (space-separated) into downstream handlers that can't see the Go
// context — specifically the MCP streamable handler, which surfaces the request
// header to tools via RequestExtra but does NOT propagate the per-request Go
// context to tool calls. The middleware ALWAYS deletes any client-supplied value
// before setting its own, so a client cannot forge scopes by sending this
// header. See core/go/mcp for the read side.
const ScopesHeader = "X-Gantry-Scopes"

// Grant is the outcome of a successful authentication: which token (empty for
// loopback trust) and the scopes it carries.
type Grant struct {
	// TokenID is the public token id, or "" for a fully-trusted loopback caller.
	TokenID string
	// Scopes are the granted scopes (all four for loopback trust).
	Scopes []string
	// Loopback is true when this grant came from loopback trust, not a token.
	Loopback bool
}

// Verifier verifies a bearer token string and returns its grant. *Store
// satisfies it.
type Verifier interface {
	Verify(ctx context.Context, bearer string) (*Grant, error)
}

type grantKey struct{}

// WithGrant attaches a grant to ctx (used by the middleware; exposed for tests).
func WithGrant(ctx context.Context, g *Grant) context.Context {
	return context.WithValue(ctx, grantKey{}, g)
}

// GrantFromContext returns the grant attached to ctx, or nil.
func GrantFromContext(ctx context.Context) *Grant {
	g, _ := ctx.Value(grantKey{}).(*Grant)
	return g
}

// Middleware guards next with the token/loopback policy. requireAuth forces the
// token path even for loopback (paranoid mode; also how tests and e2e exercise
// denial paths without binding a non-loopback socket).
//
// Ordering note: this middleware expects to sit INSIDE CORS (handler =
// withCORS(auth(mux))). CORS is outermost so that (a) a preflight OPTIONS is
// answered without a token, and (b) 401/403 responses still carry the CORS
// headers a cross-origin browser needs in order to READ the status and show the
// "connect to bench" prompt. As defense in depth the middleware also lets any
// OPTIONS through untouched.
func Middleware(v Verifier, requireAuth bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Preflight is never authenticated (belt-and-suspenders; CORS above
		// normally answers it first).
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		scope, needsAuth := RequiredScope(r.Method, r.URL.Path)
		if !needsAuth {
			// Open route (SPA + static assets): the app itself is not secret.
			next.ServeHTTP(w, r)
			return
		}

		// Strip any client-supplied internal scopes header up front so it can
		// never be spoofed, regardless of which branch we take below.
		r.Header.Del(ScopesHeader)

		// Loopback trust: full access, no token, unless paranoid mode is on.
		if !requireAuth && isLoopback(r.RemoteAddr) {
			g := &Grant{Scopes: AllScopes(), Loopback: true}
			serveWithGrant(w, r, next, g)
			return
		}

		bearer, ok := extractToken(r, scope)
		if !ok {
			writeUnauthenticated(w)
			return
		}
		g, err := v.Verify(r.Context(), bearer)
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				writeUnauthenticated(w)
				return
			}
			http.Error(w, "internal auth error", http.StatusInternalServerError)
			return
		}
		if !HasScope(g.Scopes, scope) {
			writeForbidden(w, scope)
			return
		}
		serveWithGrant(w, r, next, g)
	})
}

// serveWithGrant attaches the grant to the request context and sets the internal
// scopes header (for the MCP handler), then serves next.
func serveWithGrant(w http.ResponseWriter, r *http.Request, next http.Handler, g *Grant) {
	r.Header.Set(ScopesHeader, EncodeScopes(g.Scopes))
	next.ServeHTTP(w, r.WithContext(WithGrant(r.Context(), g)))
}

// extractToken pulls the bearer token from the Authorization header, or — for
// the read-scoped download routes a browser <a> (or a three.js asset loader)
// can't add a header to — from a ?token= query parameter. The query fallback is
// restricted to GET on /export/, /video/, and /models/ (CSV downloads,
// video-chunk GETs, and 3D mesh/URDF loads), is read-scoped for those routes,
// and is verified and logged exactly like a header token.
func extractToken(r *http.Request, scope string) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		const p = "Bearer "
		if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
			if t := strings.TrimSpace(h[len(p):]); t != "" {
				return t, true
			}
		}
		return "", false
	}
	if queryTokenAllowed(r.Method, r.URL.Path) {
		if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
			return t, true
		}
	}
	return "", false
}

// queryTokenAllowed reports whether ?token= is accepted for this request. Only
// GET fetches under /export/, /video/, and /models/ qualify — everything else
// must use the Authorization header (fetch-based calls can set it; <a>
// downloads, <video src>, and three.js mesh loaders can't).
func queryTokenAllowed(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	return strings.HasPrefix(path, "/export/") ||
		strings.HasPrefix(path, "/video/") ||
		strings.HasPrefix(path, "/models/")
}

// isLoopback reports whether remoteAddr (host:port) is a loopback address:
// 127.0.0.0/8 or ::1. This is the plug-in-and-go trust boundary.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr // no port (unlikely) — try as-is
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// writeUnauthenticated writes a 401 with a Bearer challenge and a human hint
// telling the operator where to mint a token.
func writeUnauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="gantry-bench"`)
	writeAuthJSON(w, http.StatusUnauthorized, map[string]string{
		"error": "authentication required",
		"hint":  "create a token on the bench: Settings → Access tokens",
	})
}

// writeForbidden writes a 403 naming the scope the token lacked.
func writeForbidden(w http.ResponseWriter, scope string) {
	writeAuthJSON(w, http.StatusForbidden, map[string]string{
		"error":          "insufficient scope",
		"required_scope": scope,
		"hint":           "this token is missing the " + scope + " scope",
	})
}

func writeAuthJSON(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
