// Package foxglove is an in-process client for the open
// `foxglove.websocket.v1` protocol (lerobot --display_mode=foxglove, ROS 2
// foxglove_bridge). It is the Go counterpart of the Python reference tap under
// adapters/foxglove: a read-only subscriber that connects, tracks advertised
// channels, decodes binary MessageData frames, maps scalar payloads to Gantry
// frames via a config-driven mapping engine, and hands them to an ingest sink.
//
// This file is the pure, hardware-free codec — no I/O — so it is exercised
// directly from raw byte fixtures. Only the subset a subscriber needs is
// implemented: parse the server's JSON control messages (serverInfo / advertise
// / unadvertise / status), build the client's subscribe / unsubscribe JSON, and
// decode the binary server frames (MessageData opcode 0x01, Time opcode 0x02).
//
// Binary framing is little-endian, matching the spec
// (https://github.com/foxglove/ws-protocol):
//
//	MessageData := 0x01 | subscription_id (u32 LE) | log_time (u64 LE, ns) | payload
//	Time        := 0x02 | timestamp (u64 LE, ns)
//
// Channel encoding: json payloads only in v1 (the scalar topics). Protobuf image
// channels are recognized and skipped by the client (video stays with the Python
// tap; an in-bench video tee would need an h264 encoder — a future
// ffmpeg-managed-binary pattern).
package foxglove

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Subprotocol is the WebSocket subprotocol offered on connect. A server that
// does not select it is not speaking this protocol.
const Subprotocol = "foxglove.websocket.v1"

// Binary server->client opcodes (first byte of a binary frame).
const (
	opMessageData = 0x01
	opTime        = 0x02
)

// JSON server->client op names the client acts on (others are logged/ignored).
const (
	opAdvertise   = "advertise"
	opUnadvertise = "unadvertise"
	opServerInfo  = "serverInfo"
	opStatus      = "status"
)

// EncodingJSON is the only channel encoding decoded in v1 (scalar topics). Any
// other encoding (e.g. "protobuf" image channels) is recognized and skipped.
const EncodingJSON = "json"

// Channel is a channel as announced in a server `advertise` message. Only the
// fields the client needs are decoded.
type Channel struct {
	ID         int    `json:"id"`
	Topic      string `json:"topic"`
	Encoding   string `json:"encoding"`
	SchemaName string `json:"schemaName"`
}

// MessageData is a decoded binary MessageData frame (opcode 0x01).
type MessageData struct {
	SubscriptionID uint32
	LogTime        uint64 // nanoseconds, from the producer's clock
	Payload        []byte
}

// serverMessage is the union of the server->client JSON control messages we
// parse. Fields not relevant to a given op stay zero.
type serverMessage struct {
	Op         string    `json:"op"`
	Channels   []Channel `json:"channels"`   // advertise
	ChannelIDs []int     `json:"channelIds"` // unadvertise
	Name       string    `json:"name"`       // serverInfo
	Level      int       `json:"level"`      // status
	Message    string    `json:"message"`    // status
}

// decodeJSON parses a server JSON control message. It errors if the message is
// not an object with an `op`, so a malformed control message is surfaced rather
// than silently treated as a no-op.
func decodeJSON(text []byte) (serverMessage, error) {
	var m serverMessage
	if err := json.Unmarshal(text, &m); err != nil {
		return m, fmt.Errorf("foxglove: bad JSON control message: %w", err)
	}
	if m.Op == "" {
		return m, fmt.Errorf("foxglove: JSON control message missing 'op'")
	}
	return m, nil
}

// subEntry is one (subscription_id, channel_id) pair in a subscribe message.
type subEntry struct {
	ID        int `json:"id"`
	ChannelID int `json:"channelId"`
}

// encodeSubscribe builds a `subscribe` message for the given pairs.
func encodeSubscribe(subs []subEntry) ([]byte, error) {
	return json.Marshal(struct {
		Op            string     `json:"op"`
		Subscriptions []subEntry `json:"subscriptions"`
	}{Op: "subscribe", Subscriptions: subs})
}

// encodeUnsubscribe builds an `unsubscribe` message for the given subscription
// ids. Retained for symmetry with the protocol; the client drops subscriptions
// implicitly on unadvertise (a channel that is gone cannot be unsubscribed).
func encodeUnsubscribe(subscriptionIDs []int) ([]byte, error) {
	return json.Marshal(struct {
		Op              string `json:"op"`
		SubscriptionIDs []int  `json:"subscriptionIds"`
	}{Op: "unsubscribe", SubscriptionIDs: subscriptionIDs})
}

// decodeBinary decodes a binary server frame into a *MessageData. Time frames
// (opcode 0x02) and unknown/empty opcodes return (nil, nil) — the subscriber
// does not need them. A truncated frame of a known opcode is an error so a
// corrupt stream is surfaced rather than mis-decoded.
func decodeBinary(data []byte) (*MessageData, error) {
	if len(data) == 0 {
		return nil, nil
	}
	switch data[0] {
	case opMessageData:
		if len(data) < 13 {
			return nil, fmt.Errorf("foxglove: MessageData frame too short: %d bytes", len(data))
		}
		subID := binary.LittleEndian.Uint32(data[1:5])
		logTime := binary.LittleEndian.Uint64(data[5:13])
		// Copy the payload so it does not alias the connection's read buffer,
		// which coder/websocket may reuse on the next Read.
		payload := make([]byte, len(data)-13)
		copy(payload, data[13:])
		return &MessageData{SubscriptionID: subID, LogTime: logTime, Payload: payload}, nil
	case opTime:
		if len(data) < 9 {
			return nil, fmt.Errorf("foxglove: Time frame too short: %d bytes", len(data))
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// encodeMessageData encodes a MessageData frame. Used by the test fake server
// (and for symmetry with the codec).
func encodeMessageData(subscriptionID uint32, logTime uint64, payload []byte) []byte {
	out := make([]byte, 13+len(payload))
	out[0] = opMessageData
	binary.LittleEndian.PutUint32(out[1:5], subscriptionID)
	binary.LittleEndian.PutUint64(out[5:13], logTime)
	copy(out[13:], payload)
	return out
}
