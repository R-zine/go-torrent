package peer

import (
	"bytes"
	"errors"
	"fmt"
)

const protocolString = "BitTorrent protocol"

type Handshake struct {
	Reserved [8]byte
	InfoHash [20]byte
	PeerID   [20]byte
}

func (h *Handshake) Serialize() []byte {
	buffer := make([]byte, 49+len(protocolString))
	buffer[0] = byte(len(protocolString))
	copy(buffer[1:], protocolString)
	reservedStart := 1 + len(protocolString)
	copy(buffer[reservedStart:reservedStart+8], h.Reserved[:])
	copy(buffer[reservedStart+8:reservedStart+28], h.InfoHash[:])
	copy(buffer[reservedStart+28:reservedStart+48], h.PeerID[:])
	return buffer
}

func ParseHandshake(data []byte) (*Handshake, error) {
	if len(data) < 49 {
		return nil, errors.New("handshake too short")
	}
	protocolLength := int(data[0])
	if protocolLength == 0 || len(data) != 49+protocolLength {
		return nil, errors.New("invalid handshake length")
	}
	if string(data[1:1+protocolLength]) != protocolString {
		return nil, fmt.Errorf("unexpected protocol %q", data[1:1+protocolLength])
	}

	reservedStart := 1 + protocolLength
	handshake := &Handshake{}
	copy(handshake.Reserved[:], data[reservedStart:reservedStart+8])
	copy(handshake.InfoHash[:], data[reservedStart+8:reservedStart+28])
	copy(handshake.PeerID[:], data[reservedStart+28:reservedStart+48])
	return handshake, nil
}

func (h *Handshake) Validate(expectedInfoHash [20]byte) error {
	if h == nil {
		return errors.New("handshake is nil")
	}
	if !bytes.Equal(h.InfoHash[:], expectedInfoHash[:]) {
		return errors.New("info hash mismatch")
	}
	return nil
}
