// Package workspace implements named, persistent bench layouts for the console
// (see proto/gantry/v1/workspace.proto): the panel grid — charts, state strips,
// readouts, 3D, cameras, SQL — plus each panel's configuration, stored
// server-side as an opaque versioned JSON document. It owns the CRUD service
// logic and its ConnectRPC handler. Metadata is persisted in the Bench SQLite
// store (core/go/benchdb) behind the same Store shape as experiments and
// hardware (SQLite now, Postgres later).
package workspace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Validation errors. The ConnectRPC handler maps these to status codes.
var (
	// ErrInvalid is a bad request (e.g. empty name, oversized layout_json).
	ErrInvalid = errors.New("invalid workspace request")
)

// MaxLayoutBytes caps the opaque layout_json document. 1 MiB is generous for a
// full panel grid with per-panel config while bounding a single row and the
// request body.
const MaxLayoutBytes = 1024 * 1024

// idBytes is the number of random bytes in a generated workspace id (→ 2x hex
// chars). 8 bytes = 64 bits = 16 hex chars: collision-negligible for
// bench-scale counts while staying short enough to paste into a URL.
const idBytes = 8

// Service is the workspace CRUD engine. It validates requests, generates ids on
// create, caps the opaque layout field, stamps timestamps, and delegates
// persistence to the Store. now is injectable so tests get deterministic
// timestamps.
type Service struct {
	store *Store
	now   func() time.Time
}

// NewService builds a Service over an already-migrated *sql.DB.
func NewService(db *sql.DB) *Service {
	return &Service{store: NewStore(db), now: time.Now}
}

// Store exposes the underlying store (parity with experiments/hardware; handy
// for tests and future read paths).
func (s *Service) Store() *Store { return s.store }

// Upsert creates or updates a workspace. An empty id creates a new workspace
// with a generated random hex id; a non-empty id updates the existing row. name
// is required (non-empty after trimming) and is stored trimmed. layout_json
// must be within MaxLayoutBytes. created_ns is preserved across updates;
// updated_ns is stamped now. The stored, canonical row is returned.
func (s *Service) Upsert(ctx context.Context, ws *gantryv1.Workspace) (*gantryv1.Workspace, error) {
	if ws == nil {
		return nil, fmt.Errorf("%w: workspace is required", ErrInvalid)
	}
	name := strings.TrimSpace(ws.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(ws.LayoutJson) > MaxLayoutBytes {
		return nil, fmt.Errorf("%w: layout_json is %d bytes (max %d)", ErrInvalid, len(ws.LayoutJson), MaxLayoutBytes)
	}

	id := ws.Id
	if id == "" {
		var err error
		if id, err = newID(); err != nil {
			return nil, err
		}
	}

	nowNs := uint64(s.now().UnixNano())
	row := &gantryv1.Workspace{
		Id:         id,
		Name:       name,
		LayoutJson: ws.LayoutJson,
		CreatedNs:  nowNs, // only applied on insert (ON CONFLICT keeps the original)
		UpdatedNs:  nowNs,
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		return nil, err
	}
	// Re-read so created_ns reflects a prior insert when this was an update.
	return s.store.Get(ctx, id)
}

// Get returns one workspace by id, including its layout_json (ErrNotFound if
// unknown).
func (s *Service) Get(ctx context.Context, id string) (*gantryv1.Workspace, error) {
	return s.store.Get(ctx, id)
}

// List returns all workspaces (name + timestamps only; layout_json omitted).
func (s *Service) List(ctx context.Context) ([]*gantryv1.Workspace, error) {
	return s.store.List(ctx)
}

// Delete removes a workspace (ErrNotFound if unknown).
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// newID returns a short random hex id from crypto/rand.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("workspace: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
