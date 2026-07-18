package foxglove

import (
	"testing"
)

// frameKey identifies a produced frame for order-independent assertions.
type frameKey struct {
	device  string
	packet  string
	channel string
}

func index(frames []Frame) map[frameKey]Frame {
	m := make(map[frameKey]Frame, len(frames))
	for _, f := range frames {
		m[frameKey{f.Device, f.Packet, f.Channel}] = f
	}
	return m
}

func TestLoadMappingEmptyDefaultsToLerobot(t *testing.T) {
	m, err := LoadMapping("")
	if err != nil {
		t.Fatalf("LoadMapping(\"\"): %v", err)
	}
	if m.Name != "lerobot" {
		t.Errorf("empty mapping name = %q, want lerobot", m.Name)
	}
}

func TestLoadMappingProfileReference(t *testing.T) {
	m, err := LoadMapping(`{"profile":"lerobot"}`)
	if err != nil {
		t.Fatalf("LoadMapping profile: %v", err)
	}
	if m.Name != "lerobot" || m.TrackErr == nil {
		t.Fatalf("profile did not resolve to lerobot: %+v", m)
	}
}

func TestLoadMappingUnknownProfile(t *testing.T) {
	if _, err := LoadMapping(`{"profile":"nope"}`); err == nil {
		t.Error("expected error for unknown profile")
	}
}

func TestLoadMappingBadJSON(t *testing.T) {
	if _, err := LoadMapping(`{not json`); err == nil {
		t.Error("expected error for malformed mapping JSON")
	}
}

// TestLerobotObservationMapping: /observation/state "<joint>.pos" ->
// so101-follower / packet <joint> / channel pos, unit deg, .pos stripped.
func TestLerobotObservationMapping(t *testing.T) {
	m := lerobotProfile()
	rule := m.match("/observation/state")
	if rule == nil {
		t.Fatal("no rule matched /observation/state")
	}
	frames := m.mapScalars(rule, []scalarItem{{Label: "shoulder_pan.pos", Value: 12.5, hasValue: true}}, 1000)
	idx := index(frames)
	got, ok := idx[frameKey{"so101-follower", "shoulder_pan", "pos"}]
	if !ok {
		t.Fatalf("observation did not map to follower pos; got %+v", frames)
	}
	if got.Value != 12.5 || got.LogTime != 1000 {
		t.Errorf("observation frame = %+v", got)
	}
	if u := m.unitFor(rule, "so101-follower", "pos"); u != "deg" {
		t.Errorf("observation unit = %q, want deg", u)
	}
}

// TestLerobotActionFanoutAndTrackErr: /action/state "<joint>.pos" fans out to
// so101-leader/pos AND so101-follower/cmd; once a follower pos is also known for
// that joint, track_err = cmd - pos is derived on the follower.
func TestLerobotActionFanoutAndTrackErr(t *testing.T) {
	m := lerobotProfile()

	// First establish an observed follower pos for shoulder_pan.
	obsRule := m.match("/observation/state")
	m.mapScalars(obsRule, []scalarItem{{Label: "shoulder_pan.pos", Value: 10.0, hasValue: true}}, 500)

	// Now an action arrives (commanded pose 13.0).
	actRule := m.match("/action/state")
	if actRule == nil {
		t.Fatal("no rule matched /action/state")
	}
	frames := m.mapScalars(actRule, []scalarItem{{Label: "shoulder_pan.pos", Value: 13.0, hasValue: true}}, 2000)
	idx := index(frames)

	leader, ok := idx[frameKey{"so101-leader", "shoulder_pan", "pos"}]
	if !ok || leader.Value != 13.0 {
		t.Errorf("action -> leader pos missing/wrong: %+v", frames)
	}
	cmd, ok := idx[frameKey{"so101-follower", "shoulder_pan", "cmd"}]
	if !ok || cmd.Value != 13.0 {
		t.Errorf("action -> follower cmd missing/wrong: %+v", frames)
	}
	// track_err = cmd(13) - pos(10) = 3, stamped at the action's log_time.
	te, ok := idx[frameKey{"so101-follower", "shoulder_pan", "track_err"}]
	if !ok {
		t.Fatalf("track_err not derived: %+v", frames)
	}
	if te.Value != 3.0 || te.LogTime != 2000 {
		t.Errorf("track_err frame = %+v, want value 3 @ 2000", te)
	}
	if u := m.unitFor(actRule, "so101-follower", "track_err"); u != "deg" {
		t.Errorf("track_err unit = %q, want deg", u)
	}
}

// TestTrackErrNeedsBothInputs: an action alone (no prior observation) yields
// leader pos + follower cmd but NO track_err until a pos is seen.
func TestTrackErrNeedsBothInputs(t *testing.T) {
	m := lerobotProfile()
	actRule := m.match("/action/state")
	frames := m.mapScalars(actRule, []scalarItem{{Label: "gripper.pos", Value: 1.0, hasValue: true}}, 100)
	if _, ok := index(frames)[frameKey{"so101-follower", "gripper", "track_err"}]; ok {
		t.Errorf("track_err emitted without a pos input: %+v", frames)
	}
}

// TestImageRuleRecognized: the image prefix rule matches but is kind "image"
// (the client skips it — no scalar frames).
func TestImageRuleRecognized(t *testing.T) {
	m := lerobotProfile()
	rule := m.match("/observation/images/front")
	if rule == nil || rule.kindOrDefault() != "image" {
		t.Fatalf("image topic did not match an image rule: %+v", rule)
	}
}

// TestCustomMappingRulesAndScale exercises an explicit (non-profile) mapping with
// a scale factor and channel_from_label.
func TestCustomMappingRulesAndScale(t *testing.T) {
	doc := `{
		"name": "custom",
		"rules": [
			{"topic": "/telemetry", "kind": "scalars", "emit": [
				{"device": "rig", "packet": "sensors", "channel_from_label": true, "scale": 2.0, "unit": "V"}
			]}
		]
	}`
	m, err := LoadMapping(doc)
	if err != nil {
		t.Fatalf("LoadMapping custom: %v", err)
	}
	rule := m.match("/telemetry")
	if rule == nil {
		t.Fatal("custom rule did not match")
	}
	frames := m.mapScalars(rule, []scalarItem{{Label: "voltage", Value: 3.0, hasValue: true}}, 42)
	idx := index(frames)
	got, ok := idx[frameKey{"rig", "sensors", "voltage"}]
	if !ok {
		t.Fatalf("custom mapping produced no frame: %+v", frames)
	}
	if got.Value != 6.0 { // 3.0 * scale 2.0
		t.Errorf("scaled value = %v, want 6", got.Value)
	}
	if u := m.unitFor(rule, "rig", "voltage"); u != "V" {
		t.Errorf("custom unit = %q, want V", u)
	}
}

// TestMalformedScalarSkipped: an entry with a non-numeric / missing value is
// skipped rather than mapped as zero.
func TestMalformedScalarSkipped(t *testing.T) {
	m := lerobotProfile()
	rule := m.match("/observation/state")
	frames := m.mapScalars(rule, []scalarItem{
		{Label: "a.pos", hasValue: false}, // missing value
		{Label: "b.pos", Value: 5, hasValue: true},
	}, 1)
	if len(frames) != 1 || frames[0].Packet != "b" {
		t.Errorf("expected only the valid scalar to map, got %+v", frames)
	}
}
