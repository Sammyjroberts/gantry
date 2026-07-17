package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrInvalid is a bad token-management request (empty name, no/unknown scopes).
// The ConnectRPC handler maps it to InvalidArgument.
var ErrInvalid = errors.New("invalid token request")

// Service is the token CRUD engine: validate, mint (id+secret+hash), store, and
// project to the wire type. It owns policy; the Store owns SQL. now is injectable
// for deterministic created_ns in tests.
type Service struct {
	store *Store
	now   func() time.Time
}

// NewService builds a Service over an already-migrated *sql.DB.
func NewService(db *sql.DB) *Service {
	s := NewStore(db)
	return &Service{store: s, now: time.Now}
}

// NewServiceWithStore builds a Service over an existing Store (so the same Store
// backs both the Service and the middleware Verifier — one throttle map, one
// clock).
func NewServiceWithStore(store *Store) *Service {
	return &Service{store: store, now: time.Now}
}

// Store exposes the underlying store (the middleware verifies against the same
// instance).
func (s *Service) Store() *Store { return s.store }

// List returns all token metadata (no secrets).
func (s *Service) List(ctx context.Context) ([]*gantryv1.TokenInfo, error) {
	return s.store.List(ctx)
}

// Create validates name+scopes, mints a token, stores its hash, and returns the
// stored metadata together with the full secret string — which is returned
// exactly once and can never be retrieved again.
func (s *Service) Create(ctx context.Context, name string, scopes []string) (*gantryv1.TokenInfo, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("%w: name is required", ErrInvalid)
	}
	norm, err := NormalizeScopes(scopes)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(norm) == 0 {
		return nil, "", fmt.Errorf("%w: at least one scope is required", ErrInvalid)
	}

	id, secret, hash, err := NewToken()
	if err != nil {
		return nil, "", err
	}
	createdNs := s.now().UnixNano()
	if err := s.store.Create(ctx, id, name, hash, norm, createdNs); err != nil {
		return nil, "", err
	}
	info := &gantryv1.TokenInfo{
		Id:         id,
		Name:       name,
		Scopes:     norm,
		CreatedNs:  uint64(createdNs),
		LastUsedNs: 0,
	}
	return info, secret, nil
}

// Delete revokes a token immediately.
func (s *Service) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalid)
	}
	return s.store.Delete(ctx, id)
}
