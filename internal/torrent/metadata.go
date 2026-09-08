// Package torrent extracts and validates BitTorrent metainfo.
package torrent

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"bittorrent-client/internal/bencode"
)

type FileInfo struct {
	Length int64
	Path   []string
}

type TorrentMeta struct {
	Announce     string
	AnnounceList [][]string
	Name         string
	Length       int64
	PieceLength  int
	Pieces       [][]byte
	InfoHash     [20]byte
	Files        []FileInfo
	MultiFile    bool
}

const MaxPieceLength = 64 << 20

// TrackerURLs returns de-duplicated tracker URLs in tier order.
func (m *TorrentMeta) TrackerURLs() []string {
	seen := make(map[string]struct{})
	var urls []string
	add := func(raw string) {
		if raw == "" {
			return
		}
		if _, exists := seen[raw]; exists {
			return
		}
		seen[raw] = struct{}{}
		urls = append(urls, raw)
	}
	add(m.Announce)
	for _, tier := range m.AnnounceList {
		for _, trackerURL := range tier {
			add(trackerURL)
		}
	}
	return urls
}

func ExtractMeta(root *bencode.BValue, rawData []byte) (*TorrentMeta, error) {
	if root == nil {
		return nil, errors.New("metainfo root is nil")
	}
	rootDict, err := bencode.AsDict(*root)
	if err != nil {
		return nil, fmt.Errorf("metainfo root: %w", err)
	}

	announce := ""
	if announceValue, ok := rootDict["announce"]; ok {
		announce, err = bencode.AsString(announceValue)
		if err != nil {
			return nil, fmt.Errorf("announce: %w", err)
		}
	}
	announceList, err := extractAnnounceList(rootDict)
	if err != nil {
		return nil, err
	}
	if announce == "" && len(announceList) == 0 {
		return nil, errors.New("metainfo has no tracker URL")
	}

	infoValue, ok := rootDict["info"]
	if !ok {
		return nil, errors.New("missing info dictionary")
	}
	info, err := bencode.AsDict(infoValue)
	if err != nil {
		return nil, fmt.Errorf("info: %w", err)
	}

	name, err := requiredString(info, "name")
	if err != nil {
		return nil, err
	}
	if err := ValidatePathComponent(name); err != nil {
		return nil, fmt.Errorf("invalid torrent name: %w", err)
	}

	pieceLength64, err := requiredInt64(info, "piece length")
	if err != nil {
		return nil, err
	}
	if pieceLength64 <= 0 || pieceLength64 > MaxPieceLength || pieceLength64 > int64(maxInt()) {
		return nil, fmt.Errorf("piece length must be between 1 and %d", MaxPieceLength)
	}

	pieceBytes, err := requiredBytes(info, "pieces")
	if err != nil {
		return nil, err
	}
	if len(pieceBytes)%sha1Size != 0 {
		return nil, fmt.Errorf("pieces length %d is not divisible by %d", len(pieceBytes), sha1Size)
	}

	meta := &TorrentMeta{
		Announce:     announce,
		AnnounceList: announceList,
		Name:         name,
		PieceLength:  int(pieceLength64),
	}
	if filesValue, hasFiles := info["files"]; hasFiles {
		if _, hasLength := info["length"]; hasLength {
			return nil, errors.New("info dictionary contains both length and files")
		}
		meta.MultiFile = true
		meta.Files, meta.Length, err = extractFiles(filesValue)
		if err != nil {
			return nil, err
		}
	} else {
		meta.Length, err = requiredInt64(info, "length")
		if err != nil {
			return nil, err
		}
		if meta.Length < 0 {
			return nil, errors.New("length must not be negative")
		}
	}

	expectedPieces := pieceCount(meta.Length, int64(meta.PieceLength))
	actualPieces := int64(len(pieceBytes) / sha1Size)
	if actualPieces != expectedPieces {
		return nil, fmt.Errorf("piece count mismatch: got %d, expected %d", actualPieces, expectedPieces)
	}
	meta.Pieces = make([][]byte, actualPieces)
	for i := range meta.Pieces {
		meta.Pieces[i] = append([]byte(nil), pieceBytes[i*sha1Size:(i+1)*sha1Size]...)
	}

	meta.InfoHash, err = bencode.ExtractInfoHash(root, rawData)
	if err != nil {
		return nil, err
	}
	return meta, nil
}

const sha1Size = 20

func extractAnnounceList(root bencode.BDict) ([][]string, error) {
	value, ok := root["announce-list"]
	if !ok {
		return nil, nil
	}
	tiers, err := bencode.AsList(value)
	if err != nil {
		return nil, fmt.Errorf("announce-list: %w", err)
	}
	result := make([][]string, 0, len(tiers))
	for i, tierValue := range tiers {
		tierList, err := bencode.AsList(tierValue)
		if err != nil {
			return nil, fmt.Errorf("announce-list tier %d: %w", i, err)
		}
		tier := make([]string, 0, len(tierList))
		for j, trackerValue := range tierList {
			trackerURL, err := bencode.AsString(trackerValue)
			if err != nil {
				return nil, fmt.Errorf("announce-list tier %d entry %d: %w", i, j, err)
			}
			if trackerURL != "" {
				tier = append(tier, trackerURL)
			}
		}
		if len(tier) > 0 {
			result = append(result, tier)
		}
	}
	return result, nil
}

func extractFiles(value bencode.BValue) ([]FileInfo, int64, error) {
	list, err := bencode.AsList(value)
	if err != nil {
		return nil, 0, fmt.Errorf("files: %w", err)
	}
	if len(list) == 0 {
		return nil, 0, errors.New("multi-file torrent has no files")
	}

	files := make([]FileInfo, 0, len(list))
	seen := make(map[string]struct{})
	pathKeys := make([]string, 0, len(list))
	var total int64
	for i, fileValue := range list {
		dict, err := bencode.AsDict(fileValue)
		if err != nil {
			return nil, 0, fmt.Errorf("file %d: %w", i, err)
		}
		length, err := requiredInt64(dict, "length")
		if err != nil {
			return nil, 0, fmt.Errorf("file %d: %w", i, err)
		}
		if length < 0 {
			return nil, 0, fmt.Errorf("file %d has a negative length", i)
		}
		pathValue, ok := dict["path"]
		if !ok {
			return nil, 0, fmt.Errorf("file %d is missing path", i)
		}
		pathList, err := bencode.AsList(pathValue)
		if err != nil || len(pathList) == 0 {
			return nil, 0, fmt.Errorf("file %d has an invalid path", i)
		}
		components := make([]string, len(pathList))
		for j, componentValue := range pathList {
			component, err := bencode.AsString(componentValue)
			if err != nil {
				return nil, 0, fmt.Errorf("file %d path component %d: %w", i, j, err)
			}
			if err := ValidatePathComponent(component); err != nil {
				return nil, 0, fmt.Errorf("file %d path component %d: %w", i, j, err)
			}
			components[j] = component
		}
		key := strings.ToLower(strings.Join(components, "\x00"))
		if _, duplicate := seen[key]; duplicate {
			return nil, 0, fmt.Errorf("duplicate file path %q", strings.Join(components, "/"))
		}
		seen[key] = struct{}{}
		pathKeys = append(pathKeys, key)
		if length > math.MaxInt64-total {
			return nil, 0, errors.New("total torrent length overflows int64")
		}
		total += length
		files = append(files, FileInfo{Length: length, Path: components})
	}
	sort.Strings(pathKeys)
	for i := 0; i+1 < len(pathKeys); i++ {
		if strings.HasPrefix(pathKeys[i+1], pathKeys[i]+"\x00") {
			return nil, 0, errors.New("a torrent file path is also used as a directory")
		}
	}
	return files, total, nil
}

// ValidatePathComponent rejects absolute paths, traversal, separators and
// Windows alternate-data-stream syntax on every platform.
func ValidatePathComponent(component string) error {
	if component == "" || component == "." || component == ".." {
		return errors.New("path component is empty or relative")
	}
	if !utf8.ValidString(component) {
		return errors.New("path component is not valid UTF-8")
	}
	for _, character := range component {
		if unicode.IsControl(character) {
			return errors.New("path component contains a control character")
		}
	}
	if strings.ContainsRune(component, '\x00') || strings.ContainsAny(component, `/\\`) {
		return errors.New("path component contains a separator or NUL")
	}
	if strings.ContainsAny(component, `:<>"|?*`) || filepath.IsAbs(component) || filepath.VolumeName(component) != "" {
		return errors.New("path component is absolute or volume-qualified")
	}
	if strings.HasSuffix(component, " ") || strings.HasSuffix(component, ".") {
		return errors.New("path component has a Windows-unsafe suffix")
	}
	stem := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return errors.New("path component is a reserved Windows device name")
	}
	return nil
}

func requiredString(dict bencode.BDict, key string) (string, error) {
	value, ok := dict[key]
	if !ok {
		return "", fmt.Errorf("missing %s", key)
	}
	result, err := bencode.AsString(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func requiredBytes(dict bencode.BDict, key string) ([]byte, error) {
	value, ok := dict[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	result, err := bencode.AsBytes(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func requiredInt64(dict bencode.BDict, key string) (int64, error) {
	value, ok := dict[key]
	if !ok {
		return 0, fmt.Errorf("missing %s", key)
	}
	result, err := bencode.AsInt64(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return result, nil
}

func pieceCount(length, pieceLength int64) int64 {
	if length == 0 {
		return 0
	}
	return length/pieceLength + boolToInt64(length%pieceLength != 0)
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
