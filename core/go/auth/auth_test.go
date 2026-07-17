package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/core/go/benchdb"
)

// ---- token format / parse / verify ----

func TestNewTokenShapeAndParse(t *testing.T) {
	id, full, hash, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(id) != idHexLen || !isHex(id) {
		t.Fatalf("id = %q, want %d hex chars", id, idHexLen)
	}
	if !strings.HasPrefix(full, "gtk_"+id+"_") {
		t.Fatalf("token %q missing gtk_<id>_ prefix", full)
	}
	if len(hash) != 32 {
		t.Fatalf("hash len = %d, want 32 (sha-256)", len(hash))
	}
	// Parse recovers the id and a hash that matches the stored one.
	gotID, gotHash, err := parseToken(full)
	if err != nil {
		t.Fatalf("parseToken: %v", err)
	}
	if gotID != id {
		t.Fatalf("parsed id = %q, want %q", gotID, id)
	}
	if !hashesEqual(gotHash, hash) {
		t.Fatalf("parsed hash does not match stored hash (constant-time compare failed)")
	}
}

func TestParseTokenMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"nope",
		"gtk_",
		"gtk_short_sec",       // id not 8 hex
		"gtk_zzzzzzzz_sec",    // id not hex
		"gtk_1234abcd_",       // empty secret
		"pat_1234abcd_secret", // wrong prefix
		"1234abcd_secret",     // no prefix
	} {
		if _, _, err := parseToken(s); err != ErrMalformedToken {
			t.Errorf("parseToken(%q) err = %v, want ErrMalformedToken", s, err)
		}
	}
}

// ---- scope validation & route map ----

func TestNormalizeScopes(t *testing.T) {
	got, err := NormalizeScopes([]string{"operate", "read", "read", " admin "})
	if err != nil {
		t.Fatalf("NormalizeScopes: %v", err)
	}
	want := []string{"read", "operate", "admin"} // canonical order, deduped, trimmed
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if _, err := NormalizeScopes([]string{"read", "bogus"}); err == nil {
		t.Fatalf("expected error for unknown scope")
	}
	empty, err := NormalizeScopes([]string{"  "})
	if err != nil || len(empty) != 0 {
		t.Fatalf("blank scopes: got %v, err %v", empty, err)
	}
}

func TestRequiredScope(t *testing.T) {
	cases := []struct {
		method, path string
		scope        string
		ok           bool
	}{
		{"POST", "/gantry.v1.IngestService/PublishBatch", ScopeIngest, true},
		{"POST", "/gantry.v1.LiveService/Subscribe", ScopeRead, true},
		{"POST", "/gantry.v1.QueryService/QueryRange", ScopeRead, true},
		{"POST", "/gantry.v1.ExperimentService/StartExperiment", ScopeOperate, true},
		{"POST", "/gantry.v1.WorkspaceService/UpsertWorkspace", ScopeOperate, true},
		{"POST", "/gantry.v1.HardwareService/UpsertHardware", ScopeAdmin, true},
		{"POST", "/gantry.v1.TokenService/CreateToken", ScopeAdmin, true},
		{"GET", "/export/experiments/abc.csv", ScopeRead, true},
		{"POST", "/sql", ScopeRead, true},
		{"POST", "/mcp", ScopeRead, true},
		{"GET", "/video/chunks/xyz", ScopeRead, true},
		{"POST", "/video/chunks", ScopeOperate, true},
		{"GET", "/models/", ScopeRead, true},
		{"GET", "/models/mr-wobbles.urdf", ScopeRead, true},
		{"PUT", "/models/mr-wobbles.urdf", ScopeAdmin, true},
		// Open routes (SPA/static).
		{"GET", "/", "", false},
		{"GET", "/assets/app.js", "", false},
		{"GET", "/index.html", "", false},
	}
	for _, c := range cases {
		scope, ok := RequiredScope(c.method, c.path)
		if ok != c.ok || scope != c.scope {
			t.Errorf("RequiredScope(%s %s) = (%q,%v), want (%q,%v)", c.method, c.path, scope, ok, c.scope, c.ok)
		}
	}
}

// ---- middleware matrix ----

type fakeVerifier struct {
	grant *Grant
	err   error
	calls int
}

func (f *fakeVerifier) Verify(_ context.Context, _ string) (*Grant, error) {
	f.calls++
	return f.grant, f.err
}

// echoNext records the internal scopes header it received and returns 200.
func echoNext(seen *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Get(ScopesHeader)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func req(method, path, remote, bearer string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = remote
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

const (
	loopback    = "127.0.0.1:5555"
	loopback6   = "[::1]:5555"
	nonLoopback = "203.0.113.7:5555"
)

func TestMiddlewareLoopbackTrust(t *testing.T) {
	var seen string
	fv := &fakeVerifier{}
	h := Middleware(fv, false, echoNext(&seen))
	for _, remote := range []string{loopback, loopback6} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req("POST", "/gantry.v1.HardwareService/UpsertHardware", remote, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback %s admin route = %d, want 200", remote, rec.Code)
		}
	}
	if fv.calls != 0 {
		t.Fatalf("verifier called %d times for loopback (want 0)", fv.calls)
	}
	// Loopback trust hands all four scopes downstream (so MCP write tools work).
	if seen != EncodeScopes(AllScopes()) {
		t.Fatalf("downstream scopes = %q, want all scopes", seen)
	}
}

func TestMiddlewareNoToken401(t *testing.T) {
	h := Middleware(&fakeVerifier{}, false, echoNext(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("POST", "/gantry.v1.LiveService/Subscribe", nonLoopback, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token = %d, want 401", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing Bearer challenge: %q", rec.Header().Get("WWW-Authenticate"))
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body["hint"], "Access tokens") {
		t.Fatalf("hint = %q, want mention of Access tokens", body["hint"])
	}
}

func TestMiddlewareBadToken401(t *testing.T) {
	fv := &fakeVerifier{err: ErrInvalidToken}
	h := Middleware(fv, false, echoNext(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("POST", "/gantry.v1.LiveService/Subscribe", nonLoopback, "gtk_deadbeef_bad"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-token = %d, want 401", rec.Code)
	}
}

func TestMiddlewareWrongScope403NamesScope(t *testing.T) {
	fv := &fakeVerifier{grant: &Grant{TokenID: "abcd1234", Scopes: []string{ScopeRead}}}
	h := Middleware(fv, false, echoNext(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("POST", "/gantry.v1.HardwareService/UpsertHardware", nonLoopback, "gtk_abcd1234_x"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong-scope = %d, want 403", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["required_scope"] != ScopeAdmin {
		t.Fatalf("required_scope = %q, want admin", body["required_scope"])
	}
}

func TestMiddlewareValidTokenPassesAndSetsScopes(t *testing.T) {
	var seen string
	fv := &fakeVerifier{grant: &Grant{TokenID: "abcd1234", Scopes: []string{ScopeRead}}}
	h := Middleware(fv, false, echoNext(&seen))
	rec := httptest.NewRecorder()
	// Client tries to spoof escalated scopes via the internal header; it must be
	// stripped and replaced by the real granted scopes.
	r := req("POST", "/gantry.v1.LiveService/Subscribe", nonLoopback, "gtk_abcd1234_x")
	r.Header.Set(ScopesHeader, "admin operate")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid read token on read route = %d, want 200", rec.Code)
	}
	if seen != ScopeRead {
		t.Fatalf("downstream scopes = %q, want %q (spoofed header must be dropped)", seen, ScopeRead)
	}
}

func TestMiddlewareOptionsPreflightPasses(t *testing.T) {
	h := Middleware(&fakeVerifier{}, false, echoNext(nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("OPTIONS", "/gantry.v1.HardwareService/UpsertHardware", nonLoopback, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS preflight = %d, want 200 (passed to next unauthenticated)", rec.Code)
	}
}

func TestMiddlewareParanoidLoopbackNeedsToken(t *testing.T) {
	h := Middleware(&fakeVerifier{}, true, echoNext(nil)) // requireAuth=true
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("POST", "/gantry.v1.LiveService/Subscribe", loopback, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("paranoid loopback no-token = %d, want 401", rec.Code)
	}
}

func TestMiddlewareOpenRoute(t *testing.T) {
	h := Middleware(&fakeVerifier{}, true, echoNext(nil)) // even paranoid mode
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("GET", "/", nonLoopback, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("open SPA route = %d, want 200", rec.Code)
	}
}

func TestMiddlewareQueryTokenForDownloads(t *testing.T) {
	fv := &fakeVerifier{grant: &Grant{TokenID: "abcd1234", Scopes: []string{ScopeRead}}}
	h := Middleware(fv, false, echoNext(nil))

	// ?token= accepted on a GET /export download.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("GET", "/export/experiments/a.csv?token=gtk_abcd1234_x", nonLoopback, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("export ?token= = %d, want 200", rec.Code)
	}
	// ?token= NOT accepted on a non-download route (must use header there).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req("POST", "/sql?token=gtk_abcd1234_x", nonLoopback, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("query token on /sql = %d, want 401 (header-only)", rec.Code)
	}
}

// ---- store: lifecycle, verify, last-used throttle ----

func openDB(t *testing.T) *benchStore {
	t.Helper()
	db, err := benchdb.Open(context.Background(), filepath.Join(t.TempDir(), "bench.db"))
	if err != nil {
		t.Fatalf("benchdb.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &benchStore{db: db, store: NewStore(db)}
}

type benchStore struct {
	db    interface{ Close() error }
	store *Store
}

func TestStoreLifecycleAndVerify(t *testing.T) {
	bs := openDB(t)
	svc := NewServiceWithStore(bs.store)
	ctx := context.Background()

	info, secret, err := svc.Create(ctx, "ci-rig", []string{"ingest", "read"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Id == "" || secret == "" {
		t.Fatalf("Create returned empty id/secret")
	}

	// List shows metadata, never a secret.
	list, err := svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v (n=%d)", err, len(list))
	}
	if strings.Join(list[0].Scopes, ",") != "ingest,read" {
		t.Fatalf("scopes = %v", list[0].Scopes)
	}

	// Verify the real secret → grant with the stored scopes.
	g, err := bs.store.Verify(ctx, secret)
	if err != nil {
		t.Fatalf("Verify valid: %v", err)
	}
	if g.TokenID != info.Id || !HasScope(g.Scopes, ScopeRead) {
		t.Fatalf("grant = %+v", g)
	}

	// Wrong secret with a real id → ErrInvalidToken (constant-time miss).
	bad := "gtk_" + info.Id + "_" + "wrongwrongwrongwrongwrongwrongwrongwrong"
	if _, err := bs.store.Verify(ctx, bad); err != ErrInvalidToken {
		t.Fatalf("Verify wrong secret = %v, want ErrInvalidToken", err)
	}
	// Unknown id → ErrInvalidToken (no oracle).
	if _, err := bs.store.Verify(ctx, "gtk_00000000_"+strings.TrimPrefix(secret, "gtk_"+info.Id+"_")); err != ErrInvalidToken {
		t.Fatalf("Verify unknown id = %v, want ErrInvalidToken", err)
	}

	// Delete revokes.
	if err := svc.Delete(ctx, info.Id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := bs.store.Verify(ctx, secret); err != ErrInvalidToken {
		t.Fatalf("Verify after delete = %v, want ErrInvalidToken", err)
	}
}

func TestLastUsedThrottle(t *testing.T) {
	bs := openDB(t)
	svc := NewServiceWithStore(bs.store)
	ctx := context.Background()

	now := time.Unix(1_700_000_000, 0)
	bs.store.now = func() time.Time { return now }

	info, secret, err := svc.Create(ctx, "throttle", []string{"read"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First verify stamps last_used_ns.
	if _, err := bs.store.Verify(ctx, secret); err != nil {
		t.Fatalf("verify 1: %v", err)
	}
	first := lastUsed(t, svc, ctx, info.Id)
	if first == 0 {
		t.Fatalf("last_used not stamped on first use")
	}

	// A second verify 30s later must NOT write (throttled to once/minute).
	now = now.Add(30 * time.Second)
	if _, err := bs.store.Verify(ctx, secret); err != nil {
		t.Fatalf("verify 2: %v", err)
	}
	if got := lastUsed(t, svc, ctx, info.Id); got != first {
		t.Fatalf("last_used moved within throttle window: %d != %d", got, first)
	}

	// Past the throttle window it updates again.
	now = now.Add(2 * time.Minute)
	if _, err := bs.store.Verify(ctx, secret); err != nil {
		t.Fatalf("verify 3: %v", err)
	}
	if got := lastUsed(t, svc, ctx, info.Id); got == first {
		t.Fatalf("last_used did not update after throttle window")
	}
}

func lastUsed(t *testing.T, svc *Service, ctx context.Context, id string) uint64 {
	t.Helper()
	list, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, ti := range list {
		if ti.Id == id {
			return ti.LastUsedNs
		}
	}
	t.Fatalf("token %s not found", id)
	return 0
}
