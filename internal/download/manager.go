package download

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"sync"

	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
)

type PieceManager struct {
	mu sync.RWMutex

	pieces              []managedPiece
	storage             *storage.Storage
	standardPieceLength int
	updates             chan struct{}
}

func NewPieceManager(meta *torrent.TorrentMeta, store *storage.Storage) *PieceManager {
	pieces := make([]managedPiece, len(meta.Pieces))
	for i := range pieces {
		length := meta.PieceLength
		if i == len(pieces)-1 {
			length = int(meta.Length - int64(i)*int64(meta.PieceLength))
		}
		pieces[i] = managedPiece{
			Piece: Piece{
				Index:  i,
				Hash:   append([]byte(nil), meta.Pieces[i]...),
				Length: length,
			},
			state: PieceMissing,
		}
	}
	return &PieceManager{
		pieces:              pieces,
		storage:             store,
		standardPieceLength: meta.PieceLength,
		updates:             make(chan struct{}),
	}
}

// RestoreExisting verifies partial data and marks valid pieces complete. It is
// safe to call once before workers start; invalid or unwritten pieces remain
// missing.
func (pm *PieceManager) RestoreExisting() error {
	for i := range pm.pieces {
		pm.mu.RLock()
		piece := pm.pieces[i].Piece
		pm.mu.RUnlock()
		data := make([]byte, piece.Length)
		if err := pm.storage.ReadAt(data, int64(piece.Index)*int64(pm.standardPieceLength)); err != nil {
			return fmt.Errorf("read existing piece %d: %w", piece.Index, err)
		}
		hash := sha1.Sum(data)
		if bytes.Equal(hash[:], piece.Hash) {
			pm.mu.Lock()
			pm.pieces[i].state = PieceComplete
			pm.notifyLocked()
			pm.mu.Unlock()
		}
	}
	return nil
}

// NextPieceForPeer reserves the next missing piece advertised by a peer.
func (pm *PieceManager) NextPieceForPeer(bitfield []bool) *Piece {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for i := range pm.pieces {
		piece := &pm.pieces[i]
		if piece.state != PieceMissing || piece.Index >= len(bitfield) || !bitfield[piece.Index] {
			continue
		}
		piece.state = PieceDownloading
		copy := piece.Piece
		copy.Hash = append([]byte(nil), piece.Hash...)
		return &copy
	}
	return nil
}

// CompletePiece validates length and hash without holding the manager lock,
// persists the data, then publishes the completed state.
func (pm *PieceManager) CompletePiece(index int, data []byte) error {
	pm.mu.Lock()
	if index < 0 || index >= len(pm.pieces) {
		pm.mu.Unlock()
		return errors.New("invalid piece index")
	}
	piece := &pm.pieces[index]
	if piece.state != PieceDownloading {
		pm.mu.Unlock()
		return fmt.Errorf("piece %d is not reserved", index)
	}
	piece.state = PieceVerifying
	expectedLength := piece.Length
	expectedHash := append([]byte(nil), piece.Hash...)
	pm.mu.Unlock()

	if len(data) != expectedLength {
		pm.resetAfterFailure(index)
		return fmt.Errorf("piece %d length %d, expected %d", index, len(data), expectedLength)
	}
	hash := sha1.Sum(data)
	if !bytes.Equal(hash[:], expectedHash) {
		pm.resetAfterFailure(index)
		return errors.New("piece hash verification failed")
	}
	if err := pm.storage.WritePiece(index, pm.standardPieceLength, data); err != nil {
		pm.resetAfterFailure(index)
		return err
	}

	pm.mu.Lock()
	pm.pieces[index].state = PieceComplete
	pm.notifyLocked()
	pm.mu.Unlock()
	return nil
}

func (pm *PieceManager) resetAfterFailure(index int) {
	pm.mu.Lock()
	if index >= 0 && index < len(pm.pieces) && pm.pieces[index].state != PieceComplete {
		pm.pieces[index].state = PieceMissing
		pm.notifyLocked()
	}
	pm.mu.Unlock()
}

// MarkFailed releases a reserved piece. Completed or currently-persisting
// pieces cannot be reset by a stale worker.
func (pm *PieceManager) MarkFailed(index int) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if index < 0 || index >= len(pm.pieces) || pm.pieces[index].state != PieceDownloading {
		return
	}
	pm.pieces[index].state = PieceMissing
	pm.notifyLocked()
}

func (pm *PieceManager) IsComplete() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for i := range pm.pieces {
		if pm.pieces[i].state != PieceComplete {
			return false
		}
	}
	return true
}

func (pm *PieceManager) Progress() (completed, total int) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	total = len(pm.pieces)
	for i := range pm.pieces {
		if pm.pieces[i].state == PieceComplete {
			completed++
		}
	}
	return completed, total
}

func (pm *PieceManager) CompletedBytes() int64 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	var total int64
	for i := range pm.pieces {
		if pm.pieces[i].state == PieceComplete {
			total += int64(pm.pieces[i].Length)
		}
	}
	return total
}

func (pm *PieceManager) State(index int) (PieceState, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if index < 0 || index >= len(pm.pieces) {
		return PieceMissing, errors.New("invalid piece index")
	}
	return pm.pieces[index].state, nil
}

// Updates returns a broadcast channel that closes whenever piece availability
// or progress changes. Callers should fetch a new channel after each wake-up.
func (pm *PieceManager) Updates() <-chan struct{} {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.updates
}

func (pm *PieceManager) notifyLocked() {
	close(pm.updates)
	pm.updates = make(chan struct{})
}

func (pm *PieceManager) Finalize() error {
	if !pm.IsComplete() {
		return errors.New("cannot finalize an incomplete torrent")
	}
	return pm.storage.Complete()
}
