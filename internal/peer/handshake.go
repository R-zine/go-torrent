package peer

import (
	"bytes"
	"fmt"
)

const protocolString = "BitTorrent protocol"

type Handshake struct {
	InfoHash [20]byte
	PeerID   [20]byte
}

func (h *Handshake) Serialize() []byte {
	buffer := make([]byte, 68)

	// protocol string length
	buffer[0] = byte(len(protocolString))

	// protocol string
	copy(buffer[1:20], protocolString)

	// reserved bytes are already zeroed
	// buffer[20:28]

	// info hash
	copy(buffer[28:48], h.InfoHash[:])

	// peer id
	copy(buffer[48:68], h.PeerID[:])

	return buffer
}

func ParseHandshake(data []byte) (*Handshake, error) {
	if len(data) < 68 {
		return nil, fmt.Errorf("handshake too short")
	}

	pstrlen := int(data[0])

	if len(data) != 49+pstrlen {
		return nil, fmt.Errorf("invalid handshake length")
	}

	protocol := string(data[1 : 1+pstrlen])

	if protocol != protocolString {
		return nil, fmt.Errorf("unexpected protocol: %s", protocol)
	}

	var infoHash [20]byte
	copy(infoHash[:], data[28:48])

	var peerID [20]byte
	copy(peerID[:], data[48:68])

	return &Handshake{
		InfoHash: infoHash,
		PeerID:   peerID,
	}, nil
}

func (h *Handshake) Validate(expectedInfoHash [20]byte) error {
	if !bytes.Equal(h.InfoHash[:], expectedInfoHash[:]) {
		return fmt.Errorf("info hash mismatch")
	}

	return nil
}