package peer

import (
	"bytes"
	"testing"
)

func TestHandshakeRoundTrip(t *testing.T) {
	handshake := &Handshake{}
	copy(handshake.InfoHash[:], "12345678901234567890")
	copy(handshake.PeerID[:], "abcdefghijklmnopqrst")
	handshake.Reserved[5] = 0x10
	encoded := handshake.Serialize()
	if len(encoded) != 68 {
		t.Fatalf("handshake length = %d", len(encoded))
	}
	decoded, err := ParseHandshake(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.InfoHash != handshake.InfoHash || decoded.PeerID != handshake.PeerID || decoded.Reserved != handshake.Reserved {
		t.Fatalf("round trip mismatch: %+v", decoded)
	}
	if err := decoded.Validate(handshake.InfoHash); err != nil {
		t.Fatal(err)
	}
}

func TestParseHandshakeRejectsMalformedData(t *testing.T) {
	valid := (&Handshake{}).Serialize()
	tests := [][]byte{
		nil,
		valid[:48],
		append([]byte(nil), valid[:67]...),
		append(append([]byte(nil), valid...), 0),
	}
	wrongProtocol := append([]byte(nil), valid...)
	copy(wrongProtocol[1:20], bytes.Repeat([]byte{'x'}, 19))
	tests = append(tests, wrongProtocol)
	for i, data := range tests {
		if _, err := ParseHandshake(data); err == nil {
			t.Errorf("case %d unexpectedly succeeded", i)
		}
	}
}

func TestHandshakeValidateRejectsMismatchAndNil(t *testing.T) {
	if err := (*Handshake)(nil).Validate([20]byte{}); err == nil {
		t.Fatal("nil handshake unexpectedly valid")
	}
	handshake := &Handshake{}
	if err := handshake.Validate([20]byte{1}); err == nil {
		t.Fatal("mismatched info hash unexpectedly valid")
	}
}

func FuzzParseHandshakeDoesNotPanic(f *testing.F) {
	f.Add((&Handshake{}).Serialize())
	f.Add([]byte{19})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseHandshake(data)
	})
}
