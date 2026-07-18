package source

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/foxglove"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// Source status states, mirrored in proto/gantry/v1/source.proto SourceStatus.
const (
	stateDisabled   = "disabled"
	stateConnecting = "connecting"
	stateConnected  = "connected"
	stateBackoff    = "backoff"
)

// Backoff bounds for the reconnect loop (same shape as the SO-101 bridge and the
// Python tap): first retry after backoffStart, doubling to backoffCap.
const (
	defaultBackoffStart = 500 * time.Millisecond
	defaultBackoffCap   = 5 * time.Second
)

// SupervisorOption tweaks a Supervisor (tests shrink the backoff and swap logf).
type SupervisorOption func(*Supervisor)

// WithBackoff overrides the reconnect backoff start/cap (tests use tiny values
// so a reconnect assertion doesn't wait seconds).
func WithBackoff(start, cap time.Duration) SupervisorOption {
	return func(s *Supervisor) {
		if start > 0 {
			s.backoffStart = start
		}
		if cap > 0 {
			s.backoffCap = cap
		}
	}
}

// WithLogf overrides the log sink (defaults to log.Printf).
func WithLogf(logf func(string, ...any)) SupervisorOption {
	return func(s *Supervisor) { s.logf = logf }
}

// Supervisor connects and maintains one in-process Foxglove client per ENABLED
// source. It reconciles the desired set (enabled rows in the Store) against the
// running set: starting a client for a newly-enabled source, stopping one for a
// disabled/deleted source, and restarting one whose url or mapping changed. Each
// client runs in its own goroutine that reconnects with backoff on drop; live
// status (state/detail/last_frame_ns/frames_ingested/reconnects) is held in
// memory and returned by StatusFor.
type Supervisor struct {
	store    *Store
	ingestor foxglove.Ingestor

	backoffStart time.Duration
	backoffCap   time.Duration
	logf         func(string, ...any)

	mu       sync.Mutex
	bgCtx    context.Context
	bgCancel context.CancelFunc
	runners  map[string]*runner
	wg       sync.WaitGroup
}

// NewSupervisor builds a supervisor over the source store and an ingest sink.
// It does not connect anything until Start is called.
func NewSupervisor(store *Store, ingestor foxglove.Ingestor, opts ...SupervisorOption) *Supervisor {
	s := &Supervisor{
		store:        store,
		ingestor:     ingestor,
		backoffStart: defaultBackoffStart,
		backoffCap:   defaultBackoffCap,
		logf:         log.Printf,
		runners:      make(map[string]*runner),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// sourceSpec is the connection-relevant slice of a source; a change in it forces
// a running client to restart.
type sourceSpec struct {
	url     string
	mapping string
}

// runner owns one source's reconnect goroutine and its live status.
type runner struct {
	id     string
	spec   sourceSpec
	cancel context.CancelFunc
	status *statusState
}

// Start records the supervisor's background context (children of it are each
// runner's context, so Stop cancels them all) and performs the initial
// reconcile, connecting every currently-enabled source. Safe to call once.
func (sv *Supervisor) Start(ctx context.Context) error {
	sv.mu.Lock()
	if sv.bgCtx != nil {
		sv.mu.Unlock()
		return nil
	}
	sv.bgCtx, sv.bgCancel = context.WithCancel(ctx)
	sv.mu.Unlock()
	return sv.Reconcile(ctx)
}

// Reconcile diffs the enabled sources in the store against the running clients,
// starting/stopping/restarting as needed. It is called on Start and after every
// UpsertSource/DeleteSource so a checkbox toggle takes effect promptly. ctx is
// used only for the store read; runner goroutines derive from the supervisor's
// background context so they outlive the triggering request.
func (sv *Supervisor) Reconcile(ctx context.Context) error {
	srcs, err := sv.store.List(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]sourceSpec, len(srcs))
	for _, s := range srcs {
		if s.Enabled {
			desired[s.Id] = sourceSpec{url: s.Url, mapping: s.MappingJson}
		}
	}

	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.bgCtx == nil {
		return nil // not started yet
	}
	// Stop runners that are no longer desired, or whose spec changed.
	for id, r := range sv.runners {
		if want, ok := desired[id]; !ok || want != r.spec {
			r.cancel()
			delete(sv.runners, id)
		}
	}
	// Start runners for desired sources not already running.
	for id, spec := range desired {
		if _, ok := sv.runners[id]; ok {
			continue
		}
		sv.startRunnerLocked(id, spec)
	}
	return nil
}

// startRunnerLocked launches a reconnect goroutine for one source. Caller holds
// sv.mu.
func (sv *Supervisor) startRunnerLocked(id string, spec sourceSpec) {
	rctx, cancel := context.WithCancel(sv.bgCtx)
	r := &runner{
		id:     id,
		spec:   spec,
		cancel: cancel,
		status: &statusState{state: stateConnecting},
	}
	sv.runners[id] = r
	sv.wg.Add(1)
	go func() {
		defer sv.wg.Done()
		sv.runLoop(rctx, r)
	}()
}

// runLoop is one source's connect→run→backoff→reconnect cycle until its context
// is cancelled (source disabled/deleted/changed, or supervisor shutdown).
func (sv *Supervisor) runLoop(ctx context.Context, r *runner) {
	backoff := sv.backoffStart
	for {
		if ctx.Err() != nil {
			return
		}
		r.status.set(stateConnecting, "")

		connected := false
		mapping, err := foxglove.LoadMapping(r.spec.mapping)
		if err == nil {
			client := foxglove.NewClient(r.spec.url, mapping, sv.ingestor, foxglove.Options{
				OnConnect: func() {
					connected = true
					r.status.set(stateConnected, "")
				},
				OnIngest: r.status.ingest,
				Logf:     sv.logf,
			})
			err = client.Run(ctx)
		}
		if ctx.Err() != nil {
			return // intentional stop; don't churn status
		}
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		r.status.set(stateBackoff, detail)

		// A session that actually connected resets the backoff, so a long-lived
		// connection that later drops reconnects promptly rather than at the cap.
		if connected {
			backoff = sv.backoffStart
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		r.status.incReconnect()
		backoff = min(backoff*2, sv.backoffCap)
	}
}

// StatusFor returns a SourceStatus for each source in the same order, using the
// live in-memory status for running sources and a "disabled" placeholder for the
// rest.
func (sv *Supervisor) StatusFor(sources []*gantryv1.Source) []*gantryv1.SourceStatus {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	out := make([]*gantryv1.SourceStatus, 0, len(sources))
	for _, s := range sources {
		if r, ok := sv.runners[s.Id]; ok {
			out = append(out, r.status.snapshot(s.Id))
		} else {
			out = append(out, &gantryv1.SourceStatus{Id: s.Id, State: stateDisabled})
		}
	}
	return out
}

// Stop cancels every runner and waits for the goroutines to drain (bounded by
// ctx). After Stop the supervisor is inert.
func (sv *Supervisor) Stop(ctx context.Context) error {
	sv.mu.Lock()
	if sv.bgCancel != nil {
		sv.bgCancel()
	}
	sv.mu.Unlock()

	done := make(chan struct{})
	go func() {
		sv.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// statusState is a source's mutable live status, guarded by its own mutex so the
// runner goroutine (writer) and StatusFor (reader) never race.
type statusState struct {
	mu          sync.Mutex
	state       string
	detail      string
	lastFrameNs uint64
	frames      uint64
	reconnects  uint64
}

func (s *statusState) set(state, detail string) {
	s.mu.Lock()
	s.state = state
	s.detail = detail
	s.mu.Unlock()
}

// ingest folds one published batch into the counters. It also promotes the state
// to "connected" defensively (frames only flow on a live connection).
func (s *statusState) ingest(frames int, lastLogNs uint64) {
	s.mu.Lock()
	s.frames += uint64(frames)
	if lastLogNs > s.lastFrameNs {
		s.lastFrameNs = lastLogNs
	}
	if s.state != stateConnected {
		s.state = stateConnected
		s.detail = ""
	}
	s.mu.Unlock()
}

func (s *statusState) incReconnect() {
	s.mu.Lock()
	s.reconnects++
	s.mu.Unlock()
}

func (s *statusState) snapshot(id string) *gantryv1.SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &gantryv1.SourceStatus{
		Id:             id,
		State:          s.state,
		Detail:         s.detail,
		LastFrameNs:    s.lastFrameNs,
		FramesIngested: s.frames,
		Reconnects:     s.reconnects,
	}
}
