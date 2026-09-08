// Package storage persists verified torrent data safely inside a configured
// download directory.
package storage

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bittorrent-client/internal/torrent"
)

type fileSpan struct {
	path  string
	start int64
	end   int64
}

type Storage struct {
	mu sync.RWMutex

	files  []fileSpan
	total  int64
	closed bool

	InProgressPath string
	CompletedPath  string
}

func NewStorage(meta *torrent.TorrentMeta) (*Storage, error) {
	return NewStorageAt(meta, "./torrents")
}

// NewStorageAt creates storage under baseDir. It is exported primarily to let
// embedders and tests choose an explicit download root.
func NewStorageAt(meta *torrent.TorrentMeta, baseDir string) (*Storage, error) {
	if meta == nil {
		return nil, errors.New("torrent metadata is nil")
	}
	if err := torrent.ValidatePathComponent(meta.Name); err != nil {
		return nil, fmt.Errorf("invalid torrent name: %w", err)
	}
	if meta.Length < 0 {
		return nil, errors.New("torrent length must not be negative")
	}

	inProgressDir := filepath.Join(baseDir, "in-progress")
	completedDir := filepath.Join(baseDir, "completed")
	if err := os.MkdirAll(inProgressDir, 0o755); err != nil {
		return nil, fmt.Errorf("create in-progress directory: %w", err)
	}
	if err := os.MkdirAll(completedDir, 0o755); err != nil {
		return nil, fmt.Errorf("create completed directory: %w", err)
	}
	if err := rejectSymlink(inProgressDir); err != nil {
		return nil, err
	}
	if err := rejectSymlink(completedDir); err != nil {
		return nil, err
	}

	storage := &Storage{total: meta.Length}
	if meta.MultiFile {
		storage.InProgressPath = filepath.Join(inProgressDir, meta.Name+".part")
		storage.CompletedPath = filepath.Join(completedDir, meta.Name)
		if err := os.MkdirAll(storage.InProgressPath, 0o755); err != nil {
			return nil, fmt.Errorf("create partial torrent directory: %w", err)
		}
		if err := rejectSymlink(storage.InProgressPath); err != nil {
			return nil, err
		}
		if err := storage.openMultiFile(meta); err != nil {
			_ = storage.Close()
			return nil, err
		}
	} else {
		storage.InProgressPath = filepath.Join(inProgressDir, meta.Name+".part")
		storage.CompletedPath = filepath.Join(completedDir, meta.Name)
		file, err := openSizedFile(storage.InProgressPath, meta.Length)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		storage.files = []fileSpan{{path: storage.InProgressPath, start: 0, end: meta.Length}}
	}
	return storage, nil
}

func (s *Storage) openMultiFile(meta *torrent.TorrentMeta) error {
	if len(meta.Files) == 0 {
		return errors.New("multi-file torrent has no files")
	}
	var offset int64
	for i, info := range meta.Files {
		if info.Length < 0 || len(info.Path) == 0 {
			return fmt.Errorf("invalid file metadata at index %d", i)
		}
		for _, component := range info.Path {
			if err := torrent.ValidatePathComponent(component); err != nil {
				return fmt.Errorf("invalid file path at index %d: %w", i, err)
			}
		}
		path, err := joinInside(s.InProgressPath, info.Path...)
		if err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if err := rejectSymlinksBetween(s.InProgressPath, parent); err != nil {
			return err
		}
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create file parent directory: %w", err)
		}
		if err := rejectSymlinksBetween(s.InProgressPath, parent); err != nil {
			return err
		}
		if info.Length > math.MaxInt64-offset {
			return errors.New("multi-file torrent length overflows int64")
		}
		file, err := openSizedFile(path, info.Length)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		s.files = append(s.files, fileSpan{path: path, start: offset, end: offset + info.Length})
		offset += info.Length
	}
	if offset != meta.Length {
		return fmt.Errorf("file lengths total %d, expected %d", offset, meta.Length)
	}
	return nil
}

func openSizedFile(path string, size int64) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to follow partial-file symlink: %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("size partial file: %w", err)
	}
	return file, nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to use symlink as storage path: %s", path)
	}
	return nil
}

func rejectSymlinksBetween(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("path escapes storage root")
	}
	current := root
	if err := rejectSymlink(current); err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		if err := rejectSymlink(current); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return nil
}

func joinInside(root string, components ...string) (string, error) {
	path := filepath.Join(append([]string{root}, components...)...)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes storage root")
	}
	return path, nil
}

// WritePiece writes a verified piece at its torrent offset.
func (s *Storage) WritePiece(index int, standardPieceLength int, data []byte) error {
	if index < 0 || standardPieceLength <= 0 {
		return errors.New("invalid piece position")
	}
	offset := int64(index) * int64(standardPieceLength)
	if int64(index) != 0 && offset/int64(index) != int64(standardPieceLength) {
		return errors.New("piece offset overflows int64")
	}
	return s.WriteAt(data, offset)
}

func (s *Storage) WriteAt(data []byte, offset int64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("storage is closed")
	}
	if offset < 0 || offset > s.total || int64(len(data)) > s.total-offset {
		return fmt.Errorf("write range [%d,%d) is outside torrent length %d", offset, offset+int64(len(data)), s.total)
	}
	return s.writeAtLocked(data, offset)
}

func (s *Storage) writeAtLocked(data []byte, offset int64) error {
	written := 0
	for _, span := range s.files {
		if written == len(data) {
			break
		}
		position := offset + int64(written)
		if position < span.start || position >= span.end {
			continue
		}
		chunkLength := minInt64(int64(len(data)-written), span.end-position)
		file, err := os.OpenFile(span.path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		n, writeErr := file.WriteAt(data[written:written+int(chunkLength)], position-span.start)
		closeErr := file.Close()
		written += n
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(n) != chunkLength {
			return io.ErrShortWrite
		}
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *Storage) ReadAt(data []byte, offset int64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("storage is closed")
	}
	if offset < 0 || offset > s.total || int64(len(data)) > s.total-offset {
		return errors.New("read range is outside torrent")
	}
	read := 0
	for _, span := range s.files {
		if read == len(data) {
			break
		}
		position := offset + int64(read)
		if position < span.start || position >= span.end {
			continue
		}
		chunkLength := minInt64(int64(len(data)-read), span.end-position)
		file, err := os.Open(span.path)
		if err != nil {
			return err
		}
		n, readErr := file.ReadAt(data[read:read+int(chunkLength)], position-span.start)
		closeErr := file.Close()
		read += n
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(n) != chunkLength {
			return io.ErrUnexpectedEOF
		}
	}
	if read != len(data) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// Complete syncs and closes all files, then atomically moves the partial file
// or directory into the completed directory. Existing completed data is never
// overwritten.
func (s *Storage) Complete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("storage is closed")
	}
	if _, err := os.Lstat(s.CompletedPath); err == nil {
		return fmt.Errorf("completed destination already exists: %s", s.CompletedPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, span := range s.files {
		file, err := os.OpenFile(span.path, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if syncErr != nil {
			return fmt.Errorf("sync partial file: %w", syncErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := os.Rename(s.InProgressPath, s.CompletedPath); err != nil {
		return fmt.Errorf("finalize torrent: %w", err)
	}
	s.closed = true
	return nil
}

func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.closeLocked()
}

func (s *Storage) closeLocked() error {
	s.closed = true
	return nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
