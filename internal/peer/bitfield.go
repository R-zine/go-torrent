package peer

import (
	"errors"
	"fmt"
)

type Bitfield struct {
	Pieces []bool
}

func NewBitfield(totalPieces int) *Bitfield {
	if totalPieces < 0 {
		totalPieces = 0
	}
	return &Bitfield{Pieces: make([]bool, totalPieces)}
}

func ParseBitfield(payload []byte, totalPieces int) (*Bitfield, error) {
	if totalPieces < 0 {
		return nil, errors.New("negative piece count")
	}
	expectedLength := (totalPieces + 7) / 8
	if len(payload) != expectedLength {
		return nil, fmt.Errorf("bitfield length %d, expected %d", len(payload), expectedLength)
	}
	if totalPieces%8 != 0 && len(payload) > 0 {
		unusedMask := byte(1<<(8-totalPieces%8)) - 1
		if payload[len(payload)-1]&unusedMask != 0 {
			return nil, errors.New("bitfield has non-zero spare bits")
		}
	}

	bitfield := NewBitfield(totalPieces)
	for i := range bitfield.Pieces {
		bitfield.Pieces[i] = payload[i/8]&(1<<(7-(i%8))) != 0
	}
	return bitfield, nil
}

func (b *Bitfield) HasPiece(index int) bool {
	return b != nil && index >= 0 && index < len(b.Pieces) && b.Pieces[index]
}

func (b *Bitfield) SetPiece(index int) error {
	if b == nil || index < 0 || index >= len(b.Pieces) {
		return fmt.Errorf("piece index %d is outside bitfield", index)
	}
	b.Pieces[index] = true
	return nil
}
