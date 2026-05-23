package download

type PieceState int

const (
	PieceMissing PieceState = iota
	PieceDownloading
	PieceComplete
)

type Piece struct {
	Index  int
	Hash   []byte
	Length int

	State PieceState

	Data []byte
}