package mcp

import "testing"

func TestNearestSuggests(t *testing.T) {
	cands := []string{"pitch_deg", "roll_deg", "yaw_deg", "speed"}
	got := nearest("ptch_deg", cands, 3)
	if len(got) == 0 || got[0] != "pitch_deg" {
		t.Fatalf("nearest(ptch_deg) = %v, want pitch_deg first", got)
	}
}
