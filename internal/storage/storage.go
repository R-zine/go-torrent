package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"bittorrent-client/internal/torrent"
)

type Storage struct {
	File *os.File

	InProgressPath string
	CompletedPath  string
}

func NewStorage(meta *torrent.TorrentMeta) (*Storage, error) {

	// ======================================================
	// Ensure directories exist
	// ======================================================

	err := os.MkdirAll("./torrents/in-progress", 0755)
	if err != nil {
		return nil, err
	}

	err = os.MkdirAll("./torrents/completed", 0755)
	if err != nil {
		return nil, err
	}

	// ======================================================
	// Build paths
	// ======================================================

	inProgress := filepath.Join(
		"./torrents/in-progress",
		meta.Name+".part",
	)

	completed := filepath.Join(
		"./torrents/completed",
		meta.Name,
	)

	// ======================================================
	// Open partial file
	// ======================================================

	file, err := os.OpenFile(
		inProgress,
		os.O_CREATE|os.O_RDWR,
		0644,
	)
	if err != nil {
		return nil, err
	}

	// ======================================================
	// Preallocate file size
	// ======================================================

	err = file.Truncate(int64(meta.Length))
	if err != nil {

		file.Close()

		return nil, err
	}

	return &Storage{
		File:           file,
		InProgressPath: inProgress,
		CompletedPath:  completed,
	}, nil
}

// WritePiece writes a verified piece directly into
// the correct file offset.
func (s *Storage) WritePiece(
	index int,
	standardPieceLength int,
	data []byte,
) error {

	if s.File == nil {
		return fmt.Errorf("storage file is closed")
	}

	offset := int64(index * standardPieceLength)

	n, err := s.File.WriteAt(data, offset)
	if err != nil {
		return err
	}

	if n != len(data) {
		return fmt.Errorf(
			"short write: wrote %d of %d bytes",
			n,
			len(data),
		)
	}

	return nil
}

// Complete flushes the partial file and atomically
// moves it into the completed directory.
func (s *Storage) Complete() error {

	if s.File == nil {
		return fmt.Errorf("storage file already closed")
	}

	err := s.File.Sync()
	if err != nil {
		return err
	}

	err = s.File.Close()
	if err != nil {
		return err
	}

	s.File = nil

	err = os.Rename(
		s.InProgressPath,
		s.CompletedPath,
	)
	if err != nil {
		return err
	}

	return nil
}