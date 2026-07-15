package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/Sammyjroberts/gantry/core/go/video"
)

// RegisterVideo mounts the plain-HTTP v1 video API on mux. It is Register-style
// so server.New can wire it without this package owning the mux: the coordinator
// calls RegisterVideo(mux, videoSvc) alongside the other mux.Handle lines, and
// the existing withCORS wrapper already covers these routes (it wraps the whole
// mux). A proto/ConnectRPC contract for video is deferred to a later wave; this
// JSON/HTTP surface is the v1 capture/replay transport.
//
// Routes:
//
//	POST /video/chunks?camera=<id>&start_ns=<n>&duration_ms=<n>   body = chunk bytes (Content-Type = mime) → {"id":...}
//	GET  /video/chunks?camera=<id>&from_ns=<n>&to_ns=<n>          → {"chunks":[...]}
//	GET  /video/chunks/{id}                                        → raw chunk bytes (Content-Type = stored mime)
//	GET  /video/cameras                                            → {"cameras":[...]}
func RegisterVideo(mux *http.ServeMux, svc *video.Service) {
	h := &videoHandler{svc: svc}
	mux.HandleFunc("POST /video/chunks", h.postChunk)
	mux.HandleFunc("GET /video/chunks", h.listChunks)
	mux.HandleFunc("GET /video/chunks/{id}", h.getChunk)
	mux.HandleFunc("GET /video/cameras", h.listCameras)
}

type videoHandler struct {
	svc *video.Service
}

// postChunk ingests one chunk. camera/start_ns/duration_ms come from the query;
// the body is the chunk bytes and Content-Type is the mime. The size cap is
// enforced pre-read via a Content-Length check, with the service's bounded read
// as the authoritative backstop against a lying/absent Content-Length.
func (h *videoHandler) postChunk(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	camera := q.Get("camera")
	startNs, err := parseInt64(q.Get("start_ns"))
	if err != nil {
		http.Error(w, "bad start_ns: must be an integer", http.StatusBadRequest)
		return
	}
	durationMs, err := parseInt64(q.Get("duration_ms"))
	if err != nil {
		http.Error(w, "bad duration_ms: must be an integer", http.StatusBadRequest)
		return
	}
	if r.ContentLength > h.svc.MaxChunkBytes() {
		http.Error(w, "chunk too large", http.StatusRequestEntityTooLarge)
		return
	}

	mime := r.Header.Get("Content-Type")
	id, err := h.svc.IngestChunk(r.Context(), camera, startNs, durationMs, mime, r.Body)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// listChunks lists a camera's chunks over an optional [from_ns, to_ns] window.
func (h *videoHandler) listChunks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	camera := q.Get("camera")
	if camera == "" {
		http.Error(w, "camera is required", http.StatusBadRequest)
		return
	}
	fromNs, err := parseInt64(q.Get("from_ns"))
	if err != nil {
		http.Error(w, "bad from_ns: must be an integer", http.StatusBadRequest)
		return
	}
	toNs, err := parseInt64(q.Get("to_ns"))
	if err != nil {
		http.Error(w, "bad to_ns: must be an integer", http.StatusBadRequest)
		return
	}
	chunks, err := h.svc.ListChunks(r.Context(), camera, fromNs, toNs)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	if chunks == nil {
		chunks = []video.Chunk{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunks": chunks})
}

// getChunk streams one chunk's bytes with its stored mime. Chunks are whole,
// self-contained files, so there is no range support.
func (h *videoHandler) getChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chunk, rc, err := h.svc.GetChunk(r.Context(), id)
	if err != nil {
		writeVideoError(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", chunk.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(chunk.Bytes, 10))
	// Errors after headers are sent can't change the status; a truncated body is
	// all we can offer (client disconnect, etc.).
	_, _ = io.Copy(w, rc)
}

// listCameras returns the distinct cameras with their latest chunk start.
func (h *videoHandler) listCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := h.svc.ListCameras(r.Context())
	if err != nil {
		writeVideoError(w, err)
		return
	}
	if cams == nil {
		cams = []video.Camera{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": cams})
}

// writeVideoError maps a video domain error to an HTTP status + plain message.
func writeVideoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, video.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, video.ErrUnsupportedMime):
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
	case errors.Is(err, video.ErrTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, video.ErrNotFound):
		http.Error(w, "chunk not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// parseInt64 parses a query int; an empty string is 0 (an absent optional
// bound), any other non-integer is an error.
func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// writeJSON writes v as a JSON response with the given status. Shared by the
// video and models plain-HTTP surfaces.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
