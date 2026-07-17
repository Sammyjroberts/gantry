package server_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/Sammyjroberts/gantry/apps/bench/internal/server"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/http2"
)

// startEdgeDir starts a full Bench server on a random port over a caller-owned
// data dir (so the caller can restart on the SAME dir to exercise persistence,
// e.g. tokens minted in default mode then enforced in -require-auth mode). It
// returns the base URL and a stop func that shuts the server down.
func startEdgeDir(t *testing.T, dir string, opts ...server.Option) (string, func()) {
	t.Helper()
	app, err := server.New(context.Background(), dir, opts...)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
	}
	return "http://" + ln.Addr().String(), stop
}

// bearerHTTPClient is an h2c client that attaches a fixed bearer token to every
// request (empty token = no header). This is how a non-loopback client
// authenticates against a -require-auth bench.
func bearerHTTPClient(token string) *http.Client {
	base := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return &http.Client{Transport: bearerRoundTripper{token: token, base: base}}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// TestAuthLifecycle drives the full token lifecycle across a restart:
//
//	Phase 1 (default, loopback trusted): bootstrap read/operate/admin tokens over
//	  RPC with NO token (localhost trust), and confirm ListTokens returns them.
//	Phase 2 (-require-auth, same data dir → tokens persist): loopback is no longer
//	  trusted, so scopes are enforced — allowed vs denied calls by scope, then an
//	  admin token revokes the read token and the revoked token 401s.
func TestAuthLifecycle(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// ---- Phase 1: bootstrap from loopback (default mode) ----
	base1, stop1 := startEdgeDir(t, dir)
	tok1 := gantryv1connect.NewTokenServiceClient(bearerHTTPClient(""), base1)

	mint := func(name string, scopes []string) (id, secret string) {
		resp, err := tok1.CreateToken(ctx, connect.NewRequest(&gantryv1.CreateTokenRequest{Name: name, Scopes: scopes}))
		if err != nil {
			t.Fatalf("CreateToken(%s): %v", name, err)
		}
		if resp.Msg.Secret == "" {
			t.Fatalf("CreateToken(%s) returned empty secret", name)
		}
		return resp.Msg.Token.Id, resp.Msg.Secret
	}
	readID, readSecret := mint("reader", []string{"read"})
	_, operateSecret := mint("operator", []string{"read", "operate"})
	_, adminSecret := mint("admin", []string{"read", "admin"})

	list, err := tok1.ListTokens(ctx, connect.NewRequest(&gantryv1.ListTokensRequest{}))
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(list.Msg.Tokens) != 3 {
		t.Fatalf("ListTokens = %d, want 3", len(list.Msg.Tokens))
	}
	stop1()

	// ---- Phase 2: -require-auth on the same dir (tokens persist) ----
	base2, stop2 := startEdgeDir(t, dir, server.WithRequireAuth(true))
	defer stop2()

	// No token at all → 401 (loopback no longer trusted in paranoid mode).
	noAuthLive := gantryv1connect.NewLiveServiceClient(bearerHTTPClient(""), base2)
	if _, err := noAuthLive.ListChannels(ctx, connect.NewRequest(&gantryv1.ListChannelsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no-token ListChannels code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	// No token → CreateToken also 401 (paranoid bootstrap needs an admin token).
	noAuthTok := gantryv1connect.NewTokenServiceClient(bearerHTTPClient(""), base2)
	if _, err := noAuthTok.CreateToken(ctx, connect.NewRequest(&gantryv1.CreateTokenRequest{Name: "x", Scopes: []string{"read"}})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no-token CreateToken code = %v, want Unauthenticated", connect.CodeOf(err))
	}

	// read token → read routes OK.
	readLive := gantryv1connect.NewLiveServiceClient(bearerHTTPClient(readSecret), base2)
	if _, err := readLive.ListChannels(ctx, connect.NewRequest(&gantryv1.ListChannelsRequest{})); err != nil {
		t.Fatalf("read token ListChannels: %v", err)
	}
	// read token → ingest route DENIED (403 → PermissionDenied).
	readIngest := gantryv1connect.NewIngestServiceClient(bearerHTTPClient(readSecret), base2)
	if _, err := readIngest.RegisterChannels(ctx, connect.NewRequest(&gantryv1.RegisterChannelsRequest{DeviceId: "d"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read token ingest code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	// read token → operate route (start experiment) DENIED.
	readExp := gantryv1connect.NewExperimentServiceClient(bearerHTTPClient(readSecret), base2)
	if _, err := readExp.StartExperiment(ctx, connect.NewRequest(&gantryv1.StartExperimentRequest{Name: "nope"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read token start-experiment code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	// operate token → start experiment OK.
	opExp := gantryv1connect.NewExperimentServiceClient(bearerHTTPClient(operateSecret), base2)
	if _, err := opExp.StartExperiment(ctx, connect.NewRequest(&gantryv1.StartExperimentRequest{Name: "climb"})); err != nil {
		t.Fatalf("operate token start-experiment: %v", err)
	}
	// read token → token management (admin) DENIED.
	readTok := gantryv1connect.NewTokenServiceClient(bearerHTTPClient(readSecret), base2)
	if _, err := readTok.ListTokens(ctx, connect.NewRequest(&gantryv1.ListTokensRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read token ListTokens code = %v, want PermissionDenied", connect.CodeOf(err))
	}

	// admin token → revoke the read token.
	adminTok := gantryv1connect.NewTokenServiceClient(bearerHTTPClient(adminSecret), base2)
	if _, err := adminTok.DeleteToken(ctx, connect.NewRequest(&gantryv1.DeleteTokenRequest{Id: readID})); err != nil {
		t.Fatalf("admin DeleteToken: %v", err)
	}
	// The revoked read token now 401s.
	if _, err := readLive.ListChannels(ctx, connect.NewRequest(&gantryv1.ListChannelsRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked token code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

// TestAuthCORSInteraction proves the CORS/auth ordering: on a -require-auth
// bench, (1) a browser preflight OPTIONS to a protected route is answered
// WITHOUT a token, and (2) a 401 on the real request still carries the CORS
// Allow-Origin header so a cross-origin dev client can read the status and show
// the "connect to bench" prompt.
func TestAuthCORSInteraction(t *testing.T) {
	base, stop := startEdgeDir(t, t.TempDir(), server.WithRequireAuth(true))
	defer stop()
	const origin = "http://localhost:5173"
	client := &http.Client{}

	// (1) Preflight passes unauthenticated.
	preq, _ := http.NewRequest(http.MethodOptions, base+"/gantry.v1.LiveService/Subscribe", nil)
	preq.Header.Set("Origin", origin)
	preq.Header.Set("Access-Control-Request-Method", "POST")
	presp, err := client.Do(preq)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", presp.StatusCode)
	}
	if presp.Header.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("preflight missing Allow-Origin for %s", origin)
	}

	// (2) A real, unauthenticated request 401s but still carries CORS headers.
	rreq, _ := http.NewRequest(http.MethodPost, base+"/gantry.v1.LiveService/Subscribe", nil)
	rreq.Header.Set("Origin", origin)
	rreq.Header.Set("Content-Type", "application/json")
	rresp, err := client.Do(rreq)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	rresp.Body.Close()
	if rresp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rresp.StatusCode)
	}
	if rresp.Header.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("401 response missing CORS Allow-Origin (browser could not read it)")
	}
}

// TestAuthMCPScopeEnforcement proves the MCP surface enforces scopes: a read
// token can call read tools and connect, but start_experiment (an operate write
// tool) returns a clear scope error; an operate token can run it.
func TestAuthMCPScopeEnforcement(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Bootstrap tokens from loopback.
	base1, stop1 := startEdgeDir(t, dir)
	tok1 := gantryv1connect.NewTokenServiceClient(bearerHTTPClient(""), base1)
	mk := func(scopes ...string) string {
		r, err := tok1.CreateToken(ctx, connect.NewRequest(&gantryv1.CreateTokenRequest{Name: "mcp", Scopes: scopes}))
		if err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
		return r.Msg.Secret
	}
	readSecret := mk("read")
	operateSecret := mk("read", "operate")
	stop1()

	base2, stop2 := startEdgeDir(t, dir, server.WithRequireAuth(true))
	defer stop2()

	connectMCP := func(token string) *mcpsdk.ClientSession {
		c := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
		sess, err := c.Connect(ctx, &mcpsdk.StreamableClientTransport{
			Endpoint:   base2 + "/mcp",
			HTTPClient: bearerHTTPClient(token),
		}, nil)
		if err != nil {
			t.Fatalf("MCP connect: %v", err)
		}
		return sess
	}

	// read token: connect (needs read) works and a read tool works.
	readSess := connectMCP(readSecret)
	defer readSess.Close()
	if _, err := readSess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "list_channels", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("read token list_channels: %v", err)
	}
	// read token: start_experiment (operate) returns a clear scope error.
	res, err := readSess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "start_experiment", Arguments: map[string]any{"name": "x"}})
	if err == nil && (res == nil || !res.IsError) {
		t.Fatalf("read token start_experiment should have errored on missing operate scope; res=%+v err=%v", res, err)
	}

	// operate token: start_experiment succeeds.
	opSess := connectMCP(operateSecret)
	defer opSess.Close()
	if res, err := opSess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "start_experiment", Arguments: map[string]any{"name": "climb"}}); err != nil || (res != nil && res.IsError) {
		t.Fatalf("operate token start_experiment: err=%v res=%+v", err, res)
	}
}
