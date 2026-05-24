package peer

type Bitfield struct {
	Pieces []bool
}

func ParseBitfield(payload []byte, totalPieces int) *Bitfield {
	pieces := make([]bool, totalPieces)

	for i := 0; i < totalPieces; i++ {

		byteIndex := i / 8
		offset := i % 8

		if byteIndex >= len(payload) {
			break
		}

		// bits are stored MSB first
		mask := byte(1 << (7 - offset))

		pieces[i] = payload[byteIndex]&mask != 0
	}

	return &Bitfield{
		Pieces: pieces,
	}
}

func (b *Bitfield) HasPiece(index int) bool {
	if index < 0 || index >= len(b.Pieces) {
		return false
	}

	return b.Pieces[index]
}