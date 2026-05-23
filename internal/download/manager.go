package download

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"sync"

	"bittorrent-client/internal/torrent"
)

type PieceManager struct {
	pieces []*Piece

	mu sync.Mutex
}

func NewPieceManager(meta *torrent.TorrentMeta) *PieceManager {
	totalPieces := len(meta.Pieces)

	pieces := make([]*Piece, totalPieces)

	for i := 0; i < totalPieces; i++ {
		length := meta.PieceLength

		// Last piece may be smaller
		if i == totalPieces-1 {
			remaining := meta.Length - (i * meta.PieceLength)

			if remaining > 0 {
				length = remaining
			}
		}

		pieces[i] = &Piece{
			Index:  i,
			Hash:   meta.Pieces[i],
			Length: length,
			State:  PieceMissing,
		}
	}

	return &PieceManager{
		pieces: pieces,
	}
}

// NextPiece returns the next available piece
// and marks it as downloading.
func (pm *PieceManager) NextPiece() *Piece {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, piece := range pm.pieces {
		if piece.State == PieceMissing {
			piece.State = PieceDownloading
			return piece
		}
	}

	return nil
}

// CompletePiece verifies and stores downloaded data.
func (pm *PieceManager) CompletePiece(index int, data []byte) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if index < 0 || index >= len(pm.pieces) {
		return errors.New("invalid piece index")
	}

	piece := pm.pieces[index]

	hash := sha1.Sum(data)

	if !bytes.Equal(hash[:], piece.Hash) {
		piece.State = PieceMissing
		return errors.New("piece hash verification failed")
	}

	piece.Data = data
	piece.State = PieceComplete

	return nil
}

// MarkFailed resets a failed piece.
func (pm *PieceManager) MarkFailed(index int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if index < 0 || index >= len(pm.pieces) {
		return
	}

	pm.pieces[index].State = PieceMissing
}

func (pm *PieceManager) IsComplete() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, piece := range pm.pieces {
		if piece.State != PieceComplete {
			return false
		}
	}

	return true
}

func (pm *PieceManager) Progress() (completed int, total int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	total = len(pm.pieces)

	for _, piece := range pm.pieces {
		if piece.State == PieceComplete {
			completed++
		}
	}

	return completed, total
}