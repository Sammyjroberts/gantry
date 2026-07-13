package query

import (
	"testing"
	"time"
)

func mkSamples(vals []float64, t0, step int64) []Sample {
	out := make([]Sample, len(vals))
	for i, v := range vals {
		out[i] = Sample{TNs: t0 + int64(i)*step, Num: v, Numeric: true}
	}
	return out
}

func TestDownsampleReducesToBucketCap(t *testing.T) {
	samples := mkSamples(make([]float64, 1000), 0, 10)
	for i := range samples {
		samples[i].Num = float64(i)
	}
	buckets := Downsample(samples, 50)
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
	samples := []Sample{
		{TNs: 0, Num: 2, Numeric: true},
		{TNs: 1, Num: 4, Numeric: true}, // cluster A: min2 max4 mean3
		{TNs: 1000, Num: 10, Numeric: true},
		{TNs: 1001, Num: 20, Numeric: true}, // cluster B: min10 max20 mean15
	}
	buckets := Downsample(samples, 2)
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
	samples := []Sample{
		{TNs: 5, Num: 1, Numeric: true},
		{TNs: 5, Num: 3, Numeric: true},
		{TNs: 5, Num: 2, Numeric: true},
	}
	buckets := Downsample(samples, 10)
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(buckets))
	}
	if buckets[0].Count != 3 || buckets[0].Min != 1 || buckets[0].Max != 3 || buckets[0].Mean != 2 {
		t.Errorf("collapsed bucket = %+v", buckets[0])
	}
}

func TestDownsampleEmpty(t *testing.T) {
	if got := Downsample(nil, 10); got != nil {
		t.Fatalf("Downsample(nil) = %v, want nil", got)
	}
}

func TestDownsampleTimestampNoOverflow(t *testing.T) {
	// Real Unix-nanosecond timestamps summed naively overflow int64 after a few
	// samples; the bucket timestamp must stay within the sample range.
	base := time.Now().UnixNano()
	samples := make([]Sample, 20)
	for i := range samples {
		samples[i] = Sample{TNs: base + int64(i)*1_000_000, Num: float64(i), Numeric: true}
	}
	buckets := Downsample(samples, 1)
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1", len(buckets))
	}
	tMin, tMax := samples[0].TNs, samples[len(samples)-1].TNs
	if buckets[0].TNs < tMin || buckets[0].TNs > tMax {
		t.Fatalf("bucket t_ns %d outside [%d,%d] (overflow?)", buckets[0].TNs, tMin, tMax)
	}
}
