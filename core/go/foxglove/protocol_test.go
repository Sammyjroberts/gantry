package foxglove

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeBinaryMessageData(t *testing.T) {
	payload := []byte(`{"scalars":[]}`)
	frame := encodeMessageData(7, 123456789, payload)
	msg, err := decodeBinary(frame)
	if err != nil {
		t.Fatalf("decodeBinary: %v", err)
	}
	if msg == nil {
		t.Fatal("decodeBinary returned nil for a MessageData frame")
	}
	if msg.SubscriptionID != 7 {
		t.Errorf("subscription id = %d, want 7", msg.SubscriptionID)
	}
	if msg.LogTime != 123456789 {
		t.Errorf("log time = %d, want 123456789", msg.LogTime)
	}
	if !bytes.Equal(msg.Payload, payload) {
		t.Errorf("payload = %q, want %q", msg.Payload, payload)
	}
}

func TestDecodeBinaryTimeAndUnknownAreNil(t *testing.T) {
	// Time frame (opcode 0x02) — not needed by a subscriber.
	timeFrame := append([]byte{opTime}, make([]byte, 8)...)
	if msg, err := decodeBinary(timeFrame); err != nil || msg != nil {
		t.Errorf("time frame: got (%v, %v), want (nil, nil)", msg, err)
	}
	// Unknown opcode.
	if msg, err := decodeBinary([]byte{0x7F, 1, 2, 3}); err != nil || msg != nil {
		t.Errorf("unknown opcode: got (%v, %v), want (nil, nil)", msg, err)
	}
	// Empty.
	if msg, err := decodeBinary(nil); err != nil || msg != nil {
		t.Errorf("empty: got (%v, %v), want (nil, nil)", msg, err)
	}
}

func TestDecodeBinaryTruncatedIsError(t *testing.T) {
	if _, err := decodeBinary([]byte{opMessageData, 1, 2}); err == nil {
		t.Error("expected error for truncated MessageData frame")
	}
	if _, err := decodeBinary([]byte{opTime, 1, 2}); err == nil {
		t.Error("expected error for truncated Time frame")
	}
}

func TestDecodeBinaryPayloadIsCopied(t *testing.T) {
	// The decoded payload must not alias the input buffer (coder/websocket reuses
	// its read buffer across Reads).
	frame := encodeMessageData(1, 1, []byte("abc"))
	msg, err := decodeBinary(frame)
	if err != nil {
		t.Fatalf("decodeBinary: %v", err)
	}
	frame[13] = 'X' // mutate the source buffer
	if string(msg.Payload) != "abc" {
		t.Errorf("payload aliased the input buffer: %q", msg.Payload)
	}
}

func TestEncodeSubscribe(t *testing.T) {
	got, err := encodeSubscribe([]subEntry{{ID: 1, ChannelID: 5}, {ID: 2, ChannelID: 6}})
	if err != nil {
		t.Fatalf("encodeSubscribe: %v", err)
	}
	var parsed struct {
		Op            string `json:"op"`
		Subscriptions []struct {
			ID        int `json:"id"`
			ChannelID int `json:"channelId"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Op != "subscribe" || len(parsed.Subscriptions) != 2 {
		t.Fatalf("unexpected subscribe message: %s", got)
	}
	if parsed.Subscriptions[0].ID != 1 || parsed.Subscriptions[0].ChannelID != 5 {
		t.Errorf("subscription[0] = %+v", parsed.Subscriptions[0])
	}
}

func TestDecodeJSONRequiresOp(t *testing.T) {
	if _, err := decodeJSON([]byte(`{"channels":[]}`)); err == nil {
		t.Error("expected error for JSON control message missing 'op'")
	}
	if _, err := decodeJSON([]byte(`not json`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
	msg, err := decodeJSON([]byte(`{"op":"advertise","channels":[{"id":1,"topic":"/t","encoding":"json"}]}`))
	if err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
	if msg.Op != opAdvertise || len(msg.Channels) != 1 || msg.Channels[0].Topic != "/t" {
		t.Errorf("unexpected advertise decode: %+v", msg)
	}
}
