package server

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"time"
)

// newSPAHandler serves the embedded web console as a single-page app. The
// console is a client-routed React app (react-router BrowserRouter): deep links
// like /hardware/sim-robot are virtual routes with no corresponding file on
// disk, so a bare http.FileServer would 404 them. This handler bridges that gap:
//
//   - Real files in the embedded FS are served directly, with the FileServer's
//     correct content-types (e.g. /assets/index-*.js → text/javascript).
//   - GET/HEAD requests for a virtual route — a path with no file extension that
//     doesn't match a real file — fall back to index.html with a 200 so the
//     client router can render the route.
//   - Anything else (a missing asset with an extension, e.g. /nope.png, or a
//     non-GET/HEAD method) 404s, so genuinely absent assets still fail loudly.
//
// It is mounted at "/" and, because ServeMux routes more-specific patterns
// first, never shadows the ConnectRPC service prefixes, /mcp, or the other API
// routes — every one of those is a more specific pattern.
func newSPAHandler(fsys fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, fmt.Errorf("server: read embedded index.html: %w", err)
	}
	fileServer := http.FileServer(http.FS(fsys))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only GET/HEAD get SPA treatment; other methods fall through to the
		// FileServer (which answers 405/404 as appropriate).
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fileServer.ServeHTTP(w, r)
			return
		}

		name := path.Clean("/" + r.URL.Path)
		if realFileExists(fsys, name) {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Not a real file. A path that looks like an asset (has an extension)
		// is a genuine 404; an extensionless path is a client route → index.
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, index)
	}), nil
}

// realFileExists reports whether name (a cleaned, rooted URL path) resolves to a
// regular file in fsys. The root "/" maps to index.html, which exists, so it
// serves through the FileServer.
func realFileExists(fsys fs.FS, name string) bool {
	// fs.FS paths are unrooted and never "."; "/" means the index document.
	p := name
	if p == "/" {
		p = "index.html"
	} else {
		p = p[1:] // strip leading slash
	}
	f, err := fsys.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// serveIndex writes index.html with a 200. http.ServeContent picks the
// text/html content-type from the name, handles HEAD (no body), and the zero
// modtime disables conditional-request handling. no-cache lets the browser
// revalidate so a redeployed console (new asset hashes) is picked up.
func serveIndex(w http.ResponseWriter, r *http.Request, index []byte) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}
