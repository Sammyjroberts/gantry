package experiments_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/edgedb"
	"github.com/Sammyjroberts/gantry/libs/go/experiments"
)

func newSvc(t *testing.T) (*experiments.Service, *sql.DB) {
	t.Helper()
	db, err := edgedb.Open(context.Background(), filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatalf("edgedb.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return experiments.NewService(db), db
}

// TestLifecycle exercises start → get → update → stop → list → delete.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	// Start with default (now) start_ns.
	before := uint64(time.Now().UnixNano())
	e, err := svc.Start(ctx, "climb test", "notes here", "rover-1", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.Id == "" {
		t.Fatal("Start returned empty id")
	}
	if e.StartNs < before {
		t.Fatalf("default start_ns %d predates call time %d", e.StartNs, before)
	}
	if e.EndNs != 0 {
		t.Fatalf("new experiment end_ns = %d, want 0 (running)", e.EndNs)
	}

	// Get round-trips.
	got, err := svc.Get(ctx, e.Id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "climb test" || got.Notes != "notes here" || got.DeviceId != "rover-1" {
		t.Fatalf("Get mismatch: %+v", got)
	}

	// Update name/notes.
	up, err := svc.Update(ctx, e.Id, "climb test v2", "revised")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if up.Name != "climb test v2" || up.Notes != "revised" {
		t.Fatalf("Update not applied: %+v", up)
	}

	// Stop (now).
	stopped, err := svc.Stop(ctx, e.Id, 0)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.EndNs <= stopped.StartNs {
		t.Fatalf("end_ns %d not after start_ns %d", stopped.EndNs, stopped.StartNs)
	}

	// Stopping again fails (not running).
	if _, err := svc.Stop(ctx, e.Id, 0); !errors.Is(err, experiments.ErrNotRunning) {
		t.Fatalf("second Stop err = %v, want ErrNotRunning", err)
	}

	// List returns it.
	list, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Id != e.Id {
		t.Fatalf("List = %+v, want the one experiment", list)
	}

	// Delete, then Get fails.
	if err := svc.Delete(ctx, e.Id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, e.Id); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
}

// TestBackdatedStartAndExplicitStop covers marking a range after the fact.
func TestBackdatedStartAndExplicitStop(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	start := uint64(time.Now().Add(-time.Hour).UnixNano())
	end := start + uint64(30*time.Minute)

	e, err := svc.Start(ctx, "backdated", "", "", start)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if e.StartNs != start {
		t.Fatalf("start_ns = %d, want explicit %d", e.StartNs, start)
	}
	stopped, err := svc.Stop(ctx, e.Id, end)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stopped.EndNs != end {
		t.Fatalf("end_ns = %d, want explicit %d", stopped.EndNs, end)
	}
}

// TestValidation covers the rejected requests.
func TestValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	if _, err := svc.Start(ctx, "", "", "", 0); !errors.Is(err, experiments.ErrInvalid) {
		t.Fatalf("empty-name Start err = %v, want ErrInvalid", err)
	}

	// end <= start is rejected.
	e, err := svc.Start(ctx, "v", "", "", 1_000_000)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.Stop(ctx, e.Id, 500_000); !errors.Is(err, experiments.ErrInvalid) {
		t.Fatalf("backwards Stop err = %v, want ErrInvalid", err)
	}

	// Unknown ids.
	if _, err := svc.Get(ctx, "nope"); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Get unknown err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Stop(ctx, "nope", 0); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Stop unknown err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, "nope", "x", ""); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Update unknown err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, "nope"); !errors.Is(err, experiments.ErrNotFound) {
		t.Fatalf("Delete unknown err = %v, want ErrNotFound", err)
	}
}

// TestListDeviceFilterAndOrder checks the device filter and newest-first order.
func TestListDeviceFilterAndOrder(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	base := uint64(time.Now().UnixNano())
	// Three experiments, ascending start times, two devices.
	a, _ := svc.Start(ctx, "a", "", "dev-1", base+1)
	b, _ := svc.Start(ctx, "b", "", "dev-2", base+2)
	c, _ := svc.Start(ctx, "c", "", "dev-1", base+3)

	all, err := svc.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	// Newest first: c, b, a.
	if len(all) != 3 || all[0].Id != c.Id || all[1].Id != b.Id || all[2].Id != a.Id {
		t.Fatalf("List order wrong: %v", ids(all))
	}

	dev1, err := svc.List(ctx, "dev-1")
	if err != nil {
		t.Fatalf("List dev-1: %v", err)
	}
	if len(dev1) != 2 || dev1[0].Id != c.Id || dev1[1].Id != a.Id {
		t.Fatalf("device filter wrong: %v", ids(dev1))
	}
}

// TestConcurrentStarts proves id generation and inserts are safe under
// concurrency: N goroutines each start an experiment; all succeed with unique ids.
func TestConcurrentStarts(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	const n = 50
	var wg sync.WaitGroup
	ids := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := svc.Start(ctx, "concurrent", "", "", 0)
			if err != nil {
				errs <- err
				return
			}
			ids <- e.Id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)

	if err := <-errs; err != nil {
		t.Fatalf("concurrent Start error: %v", err)
	}
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d unique ids, want %d", len(seen), n)
	}

	list, _ := svc.List(ctx, "")
	if len(list) != n {
		t.Fatalf("persisted %d experiments, want %d", len(list), n)
	}
}

// TestConcurrentStop proves exactly one of many racing Stops wins.
func TestConcurrentStop(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	e, err := svc.Start(ctx, "race", "", "", uint64(time.Now().Add(-time.Minute).UnixNano()))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	var okCount, notRunning int
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Stop(ctx, e.Id, 0)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, experiments.ErrNotRunning):
				notRunning++
			default:
				t.Errorf("unexpected Stop error: %v", err)
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("winning Stops = %d, want exactly 1 (notRunning=%d)", okCount, notRunning)
	}
}

func ids(es []*gantryv1.Experiment) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Id
	}
	return out
}
