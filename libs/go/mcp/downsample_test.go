package mcp

import (
	"testing"
	"time"
)

func mkSamples(vals []float64, t0, step int64) []sample {
	out := make([]sample, len(vals))
	for i, v := range vals {
		out[i] = sample{tNs: t0 + int64(i)*step, num: v, numeric: true}
	}
	return out
}

func TestDownsampleReducesToBucketCap(t *testing.T) {
	samples := mkSamples(make([]float64, 1000), 0, 10)
	for i := range samples {
		samples[i].num = float64(i)
	}
	buckets := downsample(samples, 50)
	if len(buckets) == 0 || len(buckets) > 50 {
		t.Fatalf("got %d buckets, want 1..50", len(buckets))
	}
	// Bucket counts must sum to the raw count (partition, no loss).
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	if total != 1000 {
		t.Fatalf("bucket counts sum = %d, want 1000", total)
	}
	// First bucket covers the low values, last covers the high values.
	if buckets[0].Min != 0 {
		t.Errorf("first bucket min = %v, want 0", buckets[0].Min)
	}
	if buckets[len(buckets)-1].Max != 999 {
		t.Errorf("last bucket max = %v, want 999", buckets[len(buckets)-1].Max)
	}
}

func TestDownsampleAggregatesMinMaxMean(t *testing.T) {
	// Two clearly separated time clusters -> two buckets with known stats.
	samples := []sample{
		{tNs: 0, num: 2, numeric: true},
		{tNs: 1, num: 4, numeric: true}, // cluster A: min2 max4 mean3
		{tNs: 1000, num: 10, numeric: true},
		{tNs: 1001, num: 20, numeric: true}, // cluster B: min10 max20 mean15
	}
	buckets := downsample(samples, 2)
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Min != 2 || buckets[0].Max != 4 || buckets[0].Mean != 3 {
		t.Errorf("bucket A = %+v, want min2 max4 mean3", buckets[0])
	}
	if buckets[1].Min != 10 || buckets[1].Max != 20 || buckets[1].Mean != 15 {
		t.Errorf("bucket B = %+v, want min10 max20 mean15", buckets[1])
	}
}

func TestDownsampleDegenerateSpan(t *testing.T) {
	// All samples share a timestamp -> single collapsed bucket.
	samples := []sample{
		{tNs: 5, num: 1, numeric: true},
		{tNs: 5, num: 3, numeric: true},
		{tNs: 5, num: 2, numeric: true},
	}
	buckets := downsample(samples, 10)
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(buckets))
	}
	if buckets[0].Count != 3 || buckets[0].Min != 1 || buckets[0].Max != 3 || buckets[0].Mean != 2 {
		t.Errorf("collapsed bucket = %+v", buckets[0])
	}
}

func TestDownsampleEmpty(t *testing.T) {
	if got := downsample(nil, 10); got != nil {
		t.Fatalf("downsample(nil) = %v, want nil", got)
	}
}

func TestDownsampleTimestampNoOverflow(t *testing.T) {
	// Real Unix-nanosecond timestamps summed naively overflow int64 after a few
	// samples; the bucket timestamp must stay within the sample range.
	base := time.Now().UnixNano()
	samples := make([]sample, 20)
	for i := range samples {
		samples[i] = sample{tNs: base + int64(i)*1_000_000, num: float64(i), numeric: true}
	}
	buckets := downsample(samples, 1)
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(buckets))
	}
	tMin, tMax := samples[0].tNs, samples[len(samples)-1].tNs
	if buckets[0].TNs < tMin || buckets[0].TNs > tMax {
		t.Fatalf("bucket t_ns %d outside [%d,%d] (overflow?)", buckets[0].TNs, tMin, tMax)
	}
}

func TestNearestSuggests(t *testing.T) {
	cands := []string{"pitch_deg", "roll_deg", "yaw_deg", "speed"}
	got := nearest("ptch_deg", cands, 3)
	if len(got) == 0 || got[0] != "pitch_deg" {
		t.Fatalf("nearest(ptch_deg) = %v, want pitch_deg first", got)
	}
}
