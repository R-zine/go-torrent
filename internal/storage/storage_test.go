package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"bittorrent-client/internal/torrent"
)

func TestSingleFileWriteReadAndComplete(t *testing.T) {
	base := t.TempDir()
	meta := &torrent.TorrentMeta{Name: "sample.bin", Length: 8, PieceLength: 4}
	store, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.WritePiece(1, 4, []byte("EFGH")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAt([]byte("ABCD"), 0); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if err := store.ReadAt(buffer, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer, []byte("ABCDEFGH")) {
		t.Fatalf("partial data = %q", buffer)
	}

	if err := store.Complete(); err != nil {
		t.Fatal(err)
	}
	completed, err := os.ReadFile(filepath.Join(base, "completed", "sample.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(completed, []byte("ABCDEFGH")) {
		t.Fatalf("completed data = %q", completed)
	}
	if _, err := os.Stat(store.InProgressPath); !os.IsNotExist(err) {
		t.Fatalf("partial path still exists: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestMultiFileWriteSpansBoundaries(t *testing.T) {
	base := t.TempDir()
	meta := &torrent.TorrentMeta{
		Name:        "bundle",
		Length:      7,
		PieceLength: 5,
		MultiFile:   true,
		Files: []torrent.FileInfo{
			{Length: 3, Path: []string{"one.bin"}},
			{Length: 4, Path: []string{"nested", "two.bin"}},
		},
	}
	store, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.WriteAt([]byte("ABCDEFG"), 0); err != nil {
		t.Fatal(err)
	}
	readBack := make([]byte, 5)
	if err := store.ReadAt(readBack, 2); err != nil {
		t.Fatal(err)
	}
	if string(readBack) != "CDEFG" {
		t.Fatalf("cross-file read = %q", readBack)
	}
	if err := store.Complete(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(base, "completed", "bundle", "one.bin"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(base, "completed", "bundle", "nested", "two.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "ABC" || string(second) != "DEFG" {
		t.Fatalf("files = %q and %q", first, second)
	}
}

func TestStorageRejectsOutOfBoundsAndUnsafePaths(t *testing.T) {
	base := t.TempDir()
	unsafe := &torrent.TorrentMeta{Name: "..\\escape", Length: 1, PieceLength: 1}
	if _, err := NewStorageAt(unsafe, base); err == nil {
		t.Fatal("unsafe name unexpectedly accepted")
	}

	meta := &torrent.TorrentMeta{Name: "safe", Length: 4, PieceLength: 4}
	store, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, test := range []struct {
		offset int64
		data   []byte
	}{
		{-1, []byte("x")},
		{4, []byte("x")},
		{3, []byte("xx")},
	} {
		if err := store.WriteAt(test.data, test.offset); err == nil {
			t.Errorf("WriteAt(%d, %d bytes) unexpectedly succeeded", test.offset, len(test.data))
		}
	}
	if err := store.WritePiece(-1, 4, []byte("x")); err == nil {
		t.Fatal("negative piece index unexpectedly accepted")
	}
}

func TestCompleteDoesNotOverwriteExistingDestination(t *testing.T) {
	base := t.TempDir()
	meta := &torrent.TorrentMeta{Name: "existing", Length: 4, PieceLength: 4}
	store, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.WriteFile(store.CompletedPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(); err == nil {
		t.Fatal("Complete() unexpectedly overwrote destination")
	}
	got, err := os.ReadFile(store.CompletedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("existing data = %q", got)
	}
	if err := store.WriteAt([]byte("data"), 0); err != nil {
		t.Fatalf("storage should remain open after destination error: %v", err)
	}
}

func TestPartialDataSurvivesReopen(t *testing.T) {
	base := t.TempDir()
	meta := &torrent.TorrentMeta{Name: "resume", Length: 4, PieceLength: 4}
	first, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.WriteAt([]byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := NewStorageAt(meta, base)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	buffer := make([]byte, 4)
	if err := second.ReadAt(buffer, 0); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "data" {
		t.Fatalf("reopened data = %q", buffer)
	}
}

func TestClosedStorageRejectsIO(t *testing.T) {
	store, err := NewStorageAt(&torrent.TorrentMeta{Name: "closed", Length: 1, PieceLength: 1}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("WriteAt() unexpectedly succeeded")
	}
	if err := store.ReadAt(make([]byte, 1), 0); err == nil {
		t.Fatal("ReadAt() unexpectedly succeeded")
	}
	if err := store.Complete(); err == nil {
		t.Fatal("Complete() unexpectedly succeeded")
	}
}

func TestStorageRefusesPartialFileSymlink(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "in-progress"), 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "in-progress", "linked.part")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := NewStorageAt(&torrent.TorrentMeta{Name: "linked", Length: 4, PieceLength: 4}, base)
	if err == nil {
		t.Fatal("symlink unexpectedly accepted")
	}
	got, err := os.ReadFile(external)
	if err != nil || string(got) != "keep" {
		t.Fatalf("external data = %q, %v", got, err)
	}
}

func TestStorageDoesNotFollowMultiFileParentSymlink(t *testing.T) {
	base := t.TempDir()
	partialRoot := filepath.Join(base, "in-progress", "bundle.part")
	if err := os.MkdirAll(partialRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(partialRoot, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	meta := &torrent.TorrentMeta{
		Name:        "bundle",
		Length:      1,
		PieceLength: 1,
		MultiFile:   true,
		Files:       []torrent.FileInfo{{Length: 1, Path: []string{"linked", "created", "file"}}},
	}
	if _, err := NewStorageAt(meta, base); err == nil {
		t.Fatal("parent symlink unexpectedly accepted")
	}
	if _, err := os.Stat(filepath.Join(external, "created")); !os.IsNotExist(err) {
		t.Fatalf("directory was created through symlink: %v", err)
	}
}
