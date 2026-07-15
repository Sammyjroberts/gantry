package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/Sammyjroberts/gantry/libs/go/blob"
	"github.com/Sammyjroberts/gantry/libs/go/models"
)

// RegisterModels mounts the plain-HTTP v1 model-file API on mux, Register-style
// like RegisterVideo (see video.go). withCORS already covers these routes.
//
// Routes:
//
//	GET /models/{$}      → {"files":[...]}          (trailing-slash list)
//	GET /models/{name}   → raw file bytes (Content-Type by extension)
//	PUT /models/{name}   → store the body under name (extension allowlist + size cap)
func RegisterModels(mux *http.ServeMux, svc *models.Service) {
	h := &modelsHandler{svc: svc}
	mux.HandleFunc("GET /models/{$}", h.list)
	mux.HandleFunc("GET /models/{name}", h.get)
	mux.HandleFunc("PUT /models/{name}", h.put)
}

type modelsHandler struct {
	svc *models.Service
}

func (h *modelsHandler) list(w http.ResponseWriter, r *http.Request) {
	files, err := h.svc.List(r.Context())
	if err != nil {
		writeModelsError(w, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (h *modelsHandler) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rc, contentType, err := h.svc.Get(r.Context(), name)
	if err != nil {
		writeModelsError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", contentType)
	_, _ = io.Copy(w, rc)
}

// put stores the request body under name. The size cap is enforced pre-read via
// a Content-Length check; the service's bounded read is the backstop.
func (h *modelsHandler) put(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if r.ContentLength > h.svc.MaxFileBytes() {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := h.svc.Put(r.Context(), name, r.Body); err != nil {
		writeModelsError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

// writeModelsError maps a models domain error to an HTTP status. There is no
// catalog for model files, so a GET on a missing name surfaces as the blob
// store's wrapped blob.ErrNotFound (fsblob and s3blob both unify their native
// not-found under it), which we map to 404. Bad names/extensions are 400/415,
// oversize 413, everything else 500.
func writeModelsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, models.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, models.ErrUnsupportedExt):
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
	case errors.Is(err, models.ErrTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, blob.ErrNotFound):
		http.Error(w, "model file not found", http.StatusNotFound)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
