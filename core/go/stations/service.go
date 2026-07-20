// Package stations implements the hardware-checkout surface for Gantry: the
// registry of stations (tagged bundles of devices+sensors that are the unit of
// checkout), their derived availability, and the lease lifecycle that reserves a
// station for a run (see proto/gantry/v1/station.proto). It answers the four CI
// questions — what's connected, is it a valid target, can I check one out, and
// what's its status — over the Bench SQLite store. Mounted identically by Bench
// (its own stations) and Cloud (the pool across hosts).
package stations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// ErrInvalid is a bad request; ErrUnavailable means not enough stations to lease.
var (
	ErrInvalid     = errors.New("invalid station request")
	ErrUnavailable = errors.New("no matching station available")
)

const (
	idBytes           = 8
	defaultTTL        = 5 * time.Minute
	defaultStaleAfter = 60 * time.Second
)

// Service is the station registry + lease engine.
type Service struct {
	store      *Store
	hostID     string
	now        func() time.Time
	staleAfter time.Duration
	defaultTTL time.Duration
}

// Option configures a Service.
type Option func(*Service)

// WithHostID sets the bench host id stamped on locally-registered stations.
func WithHostID(id string) Option { return func(s *Service) { s.hostID = id } }

// WithClock overrides the clock (tests).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService builds a Service over an already-migrated *sql.DB.
func NewService(db *sql.DB, opts ...Option) *Service {
	s := &Service{store: NewStore(db), now: time.Now, staleAfter: defaultStaleAfter, defaultTTL: defaultTTL}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Register upserts a station and stamps its liveness. id is required.
func (s *Service) Register(ctx context.Context, in *gantryv1.Station) (*gantryv1.Station, error) {
	if in == nil || in.Id == "" {
		return nil, fmt.Errorf("%w: station id is required", ErrInvalid)
	}
	nowNs := uint64(s.now().UnixNano())
	if in.BenchHostId == "" {
		in.BenchHostId = s.hostID
	}
	if err := s.store.UpsertStation(ctx, in, nowNs, nowNs); err != nil {
		return nil, err
	}
	return s.Get(ctx, in.Id)
}

// Get returns one station hydrated with availability + active lease.
func (s *Service) Get(ctx context.Context, id string) (*gantryv1.Station, error) {
	st, err := s.store.GetStation(ctx, id)
	if err != nil {
		return nil, err
	}
	s.hydrate(ctx, st)
	return st, nil
}

// List returns stations matching the selector, each hydrated.
func (s *Service) List(ctx context.Context, selector string) ([]*gantryv1.Station, error) {
	sel, err := parseSelector(selector)
	if err != nil {
		return nil, err
	}
	all, err := s.store.ListStations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*gantryv1.Station, 0, len(all))
	for _, st := range all {
		if !matches(st.Tags, sel) {
			continue
		}
		s.hydrate(ctx, st)
		out = append(out, st)
	}
	return out, nil
}

// CheckTarget is a dry run: how many stations match the selector, are online,
// and are free — and whether that satisfies replicas.
func (s *Service) CheckTarget(ctx context.Context, selector string, replicas uint32) (*gantryv1.CheckTargetResponse, error) {
	if replicas == 0 {
		replicas = 1
	}
	list, err := s.List(ctx, selector)
	if err != nil {
		return nil, err
	}
	resp := &gantryv1.CheckTargetResponse{}
	for _, st := range list {
		online := st.Availability == gantryv1.Availability_AVAILABILITY_ONLINE
		free := st.Lease == nil
		v := &gantryv1.StationVerdict{StationId: st.Id, Matched: true, Online: online, Free: free}
		resp.Matched++
		if online {
			resp.Online++
		} else {
			v.Reasons = append(v.Reasons, "offline")
		}
		if free {
			if online {
				resp.Free++
			}
		} else {
			v.Reasons = append(v.Reasons, "leased by "+st.Lease.Holder)
		}
		resp.Stations = append(resp.Stations, v)
	}
	resp.Ok = resp.Free >= replicas
	resp.Detail = fmt.Sprintf("%d match, %d online, %d free (need %d)", resp.Matched, resp.Online, resp.Free, replicas)
	return resp, nil
}

// Lease reserves up to replicas matching, online, free stations. Idempotent on
// idempotency_key. Returns ErrUnavailable when too few are free.
func (s *Service) Lease(ctx context.Context, selector string, replicas uint32, holder, reason string, ttlSeconds uint32, idempotencyKey string) ([]*gantryv1.Lease, []*gantryv1.Station, error) {
	if replicas == 0 {
		replicas = 1
	}
	nowNs := uint64(s.now().UnixNano())
	if idempotencyKey != "" {
		if existing, err := s.store.ActiveLeasesByIdempotencyKey(ctx, idempotencyKey, nowNs); err == nil && len(existing) > 0 {
			return existing, s.stationsFor(ctx, existing), nil
		} else if err != nil {
			return nil, nil, err
		}
	}
	list, err := s.List(ctx, selector)
	if err != nil {
		return nil, nil, err
	}
	// Deterministic pick order (by id) among free+online candidates.
	free := make([]*gantryv1.Station, 0, len(list))
	for _, st := range list {
		if st.Availability == gantryv1.Availability_AVAILABILITY_ONLINE && st.Lease == nil {
			free = append(free, st)
		}
	}
	sort.Slice(free, func(i, j int) bool { return free[i].Id < free[j].Id })

	ttl := s.defaultTTL
	if ttlSeconds > 0 {
		ttl = time.Duration(ttlSeconds) * time.Second
	}
	now := s.now()
	acquired := uint64(now.UnixNano())
	expires := uint64(now.Add(ttl).UnixNano())

	// Try to atomically grant each candidate; a station taken by a concurrent
	// caller returns ErrTaken and is skipped. Walk ALL free candidates so a lost
	// race is retried against another station, not fatal.
	var leases []*gantryv1.Lease
	var stationsOut []*gantryv1.Station
	for _, st := range free {
		if uint32(len(leases)) >= replicas {
			break
		}
		l := &gantryv1.Lease{
			Id: mustID(), StationId: st.Id, Holder: holder, Reason: reason,
			AcquiredNs: acquired, ExpiresNs: expires,
		}
		key := ""
		if len(leases) == 0 {
			key = idempotencyKey // only one row can carry the unique key
		}
		switch err := s.store.GrantLease(ctx, l, key, acquired); {
		case err == nil:
			leases = append(leases, l)
			st.Lease = l
			stationsOut = append(stationsOut, st)
		case errors.Is(err, ErrTaken):
			continue
		default:
			return nil, nil, err
		}
	}
	if uint32(len(leases)) < replicas {
		// Roll back a partial multi-station grant so we never strand stations.
		for _, l := range leases {
			_ = s.store.ReleaseLease(ctx, l.Id)
		}
		return nil, nil, fmt.Errorf("%w: leased %d of %d needed (selector %q)", ErrUnavailable, len(leases), replicas, selector)
	}
	return leases, stationsOut, nil
}

// Renew extends a lease's TTL.
func (s *Service) Renew(ctx context.Context, leaseID string, ttlSeconds uint32) (*gantryv1.Lease, error) {
	if leaseID == "" {
		return nil, fmt.Errorf("%w: lease_id is required", ErrInvalid)
	}
	ttl := s.defaultTTL
	if ttlSeconds > 0 {
		ttl = time.Duration(ttlSeconds) * time.Second
	}
	now := s.now()
	expires := uint64(now.Add(ttl).UnixNano())
	if err := s.store.RenewLease(ctx, leaseID, expires, uint64(now.UnixNano())); err != nil {
		return nil, err
	}
	l, err := s.store.GetLease(ctx, leaseID)
	if err != nil {
		return nil, err
	}
	return l, nil
}

// Release frees a station by releasing its lease.
func (s *Service) Release(ctx context.Context, leaseID string) error {
	if leaseID == "" {
		return fmt.Errorf("%w: lease_id is required", ErrInvalid)
	}
	return s.store.ReleaseLease(ctx, leaseID)
}

// hydrate sets a station's derived availability and attaches any active lease.
func (s *Service) hydrate(ctx context.Context, st *gantryv1.Station) {
	nowNs := uint64(s.now().UnixNano())
	st.Availability = s.availability(st.LastSeenNs, nowNs)
	if l, err := s.store.ActiveLease(ctx, st.Id, nowNs); err == nil {
		st.Lease = l
	}
}

// availability derives ONLINE/OFFLINE from liveness. DEGRADED is reserved for a
// later milestone (partial-subsystem health).
func (s *Service) availability(lastSeenNs, nowNs uint64) gantryv1.Availability {
	if lastSeenNs == 0 || nowNs-lastSeenNs > uint64(s.staleAfter.Nanoseconds()) {
		return gantryv1.Availability_AVAILABILITY_OFFLINE
	}
	return gantryv1.Availability_AVAILABILITY_ONLINE
}

func (s *Service) stationsFor(ctx context.Context, leases []*gantryv1.Lease) []*gantryv1.Station {
	out := make([]*gantryv1.Station, 0, len(leases))
	for _, l := range leases {
		if st, err := s.Get(ctx, l.StationId); err == nil {
			out = append(out, st)
		}
	}
	return out
}

func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("stations: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func mustID() string {
	id, err := newID()
	if err != nil {
		panic(err)
	}
	return id
}
