// Package experiments implements experiment tracking for Bench: named time ranges
// over the telemetry stream (see proto/gantry/v1/experiment.proto). It owns the
// CRUD service logic, its ConnectRPC handler, and a bounded stream-replay helper
// used by the CSV export path. Metadata is persisted in the Bench SQLite store
// (core/go/benchdb); frames are never copied — an experiment only indexes a range
// of the telemetry stream.
package experiments

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Validation / state errors. The ConnectRPC handler maps these to status codes.
var (
	// ErrInvalid is a bad request (e.g. empty name, end <= start).
	ErrInvalid = errors.New("invalid experiment request")
	// ErrNotRunning is returned when stopping an experiment that already stopped.
	ErrNotRunning = errors.New("experiment is not running")
)

// idBytes is the number of random bytes in an experiment id (→ 2x hex chars).
// 8 bytes = 64 bits = 16 hex chars: collision-negligible for bench-scale counts
// while staying short enough to paste into a URL or filename.
const idBytes = 8

// Service is the experiment CRUD engine. It defaults timestamps, validates
// requests, generates ids, and delegates persistence to the Store. now is
// injectable so tests get deterministic timestamps.
type Service struct {
	store *Store
	now   func() time.Time
}

// NewService builds a Service over an already-migrated *sql.DB.
func NewService(db *sql.DB) *Service {
	return &Service{store: NewStore(db), now: time.Now}
}

// Store exposes the underlying store (used by the export path to resolve ids).
func (s *Service) Store() *Store { return s.store }

// Start begins an experiment. name must be non-empty. startNs == 0 means "now";
// an explicit startNs (including one in the past) marks a range after the fact.
// end_ns is 0 (running) until Stop.
func (s *Service) Start(ctx context.Context, name, notes, deviceID string, startNs uint64) (*gantryv1.Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	nowNs := uint64(s.now().UnixNano())
	if startNs == 0 {
		startNs = nowNs
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	e := &gantryv1.Experiment{
		Id:        id,
		Name:      name,
		Notes:     notes,
		DeviceId:  deviceID,
		StartNs:   startNs,
		EndNs:     0,
		CreatedNs: nowNs,
	}
	if err := s.store.Insert(ctx, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Stop ends a running experiment. endNs == 0 means "now". The end must be
// strictly after the start. Stopping a non-running (or unknown) experiment
// returns ErrNotRunning / ErrNotFound respectively.
func (s *Service) Stop(ctx context.Context, id string, endNs uint64) (*gantryv1.Experiment, error) {
	e, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.EndNs != 0 {
		return nil, fmt.Errorf("%w: %s already stopped", ErrNotRunning, id)
	}
	if endNs == 0 {
		endNs = uint64(s.now().UnixNano())
	}
	if endNs <= e.StartNs {
		return nil, fmt.Errorf("%w: end_ns (%d) must be after start_ns (%d)", ErrInvalid, endNs, e.StartNs)
	}
	if err := s.store.SetEnd(ctx, id, int64(endNs)); err != nil {
		// A concurrent Stop won the race between our Get and SetEnd.
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: %s already stopped", ErrNotRunning, id)
		}
		return nil, err
	}
	e.EndNs = endNs
	return e, nil
}

// List returns experiments newest-first, optionally filtered to one device.
func (s *Service) List(ctx context.Context, deviceID string) ([]*gantryv1.Experiment, error) {
	return s.store.List(ctx, deviceID)
}

// Get returns one experiment by id (ErrNotFound if unknown).
func (s *Service) Get(ctx context.Context, id string) (*gantryv1.Experiment, error) {
	return s.store.Get(ctx, id)
}

// Update sets name/notes. name must be non-empty.
func (s *Service) Update(ctx context.Context, id, name, notes string) (*gantryv1.Experiment, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := s.store.UpdateMeta(ctx, id, name, notes); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, id)
}

// Delete removes an experiment (ErrNotFound if unknown).
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// newID returns a short random hex id from crypto/rand.
func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("experiments: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
