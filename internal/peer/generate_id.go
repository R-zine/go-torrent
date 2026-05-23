package peer

import (
	"crypto/rand"
	"fmt"
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
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

	bytes := make([]byte, length)

	randomBytes := make([]byte, length)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", err
	}

	for i := range bytes {
		bytes[i] = charset[int(randomBytes[i])%len(charset)]
	}

	return string(bytes), nil
}