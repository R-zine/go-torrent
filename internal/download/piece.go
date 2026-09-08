package download

type PieceState int

const (
	PieceMissing PieceState = iota
	PieceDownloading
	PieceVerifying
	PieceComplete
)

// Piece describes an immutable unit of work returned by PieceManager.
type Piece struct {
	Index  int
	Hash   []byte
	Length int
}

type managedPiece struct {
	Piece
	state PieceState
}
