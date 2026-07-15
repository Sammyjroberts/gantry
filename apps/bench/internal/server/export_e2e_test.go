package server_test

import (
	"context"
	"encoding/csv"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/Sammyjroberts/gantry/apps/bench/internal/server"
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/gen/go/gantry/v1/gantryv1connect"
)

// ---- frame helpers for the non-f64 kinds ----

func i64Frame(ch string, ts int64, v int64) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: ch, TimestampNs: uint64(ts), Value: &gantryv1.Value{Kind: &gantryv1.Value_I64{I64: v}}}
}
func boolFrame(ch string, ts int64, v bool) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: ch, TimestampNs: uint64(ts), Value: &gantryv1.Value{Kind: &gantryv1.Value_Flag{Flag: v}}}
}
func textFrame(ch string, ts int64, v string) *gantryv1.Frame {
	return &gantryv1.Frame{Channel: ch, TimestampNs: uint64(ts), Value: &gantryv1.Value{Kind: &gantryv1.Value_Text{Text: v}}}
}

// publishOne publishes a single frame in its own batch so stream (delivery)
// order equals the order we call it — makes long-format row order deterministic.
func publishOne(t *testing.T, c gantryv1connect.IngestServiceClient, device string, seq uint64, f *gantryv1.Frame) {
	t.Helper()
	_, err := c.PublishBatch(context.Background(), connect.NewRequest(&gantryv1.PublishBatchRequest{
		Batch: &gantryv1.FrameBatch{DeviceId: device, Sequence: seq, Frames: []*gantryv1.Frame{f}},
	}))
	if err != nil {
		t.Fatalf("PublishBatch seq %d: %v", seq, err)
	}
}

// TestExperimentCSVExport is the full vertical slice: publish frames spanning a
// window, start a backdated experiment, stop it, then GET the CSV and assert the
// header, row count, exact values/kinds, and timestamp ordering.
func TestExperimentCSVExport(t *testing.T) {
	baseURL := startEdge(t)
	httpClient := h2cClient()
	ingestClient := gantryv1connect.NewIngestServiceClient(httpClient, baseURL)
	expClient := gantryv1connect.NewExperimentServiceClient(httpClient, baseURL)
	ctx := context.Background()
	const device = "rover-1"
	const packet = "drive"

	tBase := time.Now().UnixNano()
	// Five in-window frames on four channels, ascending ts, covering every kind.
	publishOne(t, ingestClient, device, 1, f64FrameP(packet, "drive.speed", tBase+1_000_000, 1.5))
	publishOne(t, ingestClient, device, 2, f64FrameP(packet, "drive.speed", tBase+2_000_000, 2.5))
	publishOne(t, ingestClient, device, 3, i64Frame("drive.count", tBase+3_000_000, 42))
	publishOne(t, ingestClient, device, 4, boolFrame("drive.active", tBase+4_000_000, true))
	publishOne(t, ingestClient, device, 5, textFrame("drive.note", tBase+5_000_000, "hello,world"))
	// One out-of-window frame (2s after the experiment end): must be excluded.
	publishOne(t, ingestClient, device, 6, f64FrameP(packet, "drive.speed", tBase+2_000_000_000, 999))

	// Backdated experiment bracketing only the five in-window frames.
	startNs := uint64(tBase - 1_000_000_000)
	endNs := uint64(tBase + 1_000_000_000)
	started, err := expClient.StartExperiment(ctx, connect.NewRequest(&gantryv1.StartExperimentRequest{
		Name: "Climb Test #1", Notes: "n", DeviceId: device, StartNs: startNs,
	}))
	if err != nil {
		t.Fatalf("StartExperiment: %v", err)
	}
	id := started.Msg.Experiment.Id
	if _, err := expClient.StopExperiment(ctx, connect.NewRequest(&gantryv1.StopExperimentRequest{Id: id, EndNs: endNs})); err != nil {
		t.Fatalf("StopExperiment: %v", err)
	}

	// GET the long CSV.
	resp := httpGet(t, baseURL+"/export/experiments/"+id+".csv")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q, want text/csv", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="climb-test-1.csv"`) {
		t.Fatalf("Content-Disposition = %q, want slug climb-test-1.csv", cd)
	}
	records := readCSV(t, resp)

	wantHeader := []string{"ts_ns", "ts_iso", "device_id", "packet", "channel", "kind", "value"}
	if !equalRow(records[0], wantHeader) {
		t.Fatalf("header = %v, want %v", records[0], wantHeader)
	}
	rows := records[1:]
	if len(rows) != 5 {
		t.Fatalf("got %d data rows, want 5 (out-of-window frame must be excluded): %v", len(rows), rows)
	}

	// Assert exact per-row content and ascending ts.
	type want struct {
		ts             int64
		packet, ch, kd string
		val            string
	}
	wants := []want{
		{tBase + 1_000_000, packet, "drive.speed", "f64", "1.5"},
		{tBase + 2_000_000, packet, "drive.speed", "f64", "2.5"},
		{tBase + 3_000_000, "", "drive.count", "i64", "42"},
		{tBase + 4_000_000, "", "drive.active", "bool", "true"},
		{tBase + 5_000_000, "", "drive.note", "text", "hello,world"},
	}
	var prevTs int64 = -1
	for i, w := range wants {
		row := rows[i]
		if row[0] != itoa(w.ts) {
			t.Errorf("row %d ts_ns = %s, want %d", i, row[0], w.ts)
		}
		ts := atoi(t, row[0])
		if ts <= prevTs {
			t.Errorf("row %d ts %d not strictly after previous %d", i, ts, prevTs)
		}
		prevTs = ts
		// ts_iso must be RFC3339Nano UTC of ts.
		if wantIso := time.Unix(0, w.ts).UTC().Format(time.RFC3339Nano); row[1] != wantIso {
			t.Errorf("row %d ts_iso = %s, want %s", i, row[1], wantIso)
		}
		if row[2] != device {
			t.Errorf("row %d device_id = %s, want %s", i, row[2], device)
		}
		if row[3] != w.packet {
			t.Errorf("row %d packet = %s, want %s", i, row[3], w.packet)
		}
		if row[4] != w.ch {
			t.Errorf("row %d channel = %s, want %s", i, row[4], w.ch)
		}
		if row[5] != w.kd {
			t.Errorf("row %d kind = %s, want %s", i, row[5], w.kd)
		}
		if row[6] != w.val {
			t.Errorf("row %d value = %q, want %q", i, row[6], w.val)
		}
	}

	// Wide format, restricted to the speed channel: header + two pivoted rows.
	wresp := httpGet(t, baseURL+"/export/experiments/"+id+".csv?format=wide&channels=drive.speed")
	if wresp.StatusCode != http.StatusOK {
		t.Fatalf("wide export status = %d, want 200", wresp.StatusCode)
	}
	wrecords := readCSV(t, wresp)
	if !equalRow(wrecords[0], []string{"ts_ns", "drive.speed"}) {
		t.Fatalf("wide header = %v, want [ts_ns drive.speed]", wrecords[0])
	}
	if len(wrecords) != 3 {
		t.Fatalf("wide rows = %d, want header + 2", len(wrecords)-1)
	}
	if wrecords[1][1] != "1.5" || wrecords[2][1] != "2.5" {
		t.Fatalf("wide speed cells = %q,%q, want 1.5,2.5", wrecords[1][1], wrecords[2][1])
	}
}

// TestExportErrors covers 404 (unknown id) and 400 (bad format).
func TestExportErrors(t *testing.T) {
	baseURL := startEdge(t)

	if got := httpGet(t, baseURL+"/export/experiments/deadbeef.csv").StatusCode; got != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", got)
	}

	// Create a real experiment so the 400 is about the format param, not the id.
	expClient := gantryv1connect.NewExperimentServiceClient(h2cClient(), baseURL)
	started, err := expClient.StartExperiment(context.Background(), connect.NewRequest(&gantryv1.StartExperimentRequest{Name: "x"}))
	if err != nil {
		t.Fatalf("StartExperiment: %v", err)
	}
	id := started.Msg.Experiment.Id
	if got := httpGet(t, baseURL+"/export/experiments/"+id+".csv?format=bogus").StatusCode; got != http.StatusBadRequest {
		t.Fatalf("bad format status = %d, want 400", got)
	}
	// A missing .csv suffix is not a valid export path.
	if got := httpGet(t, baseURL+"/export/experiments/"+id).StatusCode; got != http.StatusNotFound {
		t.Fatalf("missing .csv suffix status = %d, want 404", got)
	}
}

// TestEmptyWindowExport: an experiment whose window predates any stream data
// exports a valid CSV with the header only (documented out-of-retention v1 case).
func TestEmptyWindowExport(t *testing.T) {
	baseURL := startEdge(t)
	expClient := gantryv1connect.NewExperimentServiceClient(h2cClient(), baseURL)
	// Window entirely in the distant past — no frames were ever published.
	past := uint64(time.Now().Add(-48 * time.Hour).UnixNano())
	started, err := expClient.StartExperiment(context.Background(), connect.NewRequest(&gantryv1.StartExperimentRequest{
		Name: "empty", StartNs: past,
	}))
	if err != nil {
		t.Fatalf("StartExperiment: %v", err)
	}
	id := started.Msg.Experiment.Id
	if _, err := expClient.StopExperiment(context.Background(), connect.NewRequest(&gantryv1.StopExperimentRequest{
		Id: id, EndNs: past + 1_000_000_000,
	})); err != nil {
		t.Fatalf("StopExperiment: %v", err)
	}
	resp := httpGet(t, baseURL+"/export/experiments/"+id+".csv")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	records := readCSV(t, resp)
	if len(records) != 1 {
		t.Fatalf("empty-window CSV has %d rows, want header only", len(records))
	}
}

// TestExperimentSurvivesRestart proves metadata persistence: an experiment
// created against one Bench instance is still present after the process restarts
// on the same data dir (a new App over the same bench.db).
func TestExperimentSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// First instance: create an experiment, then shut down cleanly.
	url1, app1 := startEdgeOnDir(t, dir)
	exp1 := gantryv1connect.NewExperimentServiceClient(h2cClient(), url1)
	started, err := exp1.StartExperiment(ctx, connect.NewRequest(&gantryv1.StartExperimentRequest{
		Name: "persisted run", DeviceId: "dev-9",
	}))
	if err != nil {
		t.Fatalf("StartExperiment: %v", err)
	}
	id := started.Msg.Experiment.Id
	shutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := app1.Shutdown(shutCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Second instance on the same dir: the experiment must still be listable.
	url2, app2 := startEdgeOnDir(t, dir)
	t.Cleanup(func() {
		c, cn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cn()
		_ = app2.Shutdown(c)
	})
	exp2 := gantryv1connect.NewExperimentServiceClient(h2cClient(), url2)
	list, err := exp2.ListExperiments(ctx, connect.NewRequest(&gantryv1.ListExperimentsRequest{}))
	if err != nil {
		t.Fatalf("ListExperiments after restart: %v", err)
	}
	found := false
	for _, e := range list.Msg.Experiments {
		if e.Id == id && e.Name == "persisted run" {
			found = true
		}
	}
	if !found {
		t.Fatalf("experiment %s did not survive restart; list=%v", id, list.Msg.Experiments)
	}
}

// ---- small test utilities ----

// startEdgeOnDir builds and serves an Bench app on the given data dir (so a
// restart can reopen the same bench.db) and returns its base URL and the app for
// manual shutdown.
func startEdgeOnDir(t *testing.T, dir string) (string, *server.App) {
	t.Helper()
	app, err := server.New(context.Background(), dir)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = app.Serve(ln) }()
	return "http://" + ln.Addr().String(), app
}

func httpGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func readCSV(t *testing.T, resp *http.Response) [][]string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\nbody:\n%s", err, body)
	}
	if len(records) == 0 {
		t.Fatalf("empty CSV (no header)")
	}
	return records
}

func equalRow(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
func atoi(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse int %q: %v", s, err)
	}
	return v
}
