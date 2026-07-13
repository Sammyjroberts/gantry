package server

import (
	"encoding/csv"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/experiments"
	"github.com/Sammyjroberts/gantry/libs/go/stream"
)

// exportRoute is the method+path pattern for the CSV export endpoint. Go's
// ServeMux wildcards must span a whole path segment, so the ".csv" suffix cannot
// live in the pattern; we capture the final segment as {file} and strip ".csv"
// in the handler (a request without that suffix 404s).
const exportRoute = "GET /export/experiments/{file}"

// longHeader is the column order for the default long (tidy) CSV format.
var longHeader = []string{"ts_ns", "ts_iso", "device_id", "packet", "channel", "kind", "value"}

// wideMaxRows caps distinct timestamps buffered for a wide (pivoted) export.
// Wide cannot stream (columns depend on the full channel set and rows on the
// union of timestamps), so it buffers; this bounds memory. Beyond the cap the
// export is truncated to the first wideMaxRows timestamps. Long stays streaming
// and is unbounded.
const wideMaxRows = 2_000_000

// exportHandler serves GET /export/experiments/{id}.csv. It resolves the
// experiment, replays its [start_ns, end_ns||now] window from the telemetry
// stream, and writes CSV. Long format streams row-by-row; wide format buffers
// and pivots. See proto/gantry/v1/experiment.proto for the contract.
type exportHandler struct {
	svc      *experiments.Service
	replayer *experiments.Replayer
}

func (h *exportHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	id, ok := strings.CutSuffix(file, ".csv")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "long"
	}
	if format != "long" && format != "wide" {
		http.Error(w, "bad format: must be long or wide", http.StatusBadRequest)
		return
	}
	channels := parseChannels(r.URL.Query().Get("channels"))

	exp, err := h.svc.Get(r.Context(), id)
	if errors.Is(err, experiments.ErrNotFound) {
		http.Error(w, "experiment not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	startNs := int64(exp.StartNs)
	endNs := int64(exp.EndNs)
	if endNs == 0 { // still running: export up to now
		endNs = time.Now().UnixNano()
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+slug(exp.Name)+`.csv"`)

	if format == "wide" {
		h.writeWide(w, r, exp, startNs, endNs, channels)
		return
	}
	h.writeLong(w, r, exp, startNs, endNs, channels)
}

// writeLong streams the long/tidy format: one row per frame, flushed in batches
// so the client sees data progressively and memory stays bounded.
func (h *exportHandler) writeLong(w http.ResponseWriter, r *http.Request, exp *gantryv1.Experiment, startNs, endNs int64, channels []string) {
	cw := csv.NewWriter(w)
	_ = cw.Write(longHeader)

	flusher, _ := w.(http.Flusher)
	n := 0
	visit := func(d stream.Delivered) error {
		f := d.Frame
		row := []string{
			strconv.FormatInt(int64(f.TimestampNs), 10),
			time.Unix(0, int64(f.TimestampNs)).UTC().Format(time.RFC3339Nano),
			d.DeviceID,
			f.Packet,
			f.Channel,
			experiments.KindString(f.Value),
			experiments.FormatValue(f.Value),
		}
		if err := cw.Write(row); err != nil {
			return err
		}
		n++
		if n%512 == 0 {
			cw.Flush()
			if err := cw.Error(); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		return nil
	}

	// Replay errors after headers are sent (client disconnect, etc.) cannot change
	// the status code; the partial CSV is all we can offer. An empty window simply
	// yields the header only — the documented out-of-retention v1 behavior.
	_ = h.replayer.Replay(r.Context(), startNs, endNs, exp.DeviceId, channels, visit)
	cw.Flush()
}

// errWideCap stops a wide collection once wideMaxRows distinct timestamps exist.
var errWideCap = errors.New("wide row cap reached")

// writeWide buffers the window and pivots to one column per channel keyed on
// ts_ns. Rows are the sorted union of timestamps; absent (channel, ts) cells are
// empty. Within one (channel, ts) the last frame wins.
func (h *exportHandler) writeWide(w http.ResponseWriter, r *http.Request, exp *gantryv1.Experiment, startNs, endNs int64, channels []string) {
	cells := make(map[int64]map[string]string)
	chanSet := make(map[string]struct{})

	visit := func(d stream.Delivered) error {
		f := d.Frame
		ts := int64(f.TimestampNs)
		row, ok := cells[ts]
		if !ok {
			if len(cells) >= wideMaxRows {
				return errWideCap
			}
			row = make(map[string]string)
			cells[ts] = row
		}
		row[f.Channel] = experiments.FormatValue(f.Value)
		chanSet[f.Channel] = struct{}{}
		return nil
	}
	if err := h.replayer.Replay(r.Context(), startNs, endNs, exp.DeviceId, channels, visit); err != nil && !errors.Is(err, errWideCap) {
		// Header not yet written for wide, but we may be mid-buffer; still emit
		// whatever was collected as a valid (possibly empty) CSV.
	}

	cols := make([]string, 0, len(chanSet))
	for c := range chanSet {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	times := make([]int64, 0, len(cells))
	for ts := range cells {
		times = append(times, ts)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write(append([]string{"ts_ns"}, cols...))
	flusher, _ := w.(http.Flusher)
	for i, ts := range times {
		row := make([]string, 0, len(cols)+1)
		row = append(row, strconv.FormatInt(ts, 10))
		vals := cells[ts]
		for _, c := range cols {
			row = append(row, vals[c])
		}
		if err := cw.Write(row); err != nil {
			return
		}
		if (i+1)%512 == 0 {
			cw.Flush()
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// parseChannels splits a comma-separated channels query value into trimmed,
// non-empty names. Empty input means "all channels".
func parseChannels(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// slug turns an experiment name into a filesystem/URL-safe token: lowercased,
// runs of non-alphanumeric replaced by a single '-', trimmed. Empty names (or
// names with no alphanumerics) fall back to "experiment".
func slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "experiment"
	}
	return s
}
