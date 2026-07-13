// Package stream is the JetStream backbone shared by Edge and Backend. It owns
// the subject scheme, stream provisioning, batch publishing, and the live
// replay-then-follow read path described in docs/ARCHITECTURE.md.
package stream

import "strings"

// Stream/subject constants.
const (
	// StreamName is the JetStream stream that holds all telemetry.
	StreamName = "TLM"
	// SubjectPrefix is the first token of every telemetry subject.
	SubjectPrefix = "tlm"
	// StreamSubject is the wildcard the TLM stream binds to.
	StreamSubject = "tlm.>"
)

// SanitizeToken makes an arbitrary string safe to use as a single NATS subject
// token. NATS treats '.' as a token separator and '*'/'>' as wildcards, and
// disallows whitespace and control characters inside tokens. Any such rune is
// replaced with '_'. An empty token becomes "_" so the subject always has the
// expected arity.
//
// Sanitization is ONLY for routing. Frames carry canonical channel names and
// batches carry the canonical device_id, so a lossy device_id/channel -> token
// mapping never corrupts the data model; it only affects which subject a
// message is routed on. (Two canonical names that sanitize to the same token
// therefore share a subject; consumers still see the true names in the decoded
// frames.)
func SanitizeToken(tok string) string {
	if tok == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(tok))
	for _, r := range tok {
		switch {
		case r == '.' || r == '*' || r == '>':
			b.WriteRune('_')
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			b.WriteRune('_')
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Subject returns the routing subject for a (deviceID, channel) pair:
// "tlm.<device>.<channel>", each token sanitized.
//
// Packet is deliberately NOT a subject token. Packets are storage/registry
// identity ((packet, param) — telemetry.proto), but routing stays channel-based:
// a subscriber selects by channel name and receives that param regardless of
// which packet it belongs to, and adding a packet token would fan subscriptions
// out per packet for no routing benefit. Packet travels inside the frame as
// metadata; it is not needed to deliver the message.
func Subject(deviceID, channel string) string {
	return SubjectPrefix + "." + SanitizeToken(deviceID) + "." + SanitizeToken(channel)
}

// SubjectFilters builds the set of subject filters for a subscription.
//
//   - deviceID == "" matches all devices (token wildcard "*").
//   - channels empty matches all channels for the selected device(s).
//   - otherwise one filter per requested channel.
func SubjectFilters(deviceID string, channels []string) []string {
	if deviceID == "" && len(channels) == 0 {
		return []string{StreamSubject}
	}
	dev := "*"
	if deviceID != "" {
		dev = SanitizeToken(deviceID)
	}
	if len(channels) == 0 {
		return []string{SubjectPrefix + "." + dev + ".>"}
	}
	out := make([]string, 0, len(channels))
	for _, c := range channels {
		out = append(out, SubjectPrefix+"."+dev+"."+SanitizeToken(c))
	}
	return out
}
