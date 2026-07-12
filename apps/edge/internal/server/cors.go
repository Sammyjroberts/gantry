package server

import (
	"net/http"
	"strings"
)

// withCORS wraps a handler with a permissive CORS policy for local development
// (the Vite dev server on a localhost origin). It reflects localhost origins and
// allows the header/method set that the Connect, gRPC-Web, and Connect-streaming
// protocols require.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLocalOrigin(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
			h.Set("Access-Control-Expose-Headers", strings.Join(exposedHeaders, ", "))
			h.Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Request headers used by Connect / gRPC-Web / Connect streaming.
var allowedHeaders = []string{
	"Content-Type",
	"Connect-Protocol-Version",
	"Connect-Timeout-Ms",
	"Connect-Accept-Encoding",
	"Connect-Content-Encoding",
	"Grpc-Timeout",
	"X-Grpc-Web",
	"X-User-Agent",
	"Accept-Encoding",
	"Authorization",
	"Message-Type",
}

// Response headers a browser client needs to read back (gRPC/Connect trailers).
var exposedHeaders = []string{
	"Grpc-Status",
	"Grpc-Message",
	"Grpc-Status-Details-Bin",
	"Connect-Content-Encoding",
	"Connect-Accept-Encoding",
}

func isLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	// http(s)://localhost[:port] or 127.0.0.1 / [::1]
	rest := origin
	switch {
	case strings.HasPrefix(rest, "http://"):
		rest = rest[len("http://"):]
	case strings.HasPrefix(rest, "https://"):
		rest = rest[len("https://"):]
	default:
		return false
	}
	host := rest
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch host {
	case "localhost", "127.0.0.1", "[::1]", "::1":
		return true
	}
	return false
}
