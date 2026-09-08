package peer

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	messages := []*Message{
		nil,
		{},
		NewInterested(),
		NewRequest(7, 16, 1024),
		NewHave(9),
	}
	for i, original := range messages {
		encoded := original.Serialize()
		decoded, err := ReadMessage(bytes.NewReader(encoded))
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if original == nil || original.ID == nil {
			if decoded.ID != nil {
				t.Fatalf("message %d: expected keep-alive", i)
			}
			continue
		}
		if decoded.ID == nil || *decoded.ID != *original.ID || !bytes.Equal(decoded.Payload, original.Payload) {
			t.Fatalf("message %d mismatch: %#v != %#v", i, decoded, original)
		}
	}
}

func TestReadMessageEnforcesLimitBeforeAllocation(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 1024)
	_, err := ReadMessageWithLimit(bytes.NewReader(header[:]), 32)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("ReadMessageWithLimit() error = %v", err)
	}
	if _, err := ReadMessageWithLimit(bytes.NewReader(header[:]), 0); err == nil {
		t.Fatal("zero limit unexpectedly accepted")
	}
}

func TestReadMessageRejectsTruncatedFrame(t *testing.T) {
	frame := []byte{0, 0, 0, 3, MsgPiece, 1}
	if _, err := ReadMessage(bytes.NewReader(frame)); err == nil {
		t.Fatal("truncated frame unexpectedly accepted")
	}
}

func TestParseHave(t *testing.T) {
	index, err := ParseHave([]byte{0, 0, 0, 42})
	if err != nil || index != 42 {
		t.Fatalf("ParseHave() = %d, %v", index, err)
	}
	for _, payload := range [][]byte{nil, {0}, {0, 0, 0, 0, 0}} {
		if _, err := ParseHave(payload); err == nil {
			t.Errorf("ParseHave(%v) unexpectedly succeeded", payload)
		}
	}
}

func TestParsePiece(t *testing.T) {
	payload := make([]byte, 11)
	binary.BigEndian.PutUint32(payload[0:4], 2)
	binary.BigEndian.PutUint32(payload[4:8], 16)
	copy(payload[8:], "abc")
	index, begin, block, err := ParsePiece(payload)
	if err != nil || index != 2 || begin != 16 || string(block) != "abc" {
		t.Fatalf("ParsePiece() = %d, %d, %q, %v", index, begin, block, err)
	}
	if _, _, _, err := ParsePiece(make([]byte, 7)); err == nil {
		t.Fatal("short piece payload unexpectedly accepted")
	}
}

func FuzzReadMessageDoesNotPanic(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Add(NewRequest(1, 2, 3).Serialize())
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadMessageWithLimit(bytes.NewReader(data), 1<<20)
	})
}
