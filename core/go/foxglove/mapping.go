package foxglove

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file mirrors adapters/foxglove/gantry_foxglove/mapping.py: a config-driven
// mapping from Foxglove topics to Gantry device/packet/channel, plus the built-in
// `lerobot` profile. A mapping is a set of rules, each matching a topic (exact or
// by prefix) and fanning its scalar labels out to one or more emit targets. An
// optional track_err block derives a per-packet error channel from two
// already-mapped channels on one device (e.g. follower cmd minus pos).
//
// Timestamps are always the frame's log_time (ns) — packet time from the
// producer's clock, so nothing here invents a timestamp.

// Frame is a produced telemetry sample ready for ingest: device, packet,
// channel, value, and the producer's log_time (ns).
type Frame struct {
	Device  string
	Packet  string
	Channel string
	Value   float64
	LogTime uint64
}

// Emit is one publish target for a scalar label. Exactly one of Channel /
// ChannelFromLabel, and one of Packet / PacketFromLabel, is active — so both
// `label -> packet` (channel fixed) and `label -> channel` (packet fixed) are
// expressible.
type Emit struct {
	Device           string  `json:"device"`
	Channel          string  `json:"channel,omitempty"`
	ChannelFromLabel bool    `json:"channel_from_label,omitempty"`
	Packet           string  `json:"packet,omitempty"`
	PacketFromLabel  bool    `json:"packet_from_label,omitempty"`
	Unit             string  `json:"unit,omitempty"`
	Scale            float64 `json:"scale,omitempty"`
}

// resolve returns (packet, channel) for an already-stripped label.
func (e Emit) resolve(label string) (packet, channel string) {
	if e.PacketFromLabel {
		packet = label
	} else {
		packet = e.Packet
	}
	if e.ChannelFromLabel {
		channel = label
	} else {
		channel = e.Channel
	}
	return packet, channel
}

// scale returns the emit's scale factor, defaulting an omitted (zero) value to
// 1.0 so an unset scale is the identity.
func (e Emit) scale() float64 {
	if e.Scale == 0 {
		return 1.0
	}
	return e.Scale
}

// Rule is a topic-matching rule. Kind is "scalars" (the flat {label,value} list
// lerobot puts on a topic) or "image" (a camera-frame topic recognized so it is
// skipped in-bench — video stays with the Python tap).
type Rule struct {
	Kind        string `json:"kind,omitempty"`
	Topic       string `json:"topic,omitempty"`
	TopicPrefix string `json:"topic_prefix,omitempty"`
	// scalars
	ScalarsField string `json:"scalars_field,omitempty"`
	StripSuffix  string `json:"strip_suffix,omitempty"`
	StripPrefix  string `json:"strip_prefix,omitempty"`
	Emit         []Emit `json:"emit,omitempty"`
}

// kindOrDefault treats an empty kind as "scalars" (parity with the Python
// adapter's default).
func (r Rule) kindOrDefault() string {
	if r.Kind == "" {
		return "scalars"
	}
	return r.Kind
}

// scalarsField is the payload key holding the scalar array ("scalars" default).
func (r Rule) scalarsField() string {
	if r.ScalarsField == "" {
		return "scalars"
	}
	return r.ScalarsField
}

// matches reports whether topic matches this rule (exact topic wins over prefix;
// a rule with neither matches nothing).
func (r Rule) matches(topic string) bool {
	if r.Topic != "" {
		return topic == r.Topic
	}
	if r.TopicPrefix != "" {
		return strings.HasPrefix(topic, r.TopicPrefix)
	}
	return false
}

// strip removes the configured prefix then suffix from a label.
func (r Rule) strip(label string) string {
	if r.StripPrefix != "" && strings.HasPrefix(label, r.StripPrefix) {
		label = label[len(r.StripPrefix):]
	}
	if r.StripSuffix != "" && strings.HasSuffix(label, r.StripSuffix) {
		label = label[:len(label)-len(r.StripSuffix)]
	}
	return label
}

// TrackErr derives out_channel = cmd_channel - pos_channel per packet on device
// (the SO-101 kit's tracking-error convention: leader/command pose minus
// follower/observed pose). Emitted on whichever of the two inputs arrives once
// both are known for a packet, stamped at that input's log_time.
type TrackErr struct {
	Device     string `json:"device"`
	PosChannel string `json:"pos_channel"`
	CmdChannel string `json:"cmd_channel"`
	OutChannel string `json:"out_channel,omitempty"`
	Unit       string `json:"unit,omitempty"`
}

func (t TrackErr) outChannel() string {
	if t.OutChannel == "" {
		return "track_err"
	}
	return t.OutChannel
}

// mappingDoc is the on-the-wire JSON shape shared by the --map file, the stored
// mapping_json, and the built-in profile. A `profile` reference selects a
// built-in (e.g. {"profile":"lerobot"}); otherwise the explicit rules are used.
type mappingDoc struct {
	Profile  string    `json:"profile,omitempty"`
	Name     string    `json:"name,omitempty"`
	Rules    []Rule    `json:"rules,omitempty"`
	TrackErr *TrackErr `json:"track_err,omitempty"`
}

// Mapping is a named set of rules plus optional derived track_err. It is
// stateful only for track_err (it caches the latest pos/cmd per packet), so a
// Mapping instance belongs to a single connection/session and must not be shared
// across concurrent sessions.
type Mapping struct {
	Name     string
	Rules    []Rule
	TrackErr *TrackErr
	// (packet) -> {"pos": value, "cmd": value} latest, for track_err.
	teCache map[string]map[string]float64
}

// match returns the first rule matching topic, or nil.
func (m *Mapping) match(topic string) *Rule {
	for i := range m.Rules {
		if m.Rules[i].matches(topic) {
			return &m.Rules[i]
		}
	}
	return nil
}

// mapScalars turns a scalar message (list of {label,value}) into frames,
// including any derived track_err.
func (m *Mapping) mapScalars(rule *Rule, scalars []scalarItem, logTime uint64) []Frame {
	var frames []Frame
	for _, item := range scalars {
		if item.Label == "" || !item.hasValue {
			continue
		}
		label := rule.strip(item.Label)
		for _, em := range rule.Emit {
			packet, channel := em.resolve(label)
			if packet == "" || channel == "" {
				continue
			}
			val := item.Value * em.scale()
			frames = append(frames, Frame{Device: em.Device, Packet: packet, Channel: channel, Value: val, LogTime: logTime})
			frames = append(frames, m.trackErr(em.Device, packet, channel, val, logTime)...)
		}
	}
	return frames
}

// trackErr updates the pos/cmd cache for a packet and, once both are known,
// emits out_channel = cmd - pos stamped at this input's log_time.
func (m *Mapping) trackErr(device, packet, channel string, value float64, logTime uint64) []Frame {
	te := m.TrackErr
	if te == nil || device != te.Device {
		return nil
	}
	var slot string
	switch channel {
	case te.PosChannel:
		slot = "pos"
	case te.CmdChannel:
		slot = "cmd"
	default:
		return nil
	}
	if m.teCache == nil {
		m.teCache = make(map[string]map[string]float64)
	}
	cache := m.teCache[packet]
	if cache == nil {
		cache = make(map[string]float64, 2)
		m.teCache[packet] = cache
	}
	cache[slot] = value
	pos, okPos := cache["pos"]
	cmd, okCmd := cache["cmd"]
	if okPos && okCmd {
		return []Frame{{Device: te.Device, Packet: packet, Channel: te.outChannel(), Value: cmd - pos, LogTime: logTime}}
	}
	return nil
}

// unitFor returns the declared unit for a (device, channel) under a rule: the
// matching emit target's unit, or track_err's unit for the derived channel.
func (m *Mapping) unitFor(rule *Rule, device, channel string) string {
	for _, em := range rule.Emit {
		if em.Device == device && (em.Channel == channel || em.ChannelFromLabel) {
			return em.Unit
		}
	}
	if te := m.TrackErr; te != nil && device == te.Device && channel == te.outChannel() {
		return te.Unit
	}
	return ""
}

// scalarItem is one {label, value} entry in a scalar payload. hasValue
// distinguishes a present numeric value from an absent/non-numeric one so a
// malformed entry is skipped rather than mapped as zero.
type scalarItem struct {
	Label    string
	Value    float64
	hasValue bool
}

func (s *scalarItem) UnmarshalJSON(b []byte) error {
	var raw struct {
		Label string   `json:"label"`
		Value *float64 `json:"value"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	s.Label = raw.Label
	if raw.Value != nil {
		s.Value = *raw.Value
		s.hasValue = true
	}
	return nil
}

// LoadMapping resolves a stored mapping_json document into a Mapping. A profile
// reference ({"profile":"lerobot"}) selects the built-in profile; otherwise the
// explicit rules are used. An empty document defaults to the lerobot profile
// (the common case: a plug-in-and-go lerobot bench). Each call returns a fresh
// Mapping with its own track_err cache.
func LoadMapping(mappingJSON string) (*Mapping, error) {
	trimmed := strings.TrimSpace(mappingJSON)
	if trimmed == "" {
		return lerobotProfile(), nil
	}
	var doc mappingDoc
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("foxglove: parse mapping: %w", err)
	}
	if doc.Profile != "" {
		return loadProfile(doc.Profile)
	}
	name := doc.Name
	if name == "" {
		name = "custom"
	}
	return &Mapping{Name: name, Rules: doc.Rules, TrackErr: doc.TrackErr}, nil
}

// ValidateMapping reports whether a mapping_json document parses and resolves.
// Used by the source service to reject a bad document at write time.
func ValidateMapping(mappingJSON string) error {
	_, err := LoadMapping(mappingJSON)
	return err
}

// loadProfile returns a built-in named profile.
func loadProfile(name string) (*Mapping, error) {
	switch name {
	case "lerobot":
		return lerobotProfile(), nil
	default:
		return nil, fmt.Errorf("foxglove: unknown built-in profile %q (known: lerobot)", name)
	}
}

// lerobotProfile is the built-in lerobot mapping (SO-101 leader/follower teleop),
// verified against lerobot's foxglove backend and matching the Python adapter's
// _LEROBOT_PROFILE exactly:
//
//	observation "<joint>.pos" -> so101-follower / packet <joint> / channel pos
//	action      "<joint>.pos" -> so101-leader   / packet <joint> / channel pos
//	                         AND so101-follower / packet <joint> / channel cmd
//	track_err (follower) = cmd - pos per joint. Units deg.
//
// The /observation/images/ prefix is present as an image rule for parity; the
// client recognizes and skips those protobuf channels (video stays with the
// Python tap).
func lerobotProfile() *Mapping {
	return &Mapping{
		Name: "lerobot",
		Rules: []Rule{
			{
				Topic:       "/observation/state",
				Kind:        "scalars",
				StripSuffix: ".pos",
				Emit: []Emit{
					{Device: "so101-follower", Channel: "pos", PacketFromLabel: true, Unit: "deg"},
				},
			},
			{
				Topic:       "/action/state",
				Kind:        "scalars",
				StripSuffix: ".pos",
				Emit: []Emit{
					{Device: "so101-leader", Channel: "pos", PacketFromLabel: true, Unit: "deg"},
					{Device: "so101-follower", Channel: "cmd", PacketFromLabel: true, Unit: "deg"},
				},
			},
			{
				TopicPrefix: "/observation/images/",
				Kind:        "image",
			},
		},
		TrackErr: &TrackErr{
			Device:     "so101-follower",
			PosChannel: "pos",
			CmdChannel: "cmd",
			OutChannel: "track_err",
			Unit:       "deg",
		},
	}
}
