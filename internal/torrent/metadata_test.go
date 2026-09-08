package torrent

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"sort"
	"testing"

	"bittorrent-client/internal/bencode"
)

func TestExtractMetaSingleFile(t *testing.T) {
	info := singleFileInfo("sample.bin", []byte("abcdefgh"), 4)
	raw := encodeBencode(map[string]any{
		"announce": "https://tracker.example/announce?passkey=secret",
		"announce-list": []any{
			[]any{"udp://one.example:80/announce"},
			[]any{"https://two.example/announce"},
		},
		"info": info,
	})
	meta := parseMeta(t, raw)

	if meta.Name != "sample.bin" || meta.Length != 8 || meta.PieceLength != 4 || meta.MultiFile {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if len(meta.Pieces) != 2 {
		t.Fatalf("piece count = %d, want 2", len(meta.Pieces))
	}
	wantHash := sha1.Sum(encodeBencode(info))
	if meta.InfoHash != wantHash {
		t.Fatalf("info hash = %x, want %x", meta.InfoHash, wantHash)
	}
	urls := meta.TrackerURLs()
	if len(urls) != 3 || urls[0] != "https://tracker.example/announce?passkey=secret" {
		t.Fatalf("TrackerURLs() = %v", urls)
	}
}

func TestExtractMetaMultiFile(t *testing.T) {
	content := []byte("abcde")
	info := map[string]any{
		"files": []any{
			map[string]any{"length": int64(2), "path": []any{"one.bin"}},
			map[string]any{"length": int64(3), "path": []any{"dir", "two.bin"}},
		},
		"name":         "bundle",
		"piece length": int64(4),
		"pieces":       pieceHashes(content, 4),
	}
	raw := encodeBencode(map[string]any{"announce": "http://tracker.test", "info": info})
	meta := parseMeta(t, raw)
	if !meta.MultiFile || meta.Length != 5 || len(meta.Files) != 2 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if got := meta.Files[1].Path; len(got) != 2 || got[0] != "dir" || got[1] != "two.bin" {
		t.Fatalf("second path = %v", got)
	}
}

func TestExtractMetaRejectsInvalidMetadata(t *testing.T) {
	validFiles := []any{map[string]any{"length": int64(4), "path": []any{"a.bin"}}}
	tests := []struct {
		name string
		root map[string]any
	}{
		{
			name: "missing tracker",
			root: map[string]any{"info": singleFileInfo("a", []byte("data"), 4)},
		},
		{
			name: "traversal name",
			root: map[string]any{"announce": "http://tracker", "info": singleFileInfo("..\\escape", []byte("data"), 4)},
		},
		{
			name: "zero piece length",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"length": int64(4), "name": "a", "piece length": int64(0), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
		{
			name: "oversized piece length",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"length": int64(0), "name": "a", "piece length": int64(MaxPieceLength + 1), "pieces": []byte{},
				},
			},
		},
		{
			name: "negative length",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"length": int64(-1), "name": "a", "piece length": int64(4), "pieces": []byte{},
				},
			},
		},
		{
			name: "piece count mismatch",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"length": int64(5), "name": "a", "piece length": int64(4), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
		{
			name: "length and files",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"files": validFiles, "length": int64(4), "name": "a", "piece length": int64(4), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
		{
			name: "unsafe file path",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"files": []any{map[string]any{"length": int64(4), "path": []any{"..", "escape"}}},
					"name":  "a", "piece length": int64(4), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
		{
			name: "duplicate file path",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"files": []any{
						map[string]any{"length": int64(2), "path": []any{"a"}},
						map[string]any{"length": int64(2), "path": []any{"A"}},
					},
					"name": "bundle", "piece length": int64(4), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
		{
			name: "file and directory conflict",
			root: map[string]any{
				"announce": "http://tracker",
				"info": map[string]any{
					"files": []any{
						map[string]any{"length": int64(2), "path": []any{"a"}},
						map[string]any{"length": int64(2), "path": []any{"a", "b"}},
					},
					"name": "bundle", "piece length": int64(4), "pieces": pieceHashes([]byte("data"), 4),
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := encodeBencode(test.root)
			root, err := bencode.NewParser(raw).Parse()
			if err != nil {
				t.Fatalf("test fixture is invalid: %v", err)
			}
			if _, err := ExtractMeta(root, raw); err == nil {
				t.Fatal("ExtractMeta() unexpectedly succeeded")
			}
		})
	}
}

func TestEmptyTorrentIsConsistent(t *testing.T) {
	info := singleFileInfo("empty", nil, 16)
	raw := encodeBencode(map[string]any{"announce": "http://tracker", "info": info})
	meta := parseMeta(t, raw)
	if meta.Length != 0 || len(meta.Pieces) != 0 {
		t.Fatalf("empty metadata = %+v", meta)
	}
}

func TestValidatePathComponent(t *testing.T) {
	valid := []string{"file.txt", "дані.bin", "two words"}
	for _, component := range valid {
		if err := ValidatePathComponent(component); err != nil {
			t.Errorf("ValidatePathComponent(%q) = %v", component, err)
		}
	}
	invalid := []string{"", ".", "..", "../x", `..\x`, "/absolute", `C:\absolute`, "stream:name", "bad?name", "CON.txt", "trail.", "line\nbreak", "a\x00b"}
	invalid[len(invalid)-1] = "a" + string(rune(0)) + "b"
	for _, component := range invalid {
		if err := ValidatePathComponent(component); err == nil {
			t.Errorf("ValidatePathComponent(%q) unexpectedly succeeded", component)
		}
	}
}

func parseMeta(t *testing.T, raw []byte) *TorrentMeta {
	t.Helper()
	root, err := bencode.NewParser(raw).Parse()
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	meta, err := ExtractMeta(root, raw)
	if err != nil {
		t.Fatalf("ExtractMeta() error = %v", err)
	}
	return meta
}

func singleFileInfo(name string, content []byte, pieceLength int) map[string]any {
	return map[string]any{
		"length":       int64(len(content)),
		"name":         name,
		"piece length": int64(pieceLength),
		"pieces":       pieceHashes(content, pieceLength),
	}
}

func pieceHashes(content []byte, pieceLength int) []byte {
	var result []byte
	for len(content) > 0 {
		length := pieceLength
		if len(content) < length {
			length = len(content)
		}
		hash := sha1.Sum(content[:length])
		result = append(result, hash[:]...)
		content = content[length:]
	}
	return result
}

func encodeBencode(value any) []byte {
	var buffer bytes.Buffer
	var encode func(any)
	encode = func(value any) {
		switch value := value.(type) {
		case string:
			fmt.Fprintf(&buffer, "%d:%s", len(value), value)
		case []byte:
			fmt.Fprintf(&buffer, "%d:", len(value))
			buffer.Write(value)
		case int64:
			fmt.Fprintf(&buffer, "i%de", value)
		case []any:
			buffer.WriteByte('l')
			for _, item := range value {
				encode(item)
			}
			buffer.WriteByte('e')
		case map[string]any:
			buffer.WriteByte('d')
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				encode(key)
				encode(value[key])
			}
			buffer.WriteByte('e')
		default:
			panic(fmt.Sprintf("unsupported bencode test type %T", value))
		}
	}
	encode(value)
	return buffer.Bytes()
}
