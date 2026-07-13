package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultMaxPoints  = 500
	maxSeconds        = 1800 // JetStream retention window ceiling
	lastTailSeconds   = 60   // how far back get_last looks for a last value
	statusTailSeconds = 120  // how far back edge_status looks for device last-seen
)

// registerTools wires the v1 read-only tool surface onto s.
func registerTools(s *mcpsdk.Server, d Deps) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "list_channels",
		Description: "List telemetry devices and their channels (params), grouped by packet, with value kind and unit. Read-only. Call this first to discover exact channel names before get_window/get_last.",
	}, d.listChannels)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "get_window",
		Description: "Fetch the recent time-series for one or more channels over the last N seconds (1..1800, the retention window). Numeric channels with more than max_points_per_channel samples are downsampled server-side to (t, min, max, mean, count) buckets; otherwise raw (t, v) points are returned. This is the tool for questions like \"what did pitch do in the last 40 seconds\".",
	}, d.getWindow)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "get_last",
		Description: "Get the most recent value per channel with its age in seconds. Channels known to the registry but with no sample in the recent tail are marked stale. Read-only.",
	}, d.getLast)

	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "edge_status",
		Description: "Report engine uptime, telemetry stream stats (message/byte counts, first/last timestamps), and the device list with last-seen ages. Read-only.",
	}, d.edgeStatus)
}

// ---- list_channels ----

type listChannelsArgs struct {
	DeviceID string `json:"device_id,omitempty" jsonschema:"optional device id to restrict to; omit for all devices"`
}

type channelEntry struct {
	Name   string `json:"name"`
	Packet string `json:"packet,omitempty"`
	Kind   string `json:"kind"`
	Unit   string `json:"unit,omitempty"`
}

type deviceChannels struct {
	DeviceID string         `json:"device_id"`
	Channels []channelEntry `json:"channels"`
}

type listChannelsResult struct {
	Devices []deviceChannels `json:"devices"`
}

func (d Deps) listChannels(_ context.Context, _ *mcpsdk.CallToolRequest, args listChannelsArgs) (*mcpsdk.CallToolResult, listChannelsResult, error) {
	var out listChannelsResult
	for _, dc := range d.Channels.List(args.DeviceID) {
		entry := deviceChannels{DeviceID: dc.DeviceId}
		for _, ci := range dc.Channels {
			entry.Channels = append(entry.Channels, channelEntry{
				Name:   ci.Name,
				Packet: ci.Packet,
				Kind:   kindString(ci.Kind),
				Unit:   ci.Unit,
			})
		}
		out.Devices = append(out.Devices, entry)
	}
	return nil, out, nil
}

// ---- get_window ----

type getWindowArgs struct {
	DeviceID            string   `json:"device_id,omitempty" jsonschema:"optional device id; omit to match all devices"`
	Channels            []string `json:"channels" jsonschema:"channel (param) names to fetch, e.g. [\"pitch_deg\",\"roll_deg\"]"`
	Seconds             int      `json:"seconds" jsonschema:"look-back window in seconds, 1..1800"`
	MaxPointsPerChannel int      `json:"max_points_per_channel,omitempty" jsonschema:"max points per channel before downsampling (default 500)"`
}

type textPoint struct {
	TNs  int64  `json:"t_ns"`
	Text string `json:"text"`
}

type windowChannel struct {
	DeviceID    string      `json:"device_id"`
	Channel     string      `json:"channel"`
	Packet      string      `json:"packet,omitempty"`
	Kind        string      `json:"kind"`
	Unit        string      `json:"unit,omitempty"`
	Numeric     bool        `json:"numeric"`
	RawCount    int         `json:"raw_count"`
	Downsampled bool        `json:"downsampled"`
	Buckets     []bucket    `json:"buckets,omitempty"`
	Points      []rawPoint  `json:"points,omitempty"`
	TextPoints  []textPoint `json:"text_points,omitempty"`
}

type getWindowResult struct {
	DeviceID            string           `json:"device_id,omitempty"`
	Seconds             int              `json:"seconds"`
	MaxPointsPerChannel int              `json:"max_points_per_channel"`
	Channels            []windowChannel  `json:"channels"`
	UnknownChannels     []unknownChannel `json:"unknown_channels,omitempty"`
	Truncated           bool             `json:"truncated,omitempty"`
}

func (d Deps) getWindow(ctx context.Context, _ *mcpsdk.CallToolRequest, args getWindowArgs) (*mcpsdk.CallToolResult, getWindowResult, error) {
	if args.Seconds < 1 || args.Seconds > maxSeconds {
		return nil, getWindowResult{}, fmt.Errorf("seconds must be between 1 and %d, got %d", maxSeconds, args.Seconds)
	}
	if len(args.Channels) == 0 {
		return nil, getWindowResult{}, fmt.Errorf("channels must not be empty; call list_channels to discover names")
	}
	maxPoints := args.MaxPointsPerChannel
	if maxPoints <= 0 {
		maxPoints = defaultMaxPoints
	}

	metas, known := knownChannels(d.Channels, args.DeviceID)
	found, unknown := resolveRequested(args.Channels, known)
	metaByName := map[string]channelMeta{}
	metaByKey := map[collectKey]channelMeta{}
	for _, m := range metas {
		if _, ok := metaByName[m.name]; !ok {
			metaByName[m.name] = m
		}
		metaByKey[collectKey{device: m.device, channel: m.name}] = m
	}

	result := getWindowResult{
		DeviceID:            args.DeviceID,
		Seconds:             args.Seconds,
		MaxPointsPerChannel: maxPoints,
		UnknownChannels:     unknown,
	}

	if len(found) == 0 {
		return nil, result, nil // nothing resolvable; unknowns already reported
	}

	highWater, hasHW, err := d.highWater(ctx)
	if err != nil {
		return nil, getWindowResult{}, err
	}
	coll, err := collectWindow(ctx, d.Replay, highWater, hasHW, args.DeviceID, found, uint32(args.Seconds))
	if err != nil {
		return nil, getWindowResult{}, err
	}
	result.Truncated = coll.truncated

	// Group collected keys by channel name so a channel present on several
	// devices yields one entry per device.
	keysByName := map[string][]collectKey{}
	for key := range coll.series {
		keysByName[key.channel] = append(keysByName[key.channel], key)
	}
	for _, keys := range keysByName {
		sort.Slice(keys, func(i, j int) bool { return keys[i].device < keys[j].device })
	}

	for _, name := range found {
		keys := keysByName[name]
		if len(keys) == 0 {
			// Known channel, no data in window: emit an empty series.
			m := metaByName[name]
			result.Channels = append(result.Channels, windowChannel{
				DeviceID: args.DeviceID,
				Channel:  name,
				Packet:   m.packet,
				Kind:     kindString(m.kind),
				Numeric:  isNumericKind(m.kind),
				Unit:     m.unit,
				RawCount: 0,
			})
			continue
		}
		for _, key := range keys {
			samples := coll.sortedByTime(key)
			result.Channels = append(result.Channels, renderWindowChannel(key, samples, maxPoints, metaByKey[key]))
		}
	}
	return nil, result, nil
}

func renderWindowChannel(key collectKey, samples []sample, maxPoints int, m channelMeta) windowChannel {
	wc := windowChannel{
		DeviceID: key.device,
		Channel:  key.channel,
		RawCount: len(samples),
		Unit:     m.unit,
	}
	packet := m.packet
	kind := m.kind
	numeric := true
	for _, s := range samples {
		if s.packet != "" {
			packet = s.packet
		}
		if kind == gantryv1.ValueKind_VALUE_KIND_UNSPECIFIED {
			kind = s.kind
		}
		if !s.numeric {
			numeric = false
		}
	}
	wc.Packet = packet
	wc.Kind = kindString(kind)
	wc.Numeric = numeric

	if !numeric {
		// Non-numeric series: return the most recent maxPoints text points.
		pts := samples
		if len(pts) > maxPoints {
			pts = pts[len(pts)-maxPoints:]
		}
		for _, s := range pts {
			wc.TextPoints = append(wc.TextPoints, textPoint{TNs: s.tNs, Text: s.text})
		}
		return wc
	}

	if len(samples) > maxPoints {
		wc.Downsampled = true
		wc.Buckets = downsample(samples, maxPoints)
	} else {
		wc.Points = rawPoints(samples)
	}
	return wc
}

// ---- get_last ----

type getLastArgs struct {
	DeviceID string   `json:"device_id,omitempty" jsonschema:"optional device id; omit to match all devices"`
	Channels []string `json:"channels,omitempty" jsonschema:"optional channel names; omit for every known channel"`
}

type lastValue struct {
	DeviceID   string   `json:"device_id"`
	Channel    string   `json:"channel"`
	Packet     string   `json:"packet,omitempty"`
	Kind       string   `json:"kind"`
	Unit       string   `json:"unit,omitempty"`
	Numeric    bool     `json:"numeric"`
	Value      *float64 `json:"value,omitempty"`
	Text       string   `json:"text,omitempty"`
	TNs        int64    `json:"t_ns,omitempty"`
	AgeSeconds *float64 `json:"age_seconds,omitempty"`
	Stale      bool     `json:"stale"`
}

type getLastResult struct {
	TailSeconds     int              `json:"tail_seconds"`
	Channels        []lastValue      `json:"channels"`
	UnknownChannels []unknownChannel `json:"unknown_channels,omitempty"`
}

func (d Deps) getLast(ctx context.Context, _ *mcpsdk.CallToolRequest, args getLastArgs) (*mcpsdk.CallToolResult, getLastResult, error) {
	metas, known := knownChannels(d.Channels, args.DeviceID)
	result := getLastResult{TailSeconds: lastTailSeconds}

	// Decide which channel names to report and which to filter the replay by.
	var filterChannels []string
	wantName := map[string]bool{}
	if len(args.Channels) > 0 {
		found, unknown := resolveRequested(args.Channels, known)
		result.UnknownChannels = unknown
		filterChannels = found
		for _, n := range found {
			wantName[n] = true
		}
		if len(found) == 0 {
			return nil, result, nil
		}
	}

	highWater, hasHW, err := d.highWater(ctx)
	if err != nil {
		return nil, getLastResult{}, err
	}
	coll, err := collectWindow(ctx, d.Replay, highWater, hasHW, args.DeviceID, filterChannels, lastTailSeconds)
	if err != nil {
		return nil, getLastResult{}, err
	}

	// Last sample per (device, channel).
	last := map[collectKey]sample{}
	for key := range coll.series {
		s := coll.sortedByTime(key)
		last[key] = s[len(s)-1]
	}

	now := time.Now()
	emit := func(device, name, packet string, kind gantryv1.ValueKind, unit string) {
		key := collectKey{device: device, channel: name}
		lv := lastValue{DeviceID: device, Channel: name, Packet: packet, Unit: unit, Kind: kindString(kind), Numeric: isNumericKind(kind)}
		if s, ok := last[key]; ok {
			lv.TNs = s.tNs
			age := now.Sub(time.Unix(0, s.tNs)).Seconds()
			if age < 0 {
				age = 0
			}
			lv.AgeSeconds = &age
			lv.Numeric = s.numeric
			if s.numeric {
				v := s.num
				lv.Value = &v
				lv.Kind = kindString(s.kind)
			} else {
				lv.Text = s.text
				lv.Kind = kindString(s.kind)
			}
		} else {
			lv.Stale = true
		}
		result.Channels = append(result.Channels, lv)
	}

	// Emit one row per registry channel (optionally filtered), so channels with
	// no recent data still appear, marked stale.
	for _, m := range metas {
		if len(wantName) > 0 && !wantName[m.name] {
			continue
		}
		emit(m.device, m.name, m.packet, m.kind, m.unit)
	}
	return nil, result, nil
}

// ---- edge_status ----

type deviceStatus struct {
	DeviceID          string   `json:"device_id"`
	ChannelCount      int      `json:"channel_count"`
	LastSeenTNs       int64    `json:"last_seen_t_ns,omitempty"`
	LastSeenAgeSecond *float64 `json:"last_seen_age_seconds,omitempty"`
}

type streamStatus struct {
	Name      string `json:"name"`
	Msgs      uint64 `json:"msgs"`
	Bytes     uint64 `json:"bytes"`
	FirstSeq  uint64 `json:"first_seq"`
	LastSeq   uint64 `json:"last_seq"`
	FirstTsNs int64  `json:"first_ts_ns,omitempty"`
	LastTsNs  int64  `json:"last_ts_ns,omitempty"`
}

type edgeStatusResult struct {
	Server        string         `json:"server"`
	Version       string         `json:"version"`
	UptimeSeconds float64        `json:"uptime_seconds"`
	Stream        *streamStatus  `json:"stream,omitempty"`
	Devices       []deviceStatus `json:"devices"`
}

func (d Deps) edgeStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, edgeStatusResult, error) {
	result := edgeStatusResult{Server: ServerName, Version: Version}
	if !d.StartedAt.IsZero() {
		result.UptimeSeconds = time.Since(d.StartedAt).Seconds()
	}

	var highWater uint64
	var hasHW bool
	if d.Stream != nil {
		st, err := d.Stream.StreamState(ctx)
		if err != nil {
			return nil, edgeStatusResult{}, fmt.Errorf("stream state: %w", err)
		}
		result.Stream = &streamStatus{
			Name: st.Name, Msgs: st.Msgs, Bytes: st.Bytes,
			FirstSeq: st.FirstSeq, LastSeq: st.LastSeq,
			FirstTsNs: st.FirstTsNs, LastTsNs: st.LastTsNs,
		}
		highWater, hasHW = st.LastSeq, true
	}

	// Per-device last-seen from a bounded tail across all devices.
	coll, err := collectWindow(ctx, d.Replay, highWater, hasHW, "", nil, statusTailSeconds)
	if err != nil {
		return nil, edgeStatusResult{}, err
	}
	lastSeen := map[string]int64{}
	for key, samples := range coll.series {
		for _, s := range samples {
			if s.tNs > lastSeen[key.device] {
				lastSeen[key.device] = s.tNs
			}
		}
	}

	now := time.Now()
	for _, dc := range d.Channels.List("") {
		ds := deviceStatus{DeviceID: dc.DeviceId, ChannelCount: len(dc.Channels)}
		if t, ok := lastSeen[dc.DeviceId]; ok {
			ds.LastSeenTNs = t
			age := now.Sub(time.Unix(0, t)).Seconds()
			if age < 0 {
				age = 0
			}
			ds.LastSeenAgeSecond = &age
		}
		result.Devices = append(result.Devices, ds)
	}
	return nil, result, nil
}

// ---- shared ----

// highWater fetches the current stream last-sequence as a snapshot high-water
// mark for replay draining. Returns hasHW=false when no StreamStater is wired.
func (d Deps) highWater(ctx context.Context) (uint64, bool, error) {
	if d.Stream == nil {
		return 0, false, nil
	}
	st, err := d.Stream.StreamState(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("stream state: %w", err)
	}
	return st.LastSeq, true, nil
}

func isNumericKind(k gantryv1.ValueKind) bool {
	switch k {
	case gantryv1.ValueKind_VALUE_KIND_F64, gantryv1.ValueKind_VALUE_KIND_I64, gantryv1.ValueKind_VALUE_KIND_BOOL:
		return true
	default:
		return false
	}
}
