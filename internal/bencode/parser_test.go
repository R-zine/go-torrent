package bencode

import (
	"crypto/sha1"
	"strings"
	"testing"
)

func TestParserValidValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(*testing.T, *BValue)
	}{
		{
			name:  "empty string",
			input: "0:",
			check: func(t *testing.T, value *BValue) {
				got, err := AsBytes(*value)
				if err != nil || len(got) != 0 {
					t.Fatalf("AsBytes() = %q, %v", got, err)
				}
			},
		},
		{
			name:  "negative integer",
			input: "i-42e",
			check: func(t *testing.T, value *BValue) {
				got, err := AsInt64(*value)
				if err != nil || got != -42 {
					t.Fatalf("AsInt64() = %d, %v", got, err)
				}
			},
		},
		{
			name:  "list",
			input: "l4:spami1ee",
			check: func(t *testing.T, value *BValue) {
				got, err := AsList(*value)
				if err != nil || len(got) != 2 {
					t.Fatalf("AsList() length = %d, %v", len(got), err)
				}
			},
		},
		{
			name:  "sorted dictionary",
			input: "d1:ai1e1:b1:xe",
			check: func(t *testing.T, value *BValue) {
				got, err := AsDict(*value)
				if err != nil || len(got) != 2 {
					t.Fatalf("AsDict() length = %d, %v", len(got), err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := NewParser([]byte(test.input)).Parse()
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if value.Start != 0 || value.End != len(test.input) {
				t.Fatalf("range = [%d,%d), want [0,%d)", value.Start, value.End, len(test.input))
			}
			test.check(t, value)
		})
	}
}

func TestParserRejectsMalformedAndNonCanonicalInput(t *testing.T) {
	tests := []string{
		"",
		"ie",
		"i03e",
		"i-0e",
		"i+1e",
		"i1",
		"01:a",
		"3:ab",
		"d-1:ai1ee",
		"d1:bi1e1:ai2ee",
		"d1:ai1e1:ai2ee",
		"i1ejunk",
		"le",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Parse() panicked: %v", recovered)
				}
			}()
			_, err := NewParser([]byte(input)).Parse()
			if input == "le" {
				if err != nil {
					t.Fatalf("empty list should be valid: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			}
		})
	}
}

func TestParserLimits(t *testing.T) {
	stringParser := NewParser([]byte("5:hello"))
	stringParser.SetLimits(4, 0)
	if _, err := stringParser.Parse(); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("string limit error = %v", err)
	}

	depthParser := NewParser([]byte("ll1:xee"))
	depthParser.SetLimits(0, 1)
	if _, err := depthParser.Parse(); err == nil || !strings.Contains(err.Error(), "nesting depth") {
		t.Fatalf("depth limit error = %v", err)
	}
}

func TestExtractInfoHashUsesExactEncodedRange(t *testing.T) {
	raw := []byte("d4:infod4:name1:xee")
	root, err := NewParser(raw).Parse()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ExtractInfoHash(root, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := sha1.Sum([]byte("d4:name1:xe"))
	if got != want {
		t.Fatalf("hash = %x, want %x", got, want)
	}
}

func TestExtractInfoHashRejectsInvalidRanges(t *testing.T) {
	root := &BValue{Value: BDict{"info": {Value: BDict{}, Start: 0, End: 99}}}
	if _, err := ExtractInfoHash(root, []byte("short")); err == nil {
		t.Fatal("ExtractInfoHash() unexpectedly succeeded")
	}
	if _, err := ExtractInfoHash(nil, nil); err == nil {
		t.Fatal("ExtractInfoHash(nil) unexpectedly succeeded")
	}
}

func TestAsHelpersRejectWrongTypes(t *testing.T) {
	value := BValue{Value: BInt(1)}
	if _, err := AsString(value); err == nil {
		t.Fatal("AsString() unexpectedly succeeded")
	}
	if _, err := AsBytes(value); err == nil {
		t.Fatal("AsBytes() unexpectedly succeeded")
	}
	if _, err := AsList(value); err == nil {
		t.Fatal("AsList() unexpectedly succeeded")
	}
	if _, err := AsDict(value); err == nil {
		t.Fatal("AsDict() unexpectedly succeeded")
	}
}

func FuzzParserDoesNotPanic(f *testing.F) {
	f.Add([]byte("d4:infod4:name1:xee"))
	f.Add([]byte("d-1:ai1ee"))
	f.Add([]byte("i9223372036854775808e"))
	f.Fuzz(func(t *testing.T, data []byte) {
		parser := NewParser(data)
		parser.SetLimits(1<<20, 32)
		_, _ = parser.Parse()
	})
}
