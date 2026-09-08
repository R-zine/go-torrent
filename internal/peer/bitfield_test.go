package peer

import "testing"

func TestParseBitfield(t *testing.T) {
	bitfield, err := ParseBitfield([]byte{0b10100000}, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, true}
	for i := range want {
		if bitfield.HasPiece(i) != want[i] {
			t.Errorf("piece %d = %v, want %v", i, bitfield.HasPiece(i), want[i])
		}
	}
	if bitfield.HasPiece(-1) || bitfield.HasPiece(3) {
		t.Fatal("out-of-range piece reported present")
	}
}

func TestParseBitfieldRejectsInvalidLengthAndSpareBits(t *testing.T) {
	if _, err := ParseBitfield(nil, 1); err == nil {
		t.Fatal("short bitfield unexpectedly accepted")
	}
	if _, err := ParseBitfield([]byte{0, 0}, 1); err == nil {
		t.Fatal("long bitfield unexpectedly accepted")
	}
	if _, err := ParseBitfield([]byte{1}, 1); err == nil {
		t.Fatal("non-zero spare bits unexpectedly accepted")
	}
	if _, err := ParseBitfield(nil, -1); err == nil {
		t.Fatal("negative piece count unexpectedly accepted")
	}
}

func TestBitfieldSetPiece(t *testing.T) {
	bitfield := NewBitfield(2)
	if err := bitfield.SetPiece(1); err != nil || !bitfield.HasPiece(1) {
		t.Fatalf("SetPiece() error = %v", err)
	}
	if err := bitfield.SetPiece(2); err == nil {
		t.Fatal("out-of-range SetPiece() unexpectedly succeeded")
	}
}
