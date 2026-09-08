package download

import (
	"crypto/sha1"
	"os"
	"sync"
	"testing"
	"time"

	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
)

func TestPieceManagerSchedulesOnlyAvailablePieces(t *testing.T) {
	manager, store := newTestManager(t, []byte("abcdefgh"), 4)
	defer store.Close()
	if piece := manager.NextPieceForPeer([]bool{false, true}); piece == nil || piece.Index != 1 || piece.Length != 4 {
		t.Fatalf("piece = %+v", piece)
	}
	if piece := manager.NextPieceForPeer([]bool{false, true}); piece != nil {
		t.Fatalf("already reserved piece returned again: %+v", piece)
	}
	manager.MarkFailed(1)
	if piece := manager.NextPieceForPeer([]bool{false, true}); piece == nil || piece.Index != 1 {
		t.Fatalf("released piece = %+v", piece)
	}
}

func TestCompletePieceValidatesAndFinalizes(t *testing.T) {
	content := []byte("abcdefgh")
	manager, store := newTestManager(t, content, 4)
	defer store.Close()
	for index, data := range [][]byte{content[:4], content[4:]} {
		piece := manager.NextPieceForPeer([]bool{true, true})
		if piece == nil || piece.Index != index {
			t.Fatalf("piece %d = %+v", index, piece)
		}
		if err := manager.CompletePiece(index, data); err != nil {
			t.Fatal(err)
		}
	}
	if !manager.IsComplete() {
		t.Fatal("manager is not complete")
	}
	if completed, total := manager.Progress(); completed != 2 || total != 2 {
		t.Fatalf("progress = %d/%d", completed, total)
	}
	completedPath := store.CompletedPath
	if err := manager.Finalize(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(completedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("completed data = %q", got)
	}
}

func TestCompletePieceFailuresRequeuePiece(t *testing.T) {
	manager, store := newTestManager(t, []byte("data"), 4)
	defer store.Close()
	tests := []struct {
		name string
		data []byte
	}{
		{"wrong length", []byte("dat")},
		{"wrong hash", []byte("nope")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			piece := manager.NextPieceForPeer([]bool{true})
			if piece == nil {
				t.Fatal("piece was not scheduled")
			}
			if err := manager.CompletePiece(piece.Index, test.data); err == nil {
				t.Fatal("CompletePiece() unexpectedly succeeded")
			}
			state, err := manager.State(0)
			if err != nil || state != PieceMissing {
				t.Fatalf("state = %v, %v", state, err)
			}
		})
	}
}

func TestMarkFailedBroadcastsAndCannotResetCompletePiece(t *testing.T) {
	manager, store := newTestManager(t, []byte("data"), 4)
	defer store.Close()
	updates := manager.Updates()
	piece := manager.NextPieceForPeer([]bool{true})
	manager.MarkFailed(piece.Index)
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("piece release was not broadcast")
	}
	piece = manager.NextPieceForPeer([]bool{true})
	if err := manager.CompletePiece(piece.Index, []byte("data")); err != nil {
		t.Fatal(err)
	}
	manager.MarkFailed(piece.Index)
	state, _ := manager.State(piece.Index)
	if state != PieceComplete {
		t.Fatalf("completed state was reset to %v", state)
	}
}

func TestRestoreExistingVerifiesOnlyValidPieces(t *testing.T) {
	content := []byte("abcdefgh")
	manager, store := newTestManager(t, content, 4)
	defer store.Close()
	if err := store.WriteAt([]byte("abcdxxxx"), 0); err != nil {
		t.Fatal(err)
	}
	if err := manager.RestoreExisting(); err != nil {
		t.Fatal(err)
	}
	if completed, total := manager.Progress(); completed != 1 || total != 2 {
		t.Fatalf("progress = %d/%d", completed, total)
	}
	if piece := manager.NextPieceForPeer([]bool{true, true}); piece == nil || piece.Index != 1 {
		t.Fatalf("next piece = %+v", piece)
	}
}

func TestFinalizeRejectsIncompleteTorrent(t *testing.T) {
	manager, store := newTestManager(t, []byte("data"), 4)
	defer store.Close()
	if err := manager.Finalize(); err == nil {
		t.Fatal("Finalize() unexpectedly succeeded")
	}
}

func TestConcurrentPieceCompletion(t *testing.T) {
	content := []byte("abcdefgh")
	manager, store := newTestManager(t, content, 4)
	defer store.Close()
	first := manager.NextPieceForPeer([]bool{true, true})
	second := manager.NextPieceForPeer([]bool{true, true})
	var wait sync.WaitGroup
	wait.Add(2)
	for _, work := range []struct {
		piece *Piece
		data  []byte
	}{{first, content[:4]}, {second, content[4:]}} {
		work := work
		go func() {
			defer wait.Done()
			if err := manager.CompletePiece(work.piece.Index, work.data); err != nil {
				t.Errorf("CompletePiece() = %v", err)
			}
		}()
	}
	wait.Wait()
	if !manager.IsComplete() {
		t.Fatal("concurrent completion did not finish torrent")
	}
}

func TestCompletedBytesCountsActualNonContiguousPieceLengths(t *testing.T) {
	manager, store := newTestManager(t, []byte("abcdef"), 4)
	defer store.Close()
	piece := manager.NextPieceForPeer([]bool{false, true})
	if piece == nil || piece.Length != 2 {
		t.Fatalf("last piece = %+v", piece)
	}
	if err := manager.CompletePiece(piece.Index, []byte("ef")); err != nil {
		t.Fatal(err)
	}
	if got := manager.CompletedBytes(); got != 2 {
		t.Fatalf("CompletedBytes() = %d, want 2", got)
	}
}

func newTestManager(t *testing.T, content []byte, pieceLength int) (*PieceManager, *storage.Storage) {
	t.Helper()
	meta := testMeta(content, pieceLength)
	base := t.TempDir()
	store, err := storage.NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	return NewPieceManager(meta, store), store
}

func testMeta(content []byte, pieceLength int) *torrent.TorrentMeta {
	pieces := make([][]byte, 0)
	for offset := 0; offset < len(content); offset += pieceLength {
		end := offset + pieceLength
		if end > len(content) {
			end = len(content)
		}
		hash := sha1.Sum(content[offset:end])
		pieces = append(pieces, append([]byte(nil), hash[:]...))
	}
	return &torrent.TorrentMeta{
		Name:        "manager.bin",
		Length:      int64(len(content)),
		PieceLength: pieceLength,
		Pieces:      pieces,
	}
}
