package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrNotFound is returned when a token id does not exist.
var ErrNotFound = errors.New("token not found")

// lastUsedThrottle bounds how often a token's last_used_ns is written back:
// updating on every request would turn every authenticated read into a DB write
// and thrash the single SQLite writer. Once per minute is plenty to answer
// "when was this token last used" in the console.
const lastUsedThrottle = time.Minute

// Store is the persistence layer for tokens over the Bench SQLite database
// (core/go/benchdb), same shape as workspaces/hardware (SQLite now, Postgres
// later). It also satisfies Verifier: Verify parses a bearer string, looks the
// row up by id, and constant-time compares the secret hash. now is injectable so
// tests control the last-used throttle clock.
type Store struct {
	db  *sql.DB
	now func() time.Time

	mu       sync.Mutex
	lastSeen map[string]time.Time // token id → last time we wrote last_used_ns
}

// NewStore builds a Store over an already-migrated *sql.DB.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now, lastSeen: map[string]time.Time{}}
}

// Create inserts a new token row. The caller (Service) has already generated the
// id/hash and validated name+scopes.
func (s *Store) Create(ctx context.Context, id, name string, hash []byte, scopes []string, createdNs int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (id, name, secret_hash, scopes, created_ns, last_used_ns)
		 VALUES (?, ?, ?, ?, ?, 0)`,
		id, name, hash, EncodeScopes(scopes), createdNs)
	if err != nil {
		return fmt.Errorf("auth: create token: %w", err)
	}
	return nil
}

// List returns all token metadata, newest-first. Secret hashes are never
// returned (the proto TokenInfo has no secret field; see auth.proto).
func (s *Store) List(ctx context.Context) ([]*gantryv1.TokenInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, scopes, created_ns, last_used_ns
		 FROM tokens ORDER BY created_ns DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	defer rows.Close()

	var out []*gantryv1.TokenInfo
	for rows.Next() {
		var (
			id, name, scopes      string
			createdNs, lastUsedNs int64
		)
		if err := rows.Scan(&id, &name, &scopes, &createdNs, &lastUsedNs); err != nil {
			return nil, fmt.Errorf("auth: list scan: %w", err)
		}
		out = append(out, &gantryv1.TokenInfo{
			Id:         id,
			Name:       name,
			Scopes:     DecodeScopes(scopes),
			CreatedNs:  uint64(createdNs),
			LastUsedNs: uint64(lastUsedNs),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list rows: %w", err)
	}
	return out, nil
}

// Delete removes a token by id (immediate revocation). ErrNotFound if unknown.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("auth: delete token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	// Drop any throttle bookkeeping so a re-minted id starts clean.
	s.mu.Lock()
	delete(s.lastSeen, id)
	s.mu.Unlock()
	return nil
}

// lookup fetches the stored hash + scopes for a token id, or ErrNotFound.
func (s *Store) lookup(ctx context.Context, id string) (hash []byte, scopes []string, err error) {
	var (
		h      []byte
		scopeS string
	)
	row := s.db.QueryRowContext(ctx, `SELECT secret_hash, scopes FROM tokens WHERE id = ?`, id)
	switch err := row.Scan(&h, &scopeS); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil, ErrNotFound
	case err != nil:
		return nil, nil, fmt.Errorf("auth: lookup token: %w", err)
	}
	return h, DecodeScopes(scopeS), nil
}

// Verify implements Verifier. It parses the bearer string, looks the row up by
// its embedded id, and constant-time compares the secret hash. On success it
// returns the granted scopes and (throttled) stamps last_used_ns. A malformed,
// unknown, or wrong-secret token all return ErrInvalidToken so callers cannot
// distinguish them (no oracle for "this id exists").
func (s *Store) Verify(ctx context.Context, bearer string) (*Grant, error) {
	id, secretHash, err := parseToken(bearer)
	if err != nil {
		return nil, ErrInvalidToken
	}
	storedHash, scopes, err := s.lookup(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if !hashesEqual(secretHash, storedHash) {
		return nil, ErrInvalidToken
	}
	s.touch(ctx, id)
	return &Grant{TokenID: id, Scopes: scopes}, nil
}

// touch writes last_used_ns for id at most once per lastUsedThrottle. The write
// is best-effort: a failure to record "last used" must not fail an otherwise
// valid request, so the error is swallowed (the row is still valid; we just
// didn't update a cosmetic timestamp).
func (s *Store) touch(ctx context.Context, id string) {
	now := s.now()
	s.mu.Lock()
	last, ok := s.lastSeen[id]
	if ok && now.Sub(last) < lastUsedThrottle {
		s.mu.Unlock()
		return
	}
	s.lastSeen[id] = now
	s.mu.Unlock()

	_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_ns = ? WHERE id = ?`, now.UnixNano(), id)
}
