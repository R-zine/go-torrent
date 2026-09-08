package peer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	MsgChoke         byte = 0
	MsgUnchoke       byte = 1
	MsgInterested    byte = 2
	MsgNotInterested byte = 3
	MsgHave          byte = 4
	MsgBitfield      byte = 5
	MsgRequest       byte = 6
	MsgPiece         byte = 7
	MsgCancel        byte = 8

	DefaultMaxMessageSize uint32 = 4 << 20
)

type Message struct {
	ID      *byte
	Payload []byte
}

func ReadMessage(r io.Reader) (*Message, error) {
	return ReadMessageWithLimit(r, DefaultMaxMessageSize)
}

func ReadMessageWithLimit(r io.Reader, maxLength uint32) (*Message, error) {
	if maxLength == 0 {
		return nil, errors.New("maximum message length must be positive")
	}
	var lengthBuffer [4]byte
	if _, err := io.ReadFull(r, lengthBuffer[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(lengthBuffer[:])
	if length == 0 {
		return &Message{}, nil
	}
	if length > maxLength {
		return nil, fmt.Errorf("peer message length %d exceeds limit %d", length, maxLength)
	}

	messageBuffer := make([]byte, int(length))
	if _, err := io.ReadFull(r, messageBuffer); err != nil {
		return nil, err
	}
	id := messageBuffer[0]
	return &Message{ID: &id, Payload: messageBuffer[1:]}, nil
}

func (m *Message) Serialize() []byte {
	if m == nil || m.ID == nil {
		return []byte{0, 0, 0, 0}
	}
	length := uint32(len(m.Payload) + 1)
	buffer := make([]byte, 4+int(length))
	binary.BigEndian.PutUint32(buffer[:4], length)
	buffer[4] = *m.ID
	copy(buffer[5:], m.Payload)
	return buffer
}

func NewInterested() *Message {
	id := MsgInterested
	return &Message{ID: &id}
}

func NewRequest(index, begin, length int) *Message {
	id := MsgRequest
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], uint32(index))
	binary.BigEndian.PutUint32(payload[4:8], uint32(begin))
	binary.BigEndian.PutUint32(payload[8:12], uint32(length))
	return &Message{ID: &id, Payload: payload}
}

func NewHave(index int) *Message {
	id := MsgHave
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(index))
	return &Message{ID: &id, Payload: payload}
}

func ParseHave(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, errors.New("invalid have payload length")
	}
	value := binary.BigEndian.Uint32(payload)
	if uint64(value) > uint64(maxInt()) {
		return 0, errors.New("have index does not fit in int")
	}
	return int(value), nil
}

func ParsePiece(payload []byte) (index, begin int, block []byte, err error) {
	if len(payload) < 8 {
		return 0, 0, nil, errors.New("invalid piece payload")
	}
	indexValue := binary.BigEndian.Uint32(payload[0:4])
	beginValue := binary.BigEndian.Uint32(payload[4:8])
	if uint64(indexValue) > uint64(maxInt()) || uint64(beginValue) > uint64(maxInt()) {
		return 0, 0, nil, errors.New("piece position does not fit in int")
	}
	return int(indexValue), int(beginValue), payload[8:], nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
