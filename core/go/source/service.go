// Package source implements bench-managed telemetry sources (see
// proto/gantry/v1/source.proto): connections the Bench itself maintains to pull
// telemetry in from external publishers — the first kind being a Foxglove
// WebSocket server. It owns the CRUD service logic, its ConnectRPC handler, and
// the in-process Supervisor that connects each enabled source, decodes and maps
// its stream (core/go/foxglove), ingests the frames, and reconnects with
// backoff. Metadata is persisted in the Bench SQLite store (core/go/benchdb)
// behind the same Store shape as workspaces/hardware (SQLite now, Postgres
// later).
package source

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/foxglove"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Validation errors. The ConnectRPC handler maps these to status codes.
var (
	// ErrInvalid is a bad request (bad type, url scheme, or mapping document).
	ErrInvalid = errors.New("invalid source request")
)

// SourceTypeFoxglove is the only source type today.
const SourceTypeFoxglove = "foxglove"

// MaxMappingBytes caps the opaque mapping_json document. 256 KiB is generous for
// a full explicit rule set while bounding a single row and the request body.
const MaxMappingBytes = 256 * 1024

// idBytes is the number of random bytes in a generated source id (→ 2x hex
// chars). 8 bytes = 64 bits = 16 hex chars: collision-negligible at bench scale.
const idBytes = 8

// Service is the source CRUD engine. It validates requests, generates ids on
// create, stamps timestamps, and delegates persistence to the Store. now is
// injectable so tests get deterministic timestamps.
type Service struct {
	store *Store
	now   func() time.Time
}

// NewService builds a Service over an already-migrated *sql.DB.
func NewService(db *sql.DB) *Service {
	return &Service{store: NewStore(db), now: time.Now}
}

// Store exposes the underlying store (parity with the other services; the
// supervisor reads through it).
func (s *Service) Store() *Store { return s.store }

// Upsert creates or updates a source. An empty id creates a new source with a
// generated random hex id; a non-empty id updates the existing row. Validation:
// type must be "foxglove"; url must be ws:// or wss://; mapping_json must be
// within MaxMappingBytes and parse/resolve (an empty document defaults to the
// lerobot profile). created_ns is preserved across updates; updated_ns is
// stamped now. The stored, canonical row is returned.
func (s *Service) Upsert(ctx context.Context, src *gantryv1.Source) (*gantryv1.Source, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: source is required", ErrInvalid)
	}
	typ := strings.TrimSpace(src.Type)
	if typ == "" {
		typ = SourceTypeFoxglove // type defaults to the only kind today
	}
	if typ != SourceTypeFoxglove {
		return nil, fmt.Errorf("%w: type %q must be %q", ErrInvalid, src.Type, SourceTypeFoxglove)
	}
	url := strings.TrimSpace(src.Url)
	if !validWSURL(url) {
		return nil, fmt.Errorf("%w: url %q must be ws:// or wss://", ErrInvalid, src.Url)
	}
	if len(src.MappingJson) > MaxMappingBytes {
		return nil, fmt.Errorf("%w: mapping_json is %d bytes (max %d)", ErrInvalid, len(src.MappingJson), MaxMappingBytes)
	}
	if err := foxglove.ValidateMapping(src.MappingJson); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	id := src.Id
	if id == "" {
		var err error
		if id, err = newID(); err != nil {
			return nil, err
		}
	}

	nowNs := uint64(s.now().UnixNano())
	row := &gantryv1.Source{
		Id:          id,
		Type:        typ,
		Name:        strings.TrimSpace(src.Name),
		Url:         url,
		MappingJson: src.MappingJson,
		Enabled:     src.Enabled,
		CreatedNs:   nowNs, // only applied on insert (ON CONFLICT keeps the original)
		UpdatedNs:   nowNs,
	}
	if err := s.store.Upsert(ctx, row); err != nil {
		return nil, err
	}
	// Re-read so created_ns reflects a prior insert when this was an update.
	return s.store.Get(ctx, id)
}

// Get returns one source by id (ErrNotFound if unknown).
func (s *Service) Get(ctx context.Context, id string) (*gantryv1.Source, error) {
	return s.store.Get(ctx, id)
}

// List returns all sources.
func (s *Service) List(ctx context.Context) ([]*gantryv1.Source, error) {
	return s.store.List(ctx)
}

// Delete removes a source (ErrNotFound if unknown).
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// validWSURL accepts a non-empty ws:// or wss:// URL with a host.
func validWSURL(url string) bool {
	for _, scheme := range []string{"ws://", "wss://"} {
		if strings.HasPrefix(url, scheme) && len(url) > len(scheme) {
			return true
		}
	}
	return false
}

// newID returns a short random hex id from crypto/rand.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("source: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
