package download

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"sync"

	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
)

type PieceManager struct {
	pieces []*Piece

	mu sync.Mutex

	storage *storage.Storage

	standardPieceLength int
}

func NewPieceManager(
	meta *torrent.TorrentMeta,
	storage *storage.Storage,
) *PieceManager {

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
		pieces:              pieces,
		storage:             storage,
		standardPieceLength: meta.PieceLength,
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

// NextPieceForPeer returns the next downloadable piece
// that the peer actually owns.
func (pm *PieceManager) NextPieceForPeer(
	bitfield []bool,
) *Piece {

	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, piece := range pm.pieces {

		if piece.State != PieceMissing {
			continue
		}

		if piece.Index >= len(bitfield) {
			continue
		}

		if !bitfield[piece.Index] {
			continue
		}

		piece.State = PieceDownloading

		return piece
	}

	return nil
}

// CompletePiece verifies and persists downloaded data.
func (pm *PieceManager) CompletePiece(
	index int,
	data []byte,
) error {

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if index < 0 || index >= len(pm.pieces) {
		return errors.New("invalid piece index")
	}

	piece := pm.pieces[index]

	// Verify SHA1 hash
	hash := sha1.Sum(data)

	if !bytes.Equal(hash[:], piece.Hash) {

		piece.State = PieceMissing

		return errors.New("piece hash verification failed")
	}

	// Persist piece to disk
	err := pm.storage.WritePiece(
		piece.Index,
		pm.standardPieceLength,
		data,
	)
	if err != nil {

		piece.State = PieceMissing

		return err
	}

	// Piece verified and persisted
	piece.Data = nil // free memory
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

// Finalize closes and renames the completed torrent file.
func (pm *PieceManager) Finalize() error {
	return pm.storage.Complete()
}