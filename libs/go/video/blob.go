package video

import "github.com/Sammyjroberts/gantry/libs/go/blob"

// BlobStore is the blob dependency of the video service. It is a type alias for
// libs/go/blob.Store — Gantry's shared object-storage seam (fsblob on Edge,
// s3blob on core) — so the coordinator injects the concrete store and this
// package stays storage-agnostic. It is an alias rather than a re-declared
// interface because blob.Store.List returns []blob.ObjectInfo, a named type that
// structural typing cannot reproduce; depending on blob directly is both correct
// and the intended wiring now that the package exists.
//
// The video service only ever uses keys under the "video/" prefix (see keyFor).
// blob.Store guarantees: Get/Delete on a missing key return a wrapped
// blob.ErrNotFound; Put replaces atomically; keys are logical "/"-separated
// paths with traversal ("..") rejected by the store itself.
type BlobStore = blob.Store
