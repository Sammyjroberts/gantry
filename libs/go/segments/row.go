// Package segments is Gantry's Parquet segment store: the durable, columnar tier
// of Edge persistence (ARCHITECTURE.md "Edge: JetStream tail + Parquet segments
// + SQLite index"). A Writer drains the JetStream telemetry backbone into
// immutable Parquet segment files written through the blob.Store, records each
// segment in a Catalog (the SQLite segment index), and a Reader answers bounded
// time-range queries by scanning only the segments (and row groups) that overlap
// the requested window. It is pure Go — parquet-go, no CGo — so Edge stays a
// single cross-compilable static binary. Backend later wires the SAME Writer to
// clustered NATS + an S3 blob store + a Postgres catalog.
package segments

import (
	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
	"github.com/Sammyjroberts/gantry/libs/go/query"
)

// Row is one telemetry sample as stored in a Parquet segment: the flattened,
// typed columnar form of a Frame plus the batch's received_ns arrival stamp.
// device/packet/channel are dictionary-encoded (low cardinality → strong
// compression). The value is stored in the typed column matching Kind; the other
// value columns hold their zero default. Kind is the gantryv1.ValueKind enum as
// an int32 so a reader knows which value column is authoritative without
// inspecting every column.
type Row struct {
	Device     string  `parquet:"device,dict"`
	Packet     string  `parquet:"packet,dict"`
	Channel    string  `parquet:"channel,dict"`
	Kind       int32   `parquet:"kind"`
	TsNs       int64   `parquet:"ts_ns"`
	ReceivedNs int64   `parquet:"received_ns"`
	VF64       float64 `parquet:"v_f64"`
	VI64       int64   `parquet:"v_i64"`
	VBool      bool    `parquet:"v_bool"`
	VStr       string  `parquet:"v_str"`
}

// rowFromFrame flattens one Frame (with its owning device and the batch's
// received_ns) into a columnar Row. It reuses the query package's value decoding
// so the segment tier and the replay tier classify values identically.
func rowFromFrame(device string, receivedNs int64, f *gantryv1.Frame) Row {
	r := Row{
		Device:     device,
		Packet:     f.Packet,
		Channel:    f.Channel,
		Kind:       int32(query.ValueKind(f.Value)),
		TsNs:       int64(f.TimestampNs),
		ReceivedNs: receivedNs,
	}
	switch k := f.Value.GetKind().(type) {
	case *gantryv1.Value_F64:
		r.VF64 = k.F64
	case *gantryv1.Value_I64:
		r.VI64 = k.I64
	case *gantryv1.Value_Flag:
		r.VBool = k.Flag
	case *gantryv1.Value_Text:
		r.VStr = k.Text
	case *gantryv1.Value_Raw:
		// Raw bytes are rendered to the text column as base64 (query.TextValue),
		// keeping the Parquet schema free of a variable-length bytes column while
		// staying round-trippable for display.
		r.VStr = query.TextValue(f.Value)
	}
	return r
}

// Kind reports the row's ValueKind.
func (r Row) Kind_() gantryv1.ValueKind { return gantryv1.ValueKind(r.Kind) }

// Sample projects a Row to a query.Sample so the query planner can merge segment
// rows with stream-replay samples on one code path. Numeric kinds (f64/i64/bool)
// carry Num; text/raw carry Text.
func (r Row) Sample() query.Sample {
	kind := gantryv1.ValueKind(r.Kind)
	s := query.Sample{TNs: r.TsNs, Packet: r.Packet, Kind: kind}
	switch kind {
	case gantryv1.ValueKind_VALUE_KIND_F64:
		s.Num, s.Numeric = r.VF64, true
	case gantryv1.ValueKind_VALUE_KIND_I64:
		s.Num, s.Numeric = float64(r.VI64), true
	case gantryv1.ValueKind_VALUE_KIND_BOOL:
		s.Numeric = true
		if r.VBool {
			s.Num = 1
		}
	default:
		s.Text = r.VStr
	}
	return s
}

// SeriesKey is the (device, packet, channel) identity of a row, matching
// query.SeriesKey so the planner can key merged series consistently.
func (r Row) SeriesKey() query.SeriesKey {
	return query.SeriesKey{Device: r.Device, Packet: r.Packet, Channel: r.Channel}
}
