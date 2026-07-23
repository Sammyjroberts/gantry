package stations_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
	"github.com/Sammyjroberts/gantry/core/go/stations"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newSvc(t *testing.T) (*stations.Service, *clock) {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return stations.NewService(db, stations.WithHostID("bench-local"), stations.WithClock(c.now)), c
}

func register(t *testing.T, svc *stations.Service, id string, tags map[string]string) {
	t.Helper()
	if _, err := svc.Register(context.Background(), &gantryv1.Station{Id: id, Tags: tags, DeviceIds: []string{id + "-arm"}}); err != nil {
		t.Fatalf("Register %s: %v", id, err)
	}
}

func TestRegisterDiscoverAvailability(t *testing.T) {
	ctx := context.Background()
	svc, clk := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101", "camera": "topdown"})

	st, err := svc.Get(ctx, "cell-01")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if st.BenchHostId != "bench-local" || st.Availability != gantryv1.Availability_AVAILABILITY_ONLINE {
		t.Fatalf("station not online/hosted: %+v", st)
	}

	// Selector filtering.
	if got, _ := svc.List(ctx, "arm=so101"); len(got) != 1 {
		t.Fatalf("selector arm=so101 matched %d", len(got))
	}
	if got, _ := svc.List(ctx, "arm=ur5"); len(got) != 0 {
		t.Fatalf("selector arm=ur5 matched %d, want 0", len(got))
	}

	// Goes OFFLINE once liveness is stale.
	clk.advance(2 * time.Minute)
	st, _ = svc.Get(ctx, "cell-01")
	if st.Availability != gantryv1.Availability_AVAILABILITY_OFFLINE {
		t.Fatalf("stale station should be OFFLINE, got %v", st.Availability)
	}
}

func TestCheckTargetAndLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101", "camera": "topdown"})

	// Valid target before committing a CI slot.
	chk, err := svc.CheckTarget(ctx, "arm=so101,camera=topdown", 1)
	if err != nil {
		t.Fatalf("CheckTarget: %v", err)
	}
	if !chk.Ok || chk.Matched != 1 || chk.Online != 1 || chk.Free != 1 {
		t.Fatalf("check target = %+v", chk)
	}

	// Check it out.
	leases, sts, err := svc.Lease(ctx, "arm=so101", 1, "run-42", "eval", 0, "idem-1")
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if len(leases) != 1 || len(sts) != 1 || sts[0].Lease == nil {
		t.Fatalf("lease result = %+v / %+v", leases, sts)
	}

	// Now it is not a free target.
	chk, _ = svc.CheckTarget(ctx, "arm=so101", 1)
	if chk.Ok || chk.Free != 0 {
		t.Fatalf("leased station should not be free: %+v", chk)
	}
	// A second lessee is refused.
	if _, _, err := svc.Lease(ctx, "arm=so101", 1, "run-99", "eval", 0, ""); !errors.Is(err, stations.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable for a taken station, got %v", err)
	}
	// Idempotent re-lease returns the same lease.
	again, _, err := svc.Lease(ctx, "arm=so101", 1, "run-42", "eval", 0, "idem-1")
	if err != nil || len(again) != 1 || again[0].Id != leases[0].Id {
		t.Fatalf("idempotent lease: %v / %+v", err, again)
	}

	// Renew extends the expiry.
	renewed, err := svc.Renew(ctx, leases[0].Id, 600)
	if err != nil || renewed.ExpiresNs <= leases[0].ExpiresNs {
		t.Fatalf("renew did not extend: %v / %d vs %d", err, renewed.ExpiresNs, leases[0].ExpiresNs)
	}

	// Release frees it again.
	if err := svc.Release(ctx, leases[0].Id); err != nil {
		t.Fatalf("Release: %v", err)
	}
	chk, _ = svc.CheckTarget(ctx, "arm=so101", 1)
	if !chk.Ok || chk.Free != 1 {
		t.Fatalf("released station should be free again: %+v", chk)
	}
}

func TestLeaseExpiryFreesStation(t *testing.T) {
	ctx := context.Background()
	svc, clk := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101"})
	if _, _, err := svc.Lease(ctx, "arm=so101", 1, "run", "", 1, ""); err != nil {
		t.Fatalf("Lease: %v", err)
	}
	// Past the 1s TTL the lease is no longer active → the station is free.
	clk.advance(2 * time.Second)
	chk, _ := svc.CheckTarget(ctx, "arm=so101", 1)
	if !chk.Ok || chk.Free != 1 {
		t.Fatalf("expired lease should free the station: %+v", chk)
	}
}

func TestLeaseExclusiveUnderConcurrency(t *testing.T) {
	// Regression for finding #1: N callers racing for one free station must yield
	// exactly one active lease — the DB unique index, not an app check, enforces it.
	ctx := context.Background()
	svc, _ := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101"})

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			leases, _, err := svc.Lease(ctx, "arm=so101", 1, "h", "", 0, "")
			if err == nil && len(leases) == 1 {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if err != nil && !errors.Is(err, stations.ErrUnavailable) {
				t.Errorf("unexpected lease error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("exactly one caller should win the station, got %d", wins)
	}
	if chk, _ := svc.CheckTarget(ctx, "arm=so101", 1); chk.Free != 0 {
		t.Fatalf("station should be taken after the storm: %+v", chk)
	}
}

func TestRenewRefusesExpiredLease(t *testing.T) {
	// Regression for finding #8.
	ctx := context.Background()
	svc, clk := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101"})
	leases, _, err := svc.Lease(ctx, "arm=so101", 1, "h", "", 1, "") // 1s TTL
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	clk.advance(2 * time.Second) // lease now expired
	if _, err := svc.Renew(ctx, leases[0].Id, 600); err == nil {
		t.Fatal("renewing an expired lease must fail, not resurrect it")
	}
}

func TestLeaseMultipleReplicas(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)
	register(t, svc, "cell-01", map[string]string{"arm": "so101"})
	register(t, svc, "cell-02", map[string]string{"arm": "so101"})
	register(t, svc, "cell-03", map[string]string{"arm": "so101"})

	leases, _, err := svc.Lease(ctx, "arm=so101", 2, "run", "eval", 0, "")
	if err != nil || len(leases) != 2 {
		t.Fatalf("lease 2 of 3: %v / %d", err, len(leases))
	}
	// One remains free; asking for 2 more must fail.
	if _, _, err := svc.Lease(ctx, "arm=so101", 2, "run2", "eval", 0, ""); !errors.Is(err, stations.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable when only 1 free, got %v", err)
	}
}
