package query

// Bucket is one downsampled aggregate over a time span of a numeric series.
type Bucket struct {
	TNs   int64   `json:"t_ns"` // mean timestamp of samples in the bucket
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
	Count int     `json:"count"`
}

// RawPoint is one (timestamp, value) pair for a numeric series returned without
// downsampling.
type RawPoint struct {
	TNs int64   `json:"t_ns"`
	V   float64 `json:"v"`
}

// Downsample reduces a time-ordered numeric series to at most maxPoints buckets.
// Samples are partitioned into maxPoints equal-width time bins spanning
// [tMin, tMax]; each non-empty bin becomes one (t, min, max, mean, count)
// bucket, where t is the mean timestamp of its samples. Empty bins are omitted,
// so the result has at most maxPoints buckets and preserves gaps. The input must
// be sorted ascending by time (Collection.SortedByTime guarantees this).
//
// maxPoints <= 0 is treated as 1.
func Downsample(samples []Sample, maxPoints int) []Bucket {
	if maxPoints < 1 {
		maxPoints = 1
	}
	if len(samples) == 0 {
		return nil
	}
	tMin := samples[0].TNs
	tMax := samples[len(samples)-1].TNs
	span := tMax - tMin

	// Degenerate span (all samples share a timestamp): collapse to one bucket.
	if span <= 0 {
		return []Bucket{aggregate(samples)}
	}

	out := make([]Bucket, 0, maxPoints)
	binOf := func(t int64) int {
		// Map t in [tMin, tMax] to [0, maxPoints-1].
		idx := int((t - tMin) * int64(maxPoints) / span)
		if idx >= maxPoints {
			idx = maxPoints - 1 // tMax lands exactly on the upper edge
		}
		return idx
	}

	start := 0
	curBin := binOf(samples[0].TNs)
	for i := 1; i < len(samples); i++ {
		b := binOf(samples[i].TNs)
		if b != curBin {
			out = append(out, aggregate(samples[start:i]))
			start = i
			curBin = b
		}
	}
	out = append(out, aggregate(samples[start:]))
	return out
}

// aggregate reduces a non-empty slice of samples to a single bucket over their
// numeric values. The bucket timestamp is the mean of the samples' timestamps,
// computed as base + mean(delta) so summing absolute Unix-nanosecond timestamps
// (which overflow int64 after only a handful of samples) is avoided.
func aggregate(samples []Sample) Bucket {
	b := Bucket{Count: len(samples)}
	b.Min = samples[0].Num
	b.Max = samples[0].Num
	base := samples[0].TNs
	var sum float64
	var dsum int64
	for _, s := range samples {
		if s.Num < b.Min {
			b.Min = s.Num
		}
		if s.Num > b.Max {
			b.Max = s.Num
		}
		sum += s.Num
		dsum += s.TNs - base
	}
	b.Mean = sum / float64(len(samples))
	b.TNs = base + dsum/int64(len(samples))
	return b
}

// RawPoints projects a numeric series to (t, v) pairs.
func RawPoints(samples []Sample) []RawPoint {
	out := make([]RawPoint, 0, len(samples))
	for _, s := range samples {
		out = append(out, RawPoint{TNs: s.TNs, V: s.Num})
	}
	return out
}
