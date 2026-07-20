package stations

import (
	"fmt"
	"strings"
)

// parseSelector parses a tag selector like "arm=so101,camera=topdown" into a
// map. An empty string is the empty selector (matches every station). Each
// clause must be key=value; whitespace around clauses is trimmed.
func parseSelector(s string) (map[string]string, error) {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, clause := range strings.Split(s, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		k, v, ok := strings.Cut(clause, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, fmt.Errorf("%w: bad selector clause %q (want key=value)", ErrInvalid, clause)
		}
		out[k] = v
	}
	return out, nil
}

// matches reports whether a station's tags satisfy every clause of the selector.
func matches(tags, selector map[string]string) bool {
	for k, v := range selector {
		if tags[k] != v {
			return false
		}
	}
	return true
}
