package peer

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const (
	clientPrefix = "-GO0001-"
	randomLength = 12
	totalLength  = 20
)

// GeneratePeerID creates a 20-byte BitTorrent peer ID.
//
// Example:
// -GO0001-a1b2c3d4e5f6

func GeneratePeerID() ([20]byte, error) {
	var peerID [20]byte

	randomPart, err := generateRandomString(randomLength)
	if err != nil {
		return peerID, err
	}

	fullID := clientPrefix + randomPart

	if len(fullID) != totalLength {
		return peerID, fmt.Errorf(
			"invalid peer id length: got %d expected %d",
			len(fullID),
			totalLength,
		)
	}

	copy(peerID[:], []byte(fullID))

	return peerID, nil
}

func generateRandomString(length int) (string, error) {
	if length < 0 {
		return "", fmt.Errorf("random string length must not be negative")
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

	result := make([]byte, length)
	upperBound := big.NewInt(int64(len(charset)))
	for i := range result {
		index, err := rand.Int(rand.Reader, upperBound)
		if err != nil {
			return "", err
		}
		result[i] = charset[index.Int64()]
	}
	return string(result), nil
}
