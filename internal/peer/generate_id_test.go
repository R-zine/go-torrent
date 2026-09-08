package peer

import (
	"strings"
	"testing"
)

func TestGeneratePeerID(t *testing.T) {
	first, err := GeneratePeerID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePeerID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first[:]), clientPrefix) || len(first) != totalLength {
		t.Fatalf("peer ID = %q", first)
	}
	if first == second {
		t.Fatal("two generated peer IDs were equal")
	}
}

func TestGenerateRandomStringLength(t *testing.T) {
	value, err := generateRandomString(128)
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 128 {
		t.Fatalf("length = %d", len(value))
	}
	for _, character := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789", character) {
			t.Fatalf("unexpected character %q", character)
		}
	}
	if _, err := generateRandomString(-1); err == nil {
		t.Fatal("negative length unexpectedly accepted")
	}
}
