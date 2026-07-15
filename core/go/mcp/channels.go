package mcp

import (
	"sort"
	"strings"

	gantryv1 "github.com/Sammyjroberts/gantry/gen/go/gantry/v1"
)

// channelMeta is the flattened registry view the tools resolve against.
type channelMeta struct {
	device string
	name   string
	packet string
	kind   gantryv1.ValueKind
	unit   string
}

// knownChannels flattens the registry for deviceID ("" = all) into a slice and a
// name-set. The slice preserves the registry's deterministic order.
func knownChannels(lister ChannelLister, deviceID string) ([]channelMeta, map[string]bool) {
	var metas []channelMeta
	names := map[string]bool{}
	for _, dc := range lister.List(deviceID) {
		for _, ci := range dc.Channels {
			metas = append(metas, channelMeta{
				device: dc.DeviceId,
				name:   ci.Name,
				packet: ci.Packet,
				kind:   ci.Kind,
				unit:   ci.Unit,
			})
			names[ci.Name] = true
		}
	}
	return metas, names
}

// unknownChannel reports a requested channel name that the registry does not
// know, with up to three nearest known names to help the caller self-correct.
type unknownChannel struct {
	Requested string   `json:"requested"`
	Nearest   []string `json:"nearest,omitempty"`
}

// resolveRequested splits requested channel names into those the registry knows
// and unknowns (each annotated with nearest matches). Order of known names
// follows the request; duplicates are dropped.
func resolveRequested(requested []string, known map[string]bool) (found []string, unknown []unknownChannel) {
	seen := map[string]bool{}
	allNames := make([]string, 0, len(known))
	for n := range known {
		allNames = append(allNames, n)
	}
	for _, r := range requested {
		if seen[r] {
			continue
		}
		seen[r] = true
		if known[r] {
			found = append(found, r)
		} else {
			unknown = append(unknown, unknownChannel{Requested: r, Nearest: nearest(r, allNames, 3)})
		}
	}
	return found, unknown
}

// nearest returns up to n candidate names closest to q by case-insensitive
// Levenshtein distance, with substring matches favored. Candidates further than
// half the query length (rounded up, min 2) are excluded to avoid noise.
func nearest(q string, candidates []string, n int) []string {
	if len(candidates) == 0 {
		return nil
	}
	ql := strings.ToLower(q)
	type scored struct {
		name string
		d    int
	}
	limit := (len(q) + 1) / 2
	if limit < 2 {
		limit = 2
	}
	var out []scored
	for _, c := range candidates {
		cl := strings.ToLower(c)
		d := levenshtein(ql, cl)
		if strings.Contains(cl, ql) || strings.Contains(ql, cl) {
			d = 0 // exact-substring hits sort first
		}
		if d > limit {
			continue
		}
		out = append(out, scored{name: c, d: d})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].d != out[j].d {
			return out[i].d < out[j].d
		}
		return out[i].name < out[j].name
	})
	res := make([]string, 0, n)
	for i := 0; i < len(out) && i < n; i++ {
		res = append(res, out[i].name)
	}
	return res
}

// levenshtein is the standard edit distance between two strings.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
